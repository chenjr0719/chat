package infra

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gopkg.in/yaml.v3"
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

// Apply attaches federation Sources (with SubjectTransforms) to each
// target INBOX. The transform rewrites the inbound subject from
// `outbox.{peer}.to.{site}.>` to `chat.inbox.{site}.aggregate.>` so
// federated events land under the consumer-bind namespace (see
// pkg/stream.Inbox docstring).
//
// IMPORTANT — fetch-mutate-write, not blind UpdateStream. NATS
// UpdateStream REPLACES the entire StreamConfig with what's posted;
// any field omitted on the call (e.g. Subjects, Retention, MaxAge)
// gets reset to its zero value. inbox-worker's bootstrapStreams sets
// Name + Subjects = [chat.inbox.{site}.*, chat.inbox.{site}.aggregate.>]
// so that same-site publishes (room-worker → chat.inbox.{site}.member_added)
// have a stream to land on. A blind `UpdateStream{Name, Sources}` here
// would wipe Subjects to [] and same-site publishes would silently
// fail with "no response from stream" (observed in Runs 73-74). We
// fetch the existing config, mutate only Sources, and write back.
//
// The transform's Source field doubles as the Source-consumer filter
// — NATS only pulls matching subjects. StreamSource.FilterSubject is
// deliberately empty: setting both FilterSubject AND a SubjectTransform
// with the same pattern trips NATS error 10137 ("consumer with multiple
// subject filters cannot use subject based API") on the cross-cluster
// Source consumer (observed in Run 65).
//
// Each spec is applied via the admin conn local to its target site.
// The leafnode transport between sites carries the $JS API for Source
// pulls plus the inbound message traffic itself.
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
		existing, err := js.Stream(ctx, s.Stream)
		if err != nil {
			return fmt.Errorf("federation Apply: fetch %s on %s: %w", s.Stream, s.On, err)
		}
		cfg := existing.CachedInfo().Config
		cfg.Sources = []*jetstream.StreamSource{{
			Name:   s.FromStream,
			Domain: peerDomain(s.On),
			SubjectTransforms: []jetstream.SubjectTransformConfig{{
				Source:      s.Filter,
				Destination: fmt.Sprintf("chat.inbox.%s.aggregate.>", s.On),
			}},
		}}
		if _, err := js.UpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("federation Apply: UpdateStream %s on %s: %w", s.Stream, s.On, err)
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
