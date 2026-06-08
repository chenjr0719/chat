package pollers

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// JetStreamConsumePoller is the universal `jetstream_consume`
// primitive. Per-poll args (parsed each call):
//
//	args.stream          string  required — JetStream stream name
//	args.filter_subject  string  required — subject filter for the
//	                                        ephemeral consumer
//
// Stateful: opening a JetStream consumer is expensive, so the
// poller caches one StreamPoller per (stream, filter_subject) tuple
// on first PollFn call. Subsequent calls with the same args reuse
// the existing consumer's buffer. Closing the poller (via the
// cleanup func returned from RegisterBuiltinPollers) closes every
// cached consumer.
//
// Phase 4.0 universal-primitive design: no scenario-specific stream
// names compiled into Go. The same primitive serves messages-canonical,
// rooms-canonical, inbox-aggregate, outbox, or any future stream the
// YAML names.
type JetStreamConsumePoller struct {
	conn      *nats.Conn
	startTime time.Time

	mu    sync.Mutex
	cache map[string]*StreamPoller
}

// NewJetStreamConsumePoller builds the singleton primitive. conn must
// be a connected admin NATS connection capable of opening ephemeral
// consumers. Register under "jetstream_consume".
//
// Nil conn produces a poller whose PollFn returns nil + warns —
// useful when the runner was invoked without NATSCredsFile and the
// operator still expects scenarios that reference jetstream_consume
// to fail loudly at assertion time rather than at startup.
func NewJetStreamConsumePoller(conn *nats.Conn, startTime time.Time) *JetStreamConsumePoller {
	return &JetStreamConsumePoller{
		conn:      conn,
		startTime: startTime,
		cache:     map[string]*StreamPoller{},
	}
}

// PollFn returns a closure that polls the (stream, filter_subject)
// consumer. First call for a given args tuple opens the consumer; the
// underlying StreamPoller's drain goroutine starts immediately so the
// buffer fills concurrently with the Gomega Eventually loop.
func (p *JetStreamConsumePoller) PollFn(args map[string]any, tp string) func() []readers.Event {
	stream, _ := args["stream"].(string)
	filter, _ := args["filter_subject"].(string)
	if stream == "" || filter == "" {
		return func() []readers.Event {
			slog.Warn("jetstream_consume: args.stream and args.filter_subject are required",
				"stream", args["stream"], "filter_subject", args["filter_subject"])
			return nil
		}
	}
	if p.conn == nil {
		return func() []readers.Event {
			slog.Warn("jetstream_consume: no admin NATS connection (NATS_CREDS_FILE unset?)",
				"stream", stream, "filter_subject", filter)
			return nil
		}
	}

	inner, err := p.getOrOpen(stream, filter)
	if err != nil {
		slog.Warn("jetstream_consume: open consumer", "stream", stream, "filter_subject", filter, "err", err)
		return func() []readers.Event { return nil }
	}
	return inner.PollFn(args, tp)
}

// getOrOpen returns the cached StreamPoller for (stream, filter) or
// opens a fresh one. Holds the poller's mutex for the duration of the
// open call so two concurrent assertions on the same args don't race
// to create two consumers.
func (p *JetStreamConsumePoller) getOrOpen(stream, filter string) (*StreamPoller, error) {
	key := stream + "|" + filter
	p.mu.Lock()
	defer p.mu.Unlock()
	if sp, ok := p.cache[key]; ok {
		return sp, nil
	}
	rdr := readers.NewJetStreamSubjectReader(p.conn, stream, filter, "jetstream_consume", "")
	sp, err := NewStreamPoller(rdr, "", p.startTime)
	if err != nil {
		return nil, fmt.Errorf("open jetstream consumer (stream=%s filter=%s): %w", stream, filter, err)
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
