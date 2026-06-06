package infra

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gopkg.in/yaml.v3"

	"github.com/hmchangw/chat/pkg/stream"
)

type SourceSpec struct {
	On         string `yaml:"on"`
	Stream     string `yaml:"stream"`
	FromStream string `yaml:"from_stream"`
	Filter     string `yaml:"filter"`
}

type federationCatalog struct {
	Sources []SourceSpec `yaml:"sources"`
}

var federationKnownSites = map[string]struct{}{
	"site-a": {},
	"site-b": {},
}

func LoadFederationSources(path string) ([]SourceSpec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("federation: read %s: %w", path, err)
	}
	var cat federationCatalog
	if err := yaml.Unmarshal(body, &cat); err != nil {
		return nil, fmt.Errorf("federation: parse %s: %w", path, err)
	}
	for i, s := range cat.Sources {
		if _, ok := federationKnownSites[s.On]; !ok {
			return nil, fmt.Errorf("federation: sources[%d].on = %q; must be site-a or site-b", i, s.On)
		}
	}
	return cat.Sources, nil
}

// peerDomain returns "site-b" for "site-a" and vice versa.
func peerDomain(site string) string {
	if site == "site-a" {
		return "site-b"
	}
	return "site-a"
}

// Apply creates JetStream Sources on each target INBOX. Takes a map of
// site → admin conn (one per site) — the JS API isn't carried across the
// supercluster gateway in our trust-chain config, so each spec is
// applied via the conn that's local to its target site. The Source's
// Domain = peer site lives in the stream config; NATS dials the peer
// through the gateway at message-fetch time, which DOES traverse.
func Apply(ctx context.Context, specs []SourceSpec, adminBySite map[string]*nats.Conn) error {
	for _, s := range specs {
		admin, ok := adminBySite[s.On]
		if !ok {
			return fmt.Errorf("federation Apply: no admin conn for site %s", s.On)
		}
		js, err := jetstream.NewWithDomain(admin, s.On)
		if err != nil {
			return fmt.Errorf("federation Apply: js context for %s: %w", s.On, err)
		}
		_, err = js.UpdateStream(ctx, jetstream.StreamConfig{
			Name: s.Stream,
			Sources: []*jetstream.StreamSource{{
				Name:          s.FromStream,
				FilterSubject: s.Filter,
				Domain:        peerDomain(s.On),
			}},
		})
		if err != nil {
			return fmt.Errorf("federation Apply: UpdateStream %s on %s: %w", s.Stream, s.On, err)
		}
	}
	return nil
}

// CreateOutboxStreams creates OUTBOX_<site> on each site's local
// JetStream domain. OUTBOX is the source side of every cross-site
// federation Source — production code (room-worker, room-service,
// message-worker) publishes to outbox.<site>.> subjects when an event
// must reach a peer site, and the federation Source on the peer's
// INBOX sources from OUTBOX_<this>. Without the stream existing,
// production publishes return "no response from stream" and federation
// silently fails to fire — for ALL metadata-federation events
// (member_added, role_updated, subscription_*, room_renamed, etc.).
//
// In production ops/IaC own OUTBOX (per CLAUDE.md §"Stream bootstrap
// ownership"). In the multi-site harness the infra layer is the ops
// analog: it owns stream orchestration, including the federation
// streams no service bootstraps.
func CreateOutboxStreams(ctx context.Context, adminBySite map[string]*nats.Conn) error {
	for site, admin := range adminBySite {
		js, err := jetstream.NewWithDomain(admin, site)
		if err != nil {
			return fmt.Errorf("outbox: js context for %s: %w", site, err)
		}
		cfg := stream.Outbox(site)
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:     cfg.Name,
			Subjects: cfg.Subjects,
		}); err != nil {
			return fmt.Errorf("outbox: CreateOrUpdateStream %s on %s: %w", cfg.Name, site, err)
		}
	}
	return nil
}

// WaitForStream polls until the named stream exists on the given
// JetStream domain. Used after services come up but before Apply.
func WaitForStream(ctx context.Context, admin *nats.Conn, domain, stream string) error {
	js, err := jetstream.NewWithDomain(admin, domain)
	if err != nil {
		return err
	}
	for {
		_, err := js.Stream(ctx, stream)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for stream %s on %s: %w", stream, domain, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
