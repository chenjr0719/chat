package readers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamSubjectReader observes a JetStream subject pattern via an
// ephemeral, deliver-new, filtered consumer scoped to the scenario's
// timeframe. Captures every message published during the timeframe,
// decodes the payload (best-effort JSON), and emits one Event per
// message. The ephemeral consumer is deleted at ctx-cancel.
//
// This reader does NOT classify or interpret — matchers do that.
//
// The reader is created per-stream + subject-filter (e.g. one instance
// for the rooms-canonical stream with filter
// `chat.room.canonical.*.>`). Wiring details live with the runner.
type JetStreamSubjectReader struct {
	conn          *nats.Conn
	streamName    string
	subjectFilter string
	location      string // catalog location name, set on emitted events
	ownerSvc      string // owner attribution for the classifier
}

// NewJetStreamSubjectReader returns a reader bound to the given
// JetStream stream + subject filter. The conn must already be
// connected; the reader does not own its lifecycle.
func NewJetStreamSubjectReader(conn *nats.Conn, streamName, subjectFilter, location, ownerSvc string) *JetStreamSubjectReader {
	return &JetStreamSubjectReader{
		conn:          conn,
		streamName:    streamName,
		subjectFilter: subjectFilter,
		location:      location,
		ownerSvc:      ownerSvc,
	}
}

// JetStreamSubjectPayload is the typed Event.Payload emitted by
// JetStreamSubjectReader. Mirrors the capture-and-emit discipline of
// ReplyPayload — the reader records every observable fact and lets
// matchers interpret.
type JetStreamSubjectPayload struct {
	Subject  string              `json:"subject"`
	BodyJSON map[string]any      `json:"body_json,omitempty"`
	BodyRaw  string              `json:"body_raw,omitempty"`
	Header   map[string][]string `json:"header,omitempty"`
	Sequence uint64              `json:"sequence"`
}

// Watch starts an ephemeral JetStream consumer at deliver-by-start-time
// scoped to ctx + start. start binds the consumer's OptStartTime so a
// lazily-opened consumer (Phase 4.0 jetstream_consume primitive opens
// on first PollFn call, *after* the verb has fired) replays every
// message published since the sandbox session boundary. Without this,
// the timeline race between "case fires" and "consumer becomes ready"
// would silently drop the target event under DeliverNewPolicy.
//
// Cleanup deletes the consumer on ctx-done; InactiveThreshold is a
// safety net so an orphaned consumer dies on its own.
func (r *JetStreamSubjectReader) Watch(ctx context.Context, _ string, start time.Time) (<-chan Event, error) {
	js, err := jetstream.New(r.conn)
	if err != nil {
		return nil, fmt.Errorf("jetstream.rooms-canonical: connect: %w", err)
	}

	stream, err := js.Stream(ctx, r.streamName)
	if err != nil {
		return nil, fmt.Errorf("jetstream.rooms-canonical: stream %q: %w", r.streamName, err)
	}

	cons, err := stream.CreateOrUpdateConsumer(ctx, jetstream.ConsumerConfig{
		FilterSubject:     r.subjectFilter,
		DeliverPolicy:     jetstream.DeliverByStartTimePolicy,
		OptStartTime:      &start,
		AckPolicy:         jetstream.AckNonePolicy,
		InactiveThreshold: 30 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("jetstream.rooms-canonical: create consumer: %w", err)
	}

	out := make(chan Event, 16)
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		ev := Event{
			Location:  r.location,
			Timestamp: msgTime(msg),
			OwnerSvc:  r.ownerSvc,
			Payload:   buildJetStreamPayload(msg),
			Type:      EventCascade,
		}
		if hdr := msg.Headers(); hdr != nil {
			ev.Traceparent = hdr.Get("traceparent")
		}
		select {
		case out <- ev:
		default:
			// observer buffer full — drop is preferable to blocking the
			// JetStream consumer goroutine (matches NATSReplyReader
			// dropping policy)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("jetstream.rooms-canonical: consume: %w", err)
	}

	go func() {
		<-ctx.Done()
		cc.Stop()
		// Best-effort cleanup; consumer also self-expires via InactiveThreshold.
		_ = stream.DeleteConsumer(context.Background(), cons.CachedInfo().Name)
		close(out)
	}()

	return out, nil
}

func msgTime(msg jetstream.Msg) time.Time {
	meta, err := msg.Metadata()
	if err == nil && !meta.Timestamp.IsZero() {
		return meta.Timestamp
	}
	return time.Now()
}

func buildJetStreamPayload(msg jetstream.Msg) JetStreamSubjectPayload {
	p := JetStreamSubjectPayload{Subject: msg.Subject()}
	if meta, err := msg.Metadata(); err == nil {
		p.Sequence = meta.Sequence.Stream
	}
	data := msg.Data()
	if len(data) > 0 {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err == nil {
			p.BodyJSON = m
		} else {
			p.BodyRaw = string(data)
		}
	}
	if hdr := msg.Headers(); len(hdr) > 0 {
		cp := make(map[string][]string, len(hdr))
		for k, v := range hdr {
			out := make([]string, len(v))
			copy(out, v)
			cp[k] = out
		}
		p.Header = cp
	}
	return p
}
