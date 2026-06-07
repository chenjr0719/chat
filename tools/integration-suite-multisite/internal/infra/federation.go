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

// Apply creates JetStream Sources on each target INBOX. Each Source
// carries a SubjectTransform that rewrites the inbound subject from
// `outbox.{peer}.to.{site}.>` to `chat.inbox.{site}.aggregate.>`
// before the message lands in the destination stream. The transform
// matches the chat-app's documented INBOX schema (see pkg/stream.Inbox
// docstring) — the `chat.inbox.{site}.aggregate.>` subject pattern
// only exists because federated events arrive under it. Without the
// transform the source-delivered subject stays as outbox.* and the
// inbox-worker consumer (bound to chat.inbox.{site}.aggregate.>) never
// sees the message.
//
// The transform's Source field doubles as the Source-consumer filter
// — NATS only pulls messages matching it. We deliberately leave
// StreamSource.FilterSubject empty: setting both FilterSubject AND a
// SubjectTransform with the same pattern trips NATS error 10137
// ("consumer with multiple subject filters cannot use subject based
// API") on the cross-cluster Source consumer, because NATS counts the
// two as separate subject filters on a single legacy-API consumer
// (observed in Run 65).
//
// Each spec is applied via the admin conn that's local to its target
// site. The leafnode transport between sites carries the $JS API for
// Source pulls plus the inbound message traffic itself.
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
				Name:   s.FromStream,
				Domain: peerDomain(s.On),
				SubjectTransforms: []jetstream.SubjectTransformConfig{{
					Source:      s.Filter,
					Destination: fmt.Sprintf("chat.inbox.%s.aggregate.>", s.On),
				}},
			}},
		})
		if err != nil {
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
