package pollers

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// StreamPoller wraps any readers.Reader by spawning one goroutine that
// drains the reader's Watch channel into a thread-safe in-memory
// buffer. PollFn returns a snapshot copy of the buffer.
//
// Used for sources that don't lend themselves to direct polling:
//
//   - reply (one-shot reply per fire, injected by the dispatcher)
//   - logs.* (docker logs -f streams via the existing ContainerLogsReader)
//   - jetstream.* (JetStream subject subscriptions)
//
// MongoPoller is the alternative — used for sources where querying
// the underlying store each poll is cheaper than buffering.
//
// Close MUST be called when the case ends to cancel the watcher
// goroutine.
type StreamPoller struct {
	mu     sync.Mutex
	events []readers.Event

	cancel context.CancelFunc
	done   chan struct{} // closed when the drain goroutine exits
}

// NewStreamPoller spawns the watcher and returns a poller ready to
// serve PollFn. `traceparent` is forwarded to Reader.Watch (some
// readers, e.g. NATSReply, use it as a filter hint); `startTime` is
// forwarded as T_open.
func NewStreamPoller(r readers.Reader, traceparent string, startTime time.Time) (*StreamPoller, error) {
	if r == nil {
		return nil, fmt.Errorf("StreamPoller: nil reader")
	}

	ctx, cancel := context.WithCancel(context.Background())
	ch, err := r.Watch(ctx, traceparent, startTime)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("StreamPoller: watch: %w", err)
	}

	p := &StreamPoller{
		cancel: cancel,
		done:   make(chan struct{}),
	}

	go func() {
		defer close(p.done)
		for ev := range ch {
			p.mu.Lock()
			p.events = append(p.events, ev)
			p.mu.Unlock()
		}
	}()

	return p, nil
}

// PollFn returns a function that yields a copy of the buffered
// events. args + traceparent are accepted to satisfy the Poller
// interface but are ignored — the StreamPoller is a fixed-binding
// helper; per-args dispatch is handled by the universal primitives
// that wrap it (JetStreamConsumePoller, LogsTailPoller, ReplyPoller).
func (p *StreamPoller) PollFn(_ map[string]any, _ string) func() []readers.Event {
	return func() []readers.Event {
		p.mu.Lock()
		defer p.mu.Unlock()
		out := make([]readers.Event, len(p.events))
		copy(out, p.events)
		return out
	}
}

// Close cancels the watcher goroutine. Safe to call multiple times;
// the underlying context.CancelFunc is idempotent.
func (p *StreamPoller) Close() {
	p.cancel()
}

// Done returns a channel that closes when the watcher goroutine has
// exited. Useful in tests; production code can just call Close and
// move on.
func (p *StreamPoller) Done() <-chan struct{} {
	return p.done
}

// Compile-time interface check.
var _ Poller = (*StreamPoller)(nil)
