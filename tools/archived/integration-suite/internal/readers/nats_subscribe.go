package readers

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/nats-io/nats.go"
)

// NATSSubscribeReader owns one Core NATS subscription. Unlike the
// JetStream reader, Core NATS has no replay — so the subscription
// must be live at publish time. The poller's Warm hook calls Open()
// BEFORE the case-runner fires the verb (see case_runner.go Step 3b).
//
// Concurrency: nats.Subscribe delivers messages via the library's
// internal delivery goroutine. The handler appends to queue under
// the mutex; Drain reads + clears under the same mutex. No separate
// forwarder goroutine — the NATS library already owns delivery.
//
// Buffer policy: maxQueueDepth caps the in-memory queue. Excess
// arrivals are dropped with slog.Warn, mirroring the
// JetStreamSubjectReader policy at jetstream_subject.go:101-107.
// 256 is large enough for every drafted broadcast scenario (≤10
// arrivals each) and small enough to bound memory under pathological
// publish storms.
type NATSSubscribeReader struct {
	conn    *nats.Conn
	subject string

	mu     sync.Mutex
	sub    *nats.Subscription
	queue  []NATSReceivedMessage
	closed bool
}

// NATSSubscribePayload is the typed Event.Payload the poller hands
// to MatchShape. The Subject field carries the SUBSCRIBED pattern
// (possibly a wildcard); each Received[i].Subject carries the actual
// delivery subject so wildcard authors can disambiguate.
type NATSSubscribePayload struct {
	Subject  string                `json:"subject"`
	Received []NATSReceivedMessage `json:"received"`
}

// NATSReceivedMessage mirrors the shape of ReplyPayload's body fields
// so YAML authors don't have to learn a new schema. body_json /
// body_raw split matches the existing JetStream + reply payloads.
type NATSReceivedMessage struct {
	Subject  string              `json:"subject"`
	BodyJSON map[string]any      `json:"body_json,omitempty"`
	BodyRaw  string              `json:"body_raw,omitempty"`
	Header   map[string][]string `json:"header,omitempty"`
}

// maxQueueDepth bounds in-memory accumulation. Mirrors the spec §4.5
// buffer policy and the JetStreamSubjectReader cap.
const maxQueueDepth = 256

// MaxQueueDepthForTesting exposes the queue cap to the pollers test
// suite so the overflow-drop assertion doesn't have to hardcode the
// number. The value is an implementation detail — production code
// should not depend on it.
func MaxQueueDepthForTesting() int { return maxQueueDepth }

// NewNATSSubscribeReader builds a reader bound to subject on conn.
// Open must be called separately so the subscription's open error
// can flow through Warm's return path (a panic-free degradation).
func NewNATSSubscribeReader(conn *nats.Conn, subject string) *NATSSubscribeReader {
	return &NATSSubscribeReader{
		conn:    conn,
		subject: subject,
	}
}

// Open synchronously subscribes. NATS guarantees that once Subscribe
// returns, the subscription is live — so the publish that follows
// Warm's caller will be delivered. Returns the underlying nats error
// verbatim on failure so the case-runner can surface "invalid
// subject" / "connection closed" / etc.
func (r *NATSSubscribeReader) Open() error {
	sub, err := r.conn.Subscribe(r.subject, r.handler)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.sub = sub
	r.mu.Unlock()
	return nil
}

// handler is the NATS delivery callback. Appends to the queue under
// the mutex; drops with slog.Warn at the maxQueueDepth cap. Returns
// silently when the reader has been closed (delivery may race with
// Close; the closed flag prevents accumulating into a dead reader).
func (r *NATSSubscribeReader) handler(msg *nats.Msg) {
	received := buildReceived(msg)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	if len(r.queue) >= maxQueueDepth {
		slog.Warn("nats_subscribe: buffer full; dropping message",
			"subscribed_subject", r.subject,
			"actual_subject", msg.Subject,
			"cap", maxQueueDepth,
		)
		return
	}
	r.queue = append(r.queue, received)
}

// Drain appends every queued message to the caller-supplied
// accumulator and clears the internal queue. The pattern enables
// monotonic accumulation across PollFn calls: the poller hangs onto
// the returned slice between polls so the Eventually loop sees the
// observation window grow until the matcher is satisfied.
func (r *NATSSubscribeReader) Drain(into []NATSReceivedMessage) []NATSReceivedMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.queue) == 0 {
		return into
	}
	into = append(into, r.queue...) //nolint:gocritic // accumulator pattern — caller owns `into` and re-binds the returned slice
	r.queue = r.queue[:0]
	return into
}

// Close unsubscribes and marks the reader closed. Safe to call
// twice — second call short-circuits via the closed flag. The
// underlying connection is NOT closed (owned by Sandbox); only the
// subscription is.
func (r *NATSSubscribeReader) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	sub := r.sub
	r.mu.Unlock()
	if sub == nil {
		return nil
	}
	return sub.Unsubscribe()
}

// buildReceived constructs the per-message capture. JSON-decode
// attempt mirrors NewReplyPayload's logic (reply_payload.go:36-42)
// so the body_json vs body_raw split is consistent across all
// reply-style readers.
func buildReceived(msg *nats.Msg) NATSReceivedMessage {
	r := NATSReceivedMessage{Subject: msg.Subject}
	if len(msg.Data) > 0 {
		var m map[string]any
		if err := json.Unmarshal(msg.Data, &m); err == nil {
			r.BodyJSON = m
		} else {
			r.BodyRaw = string(msg.Data)
		}
	}
	if len(msg.Header) > 0 {
		cp := make(map[string][]string, len(msg.Header))
		for k, v := range msg.Header {
			out := make([]string, len(v))
			copy(out, v)
			cp[k] = out
		}
		r.Header = cp
	}
	return r
}
