package pollers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// fakeReader pushes a canned slice of events into its returned channel,
// then closes. Watch's ctx cancellation also closes the channel.
type fakeReader struct {
	events []readers.Event
	closed chan struct{} // closed when Watch's goroutine exits
}

func newFakeReader(evs ...readers.Event) *fakeReader {
	return &fakeReader{events: evs, closed: make(chan struct{})}
}

func (r *fakeReader) Watch(ctx context.Context, _ string, _ time.Time) (<-chan readers.Event, error) {
	ch := make(chan readers.Event, len(r.events))
	go func() {
		defer close(ch)
		defer close(r.closed)
		for _, ev := range r.events {
			select {
			case <-ctx.Done():
				return
			case ch <- ev:
			}
		}
		// Block until ctx is cancelled so the test can verify cleanup.
		<-ctx.Done()
	}()
	return ch, nil
}

func TestStreamPoller_BuffersEventsFromReader(t *testing.T) {
	r := newFakeReader(
		readers.Event{Location: "reply", Payload: "first"},
		readers.Event{Location: "reply", Payload: "second"},
		readers.Event{Location: "reply", Payload: "third"},
	)

	p, err := NewStreamPoller(r, "tp-123", time.Now())
	require.NoError(t, err)
	defer p.Close()

	// Give the buffer goroutine a moment to drain the channel.
	require.Eventually(t, func() bool {
		return len(p.PollFn(nil, "tp-123")()) == 3
	}, time.Second, 10*time.Millisecond)

	events := p.PollFn(nil, "tp-123")()
	require.Len(t, events, 3)
	payloads := []any{events[0].Payload, events[1].Payload, events[2].Payload}
	assert.Equal(t, []any{"first", "second", "third"}, payloads)
}

func TestStreamPoller_PollFnReturnsCopySoCallerMutationDoesntRace(t *testing.T) {
	r := newFakeReader(readers.Event{Location: "reply", Payload: "x"})
	p, err := NewStreamPoller(r, "tp", time.Now())
	require.NoError(t, err)
	defer p.Close()

	require.Eventually(t, func() bool {
		return len(p.PollFn(nil, "tp")()) == 1
	}, time.Second, 10*time.Millisecond)

	a := p.PollFn(nil, "tp")()
	b := p.PollFn(nil, "tp")()
	require.Len(t, a, 1)
	require.Len(t, b, 1)
	// Verify they're distinct slice headers (different underlying array
	// not required, but the slice header must be a copy).
	a[0].Location = "MUTATED"
	assert.Equal(t, "reply", b[0].Location, "mutation of one PollFn result must not affect another")
}

func TestStreamPoller_CloseStopsWatcher(t *testing.T) {
	r := newFakeReader(readers.Event{Location: "reply"})
	p, err := NewStreamPoller(r, "tp", time.Now())
	require.NoError(t, err)

	p.Close()

	// fakeReader's goroutine closes `closed` when its ctx is cancelled.
	select {
	case <-r.closed:
		// good
	case <-time.After(time.Second):
		t.Fatal("StreamPoller.Close did not cancel the watcher within 1s")
	}
}

func TestStreamPoller_ConcurrentPollFnReadsAreSafe(t *testing.T) {
	r := newFakeReader(readers.Event{Location: "reply"})
	p, err := NewStreamPoller(r, "tp", time.Now())
	require.NoError(t, err)
	defer p.Close()

	require.Eventually(t, func() bool {
		return len(p.PollFn(nil, "tp")()) == 1
	}, time.Second, 10*time.Millisecond)

	// Read concurrently from 10 goroutines.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = p.PollFn(nil, "tp")()
		}()
	}
	wg.Wait()
}
