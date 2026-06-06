package pollers

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// JetStreamConsumePoller is the universal `jetstream_consume`
// primitive. Per-poll args (parsed each call):
//
//	args.stream          string  required — JetStream stream name
//	args.filter_subject  string  required — subject filter for the
//	                                        ephemeral consumer
//
// Stateful: opening a JetStream consumer is expensive, so the
// poller caches one StreamPoller per (site, stream, filter_subject)
// tuple on first PollFn call. Subsequent calls with the same args
// reuse the existing consumer's buffer. Closing the poller (via the
// cleanup func returned from RegisterBuiltinPollers) closes every
// cached consumer.
//
// Per-site admin conns: the supercluster gateway routes app traffic
// but does NOT carry $JS.<domain>.API across — a single admin conn
// can drive only its local domain. The conns map holds one nats.Conn
// per site so JS API calls hit the correct local NATS.
type JetStreamConsumePoller struct {
	conns     map[string]*nats.Conn
	startTime time.Time

	mu    sync.Mutex
	cache map[string]*StreamPoller
}

// NewJetStreamConsumePoller builds the singleton primitive. conns must
// hold one admin NATS connection per site (keyed by site name) so JS
// API calls hit the correct local domain — the gateway doesn't route
// $JS.<domain>.API across. Register under "jetstream_consume".
//
// Nil or missing-site conn produces a PollFn that returns nil + warns
// — useful when the runner was invoked without NATSCredsFile and the
// operator still expects scenarios that reference jetstream_consume
// to fail loudly at assertion time rather than at startup.
func NewJetStreamConsumePoller(conns map[string]*nats.Conn, startTime time.Time) *JetStreamConsumePoller {
	return &JetStreamConsumePoller{
		conns:     conns,
		startTime: startTime,
		cache:     map[string]*StreamPoller{},
	}
}

// PollFn returns a closure that polls the (site, stream, filter_subject)
// consumer. First call for a given args tuple opens the consumer; the
// underlying StreamPoller's drain goroutine starts immediately so the
// buffer fills concurrently with the Gomega Eventually loop.
//
// site picks the admin conn from p.conns and opens the consumer on
// that conn's local JetStream domain. Cross-domain JS API calls don't
// traverse the supercluster gateway under our trust-chain config.
func (p *JetStreamConsumePoller) PollFn(site string, args map[string]any, tp string) func() []readers.Event {
	stream, _ := args["stream"].(string)
	filter, _ := args["filter_subject"].(string)
	if stream == "" || filter == "" {
		return func() []readers.Event {
			slog.Warn("jetstream_consume: args.stream and args.filter_subject are required",
				"stream", args["stream"], "filter_subject", args["filter_subject"])
			return nil
		}
	}
	conn := p.conns[site]
	if conn == nil {
		return func() []readers.Event {
			slog.Warn("jetstream_consume: no admin NATS connection for site (NATS_CREDS_FILE unset or site not in admin-conn map?)",
				"site", site, "stream", stream, "filter_subject", filter)
			return nil
		}
	}

	inner, err := p.getOrOpen(conn, site, stream, filter)
	if err != nil {
		slog.Warn("jetstream_consume: open consumer", "site", site, "stream", stream, "filter_subject", filter, "err", err)
		return func() []readers.Event { return nil }
	}
	return inner.PollFn("", args, tp)
}

// getOrOpen returns the cached StreamPoller for (site, stream, filter)
// or opens a fresh one on the supplied conn. Holds the poller's mutex
// for the duration of the open call so two concurrent assertions on
// the same args don't race to create two consumers.
func (p *JetStreamConsumePoller) getOrOpen(conn *nats.Conn, site, stream, filter string) (*StreamPoller, error) {
	key := site + "|" + stream + "|" + filter
	p.mu.Lock()
	defer p.mu.Unlock()
	if sp, ok := p.cache[key]; ok {
		return sp, nil
	}
	rdr := readers.NewJetStreamSubjectReaderWithDomain(conn, site, stream, filter, "jetstream_consume", "")
	sp, err := NewStreamPoller(rdr, "", p.startTime)
	if err != nil {
		return nil, fmt.Errorf("open jetstream consumer (site=%s stream=%s filter=%s): %w", site, stream, filter, err)
	}
	p.cache[key] = sp
	return sp, nil
}

// Close terminates every cached consumer. Called by Sandbox.Teardown
// via the cleanup func that RegisterBuiltinPollers returns.
func (p *JetStreamConsumePoller) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sp := range p.cache {
		sp.Close()
	}
	p.cache = map[string]*StreamPoller{}
}

// Compile-time interface check.
var _ Poller = (*JetStreamConsumePoller)(nil)
