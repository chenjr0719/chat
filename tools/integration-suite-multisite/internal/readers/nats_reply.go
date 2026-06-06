package readers

import (
	"context"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// NATSReplyReader is a "pull" reader: the verb dispatcher feeds the
// captured reply (or transport error) into the reader via the Inject
// method, which then emits one event. There's no polling because the
// reply is point-in-time.
type NATSReplyReader struct {
	in chan Event
}

// NewNATSReplyReader returns a NATSReplyReader ready to receive injected replies.
func NewNATSReplyReader() *NATSReplyReader {
	return &NATSReplyReader{in: make(chan Event, 4)}
}

// Watch relays injected reply events until ctx is cancelled.
func (r *NATSReplyReader) Watch(ctx context.Context, _ string, _ time.Time) (<-chan Event, error) {
	out := make(chan Event, 4)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-r.in:
				out <- ev
			}
		}
	}()
	return out, nil
}

// Inject is called by the dispatcher when a reply (or transport error)
// arrives. The dispatcher has the traceparent context and measured
// latency; this method records them along with the full reply outcome
// as a structured ReplyPayload. The matcher does the interpretation —
// see internal/matchers/matches_shape.go's struct fall-through path.
func (r *NATSReplyReader) Inject(out *verbs.Outcome, latency time.Duration, traceparent string, ts time.Time, ownerSvc string) {
	select {
	case r.in <- Event{
		Location:    "reply",
		Timestamp:   ts,
		Traceparent: traceparent,
		OwnerSvc:    ownerSvc,
		Payload:     NewReplyPayload(out, latency),
		Type:        EventCascade,
	}:
	default:
		// drop if buffer full — should not happen with 4-deep buffer and 1 reply per scenario
	}
}
