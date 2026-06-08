package pollers

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// NATSSubscribePoller is the universal `nats_subscribe` primitive.
// Per-poll args (parsed each call):
//
//	args.subject  string  required — Core NATS subject (literal or
//	                                  wildcard: `*` single-token,
//	                                  `>` multi-token tail)
//
// Implements both Poller and Warmer. The Warmer hook is the
// architectural pivot vs. jetstream_consume — Core NATS has no
// DeliverByStartTime replay, so the subscription MUST open before
// the case-runner fires the verb. case_runner.go Step 3b invokes
// Warm during a per-case pre-fire walk over each expected block's
// poller.
//
// Stateful per-subject cache mirrors jetstream_consume's pattern:
// the same subject across multiple cases in one scenario shares a
// single subscription. Cleanup at Sandbox.Teardown (via the cleanup
// func returned by RegisterBuiltinPollers) unsubscribes every
// cached entry and clears the cache.
//
// Phase 4.0 universal-primitive design: no application-specific
// subject names compiled into Go. The same primitive serves
// broadcast-worker's chat.room.*.event, notification-worker's
// chat.user.*.event.room, or any future Core NATS subject the YAML
// names.
type NATSSubscribePoller struct {
	conn *nats.Conn

	mu    sync.Mutex
	cache map[string]*natsSubEntry
}

// natsSubEntry is one cached subscription's state. The reader owns
// the live nats.Subscription and the synchronised queue; the
// received slice is the monotonic accumulator the poller hands to
// MatchShape on every PollFn call so the Eventually loop sees the
// observation window grow.
type natsSubEntry struct {
	reader   *readers.NATSSubscribeReader
	received []readers.NATSReceivedMessage
}

// NewNATSSubscribePoller builds the singleton primitive. conn must be
// a connected NATS connection (typically the admin conn shared with
// jetstream_consume). Nil conn produces a poller whose Warm + PollFn
// both warn — matches jetstream_consume's nil-tolerant pattern so
// scenarios that reference nats_subscribe still register and surface
// a clear "no admin NATS connection" reason at assertion time.
func NewNATSSubscribePoller(conn *nats.Conn) *NATSSubscribePoller {
	return &NATSSubscribePoller{
		conn:  conn,
		cache: map[string]*natsSubEntry{},
	}
}

// Warm opens the subscription if not already cached. Idempotent —
// repeat calls with the same subject are no-ops.
//
// Hard-fails on Subscribe error: a bad subject or closed connection
// is operator-actionable, and degrading would silently morph the
// case into a guaranteed empty-buffer timeout with a misleading
// reason. Nil conn is a soft degrade (warn + return nil) so
// scenarios that reference nats_subscribe without the admin conn
// still register and time out cleanly.
func (p *NATSSubscribePoller) Warm(args map[string]any) error {
	subject, _ := args["subject"].(string)
	if subject == "" {
		return fmt.Errorf("nats_subscribe: args.subject is required and must be a string")
	}
	if p.conn == nil {
		slog.Warn("nats_subscribe: no admin NATS connection (NATS_CREDS_FILE unset?); subscription not opened",
			"subject", subject)
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, ok := p.cache[subject]; ok {
		return nil
	}

	rdr := readers.NewNATSSubscribeReader(p.conn, subject)
	if err := rdr.Open(); err != nil {
		return fmt.Errorf("nats_subscribe: open subscription on %q: %w", subject, err)
	}
	p.cache[subject] = &natsSubEntry{reader: rdr}
	return nil
}

// PollFn returns the drained-buffer closure. First-pass behavior is
// monotonic: each call appends newly-arrived messages to the cached
// `received` accumulator and emits ONE Event whose Payload carries
// the full accumulated slice. MatchShape's array branch (Phase 4.4
// ROSM) handles the element-wise assertion on the received slice.
func (p *NATSSubscribePoller) PollFn(args map[string]any, _ string) func() []readers.Event {
	subject, _ := args["subject"].(string)
	if subject == "" {
		return func() []readers.Event {
			slog.Warn("nats_subscribe: args.subject is required and must be a string",
				"got", args["subject"])
			return nil
		}
	}
	if p.conn == nil {
		return func() []readers.Event {
			slog.Warn("nats_subscribe: no admin NATS connection (NATS_CREDS_FILE unset?)",
				"subject", subject)
			return nil
		}
	}

	return func() []readers.Event {
		p.mu.Lock()
		entry, ok := p.cache[subject]
		p.mu.Unlock()
		if !ok {
			// Defensive — Warm should have run first. If a scenario
			// ever calls PollFn without Warm (the case_runner skips
			// Step 3b for non-Warmer pollers, but nats_subscribe IS
			// Warmer), we surface a slog Warn so the operator sees the
			// ordering bug.
			slog.Warn("nats_subscribe: PollFn before Warm — Warmer hook may be skipped",
				"subject", subject)
			return nil
		}

		entry.received = entry.reader.Drain(entry.received)
		return []readers.Event{{
			Location:  "nats_subscribe",
			Timestamp: time.Now(),
			Type:      readers.EventCascade,
			Payload: readers.NATSSubscribePayload{
				Subject:  subject,
				Received: cloneReceived(entry.received),
			},
		}}
	}
}

// Close terminates every cached subscription. Called by Sandbox.Teardown
// via the cleanup func that RegisterBuiltinPollers returns. Best-effort
// — Unsubscribe errors are logged but don't abort cleanup (a torn-down
// connection makes the call moot).
func (p *NATSSubscribePoller) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for subject, entry := range p.cache {
		if err := entry.reader.Close(); err != nil {
			slog.Warn("nats_subscribe: close subscription",
				"subject", subject, "err", err)
		}
	}
	p.cache = map[string]*natsSubEntry{}
}

// cloneReceived deep-copies the accumulator slice so MatchShape's
// downstream operations (JSON marshal, recursive matching) don't
// observe in-place mutations from concurrent handler appends. The
// accumulator itself is owned by the cache; the snapshot is what
// flows through the event channel.
func cloneReceived(in []readers.NATSReceivedMessage) []readers.NATSReceivedMessage {
	out := make([]readers.NATSReceivedMessage, len(in))
	copy(out, in)
	return out
}

// Compile-time interface checks.
var (
	_ Poller = (*NATSSubscribePoller)(nil)
	_ Warmer = (*NATSSubscribePoller)(nil)
)
