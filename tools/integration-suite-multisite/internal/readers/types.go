// Package readers holds reader implementations, one per catalog
// location. Each reader knows how to query its backend, filter by
// scenario trace + run prefix + timestamp window, and return observed
// changes during a scenario's timeframe.
package readers

import (
	"context"
	"time"
)

// EventType classifies what an emitted event represents — set by the
// reader catalog's matching rule (§4.6.0). Drives classifier behavior
// in verdict.go (Task 9). Until that wires in, every existing reader
// emits Type=EventCascade by default and the classifier ignores the
// field — Part-1 behavior preserved.
type EventType string

const (
	EventCascade         EventType = "cascade"
	EventFailure         EventType = "failure"
	EventRestartNoise    EventType = "restart_noise"
	EventDisconnectNoise EventType = "disconnect_noise"
	EventBackground      EventType = "background"
	EventUnmatched       EventType = "unmatched" // auto-assigned by reader when no pattern matches
)

// Event is one observed change at a reader location.
type Event struct {
	Location    string    // matches the catalog location name
	Timestamp   time.Time // when the event happened, per the location's timestamp_source
	Traceparent string    // empty if the change carries no trace
	OwnerSvc    string    // which service is attributed as the writer (from catalog ownership)
	Payload     any       // typed shape per the location (mongo doc, NATS msg, log line, ...)
	Type        EventType // classification per catalog rule (§4.6.0); default EventCascade
}

// Reader is the interface every reader implementation satisfies.
type Reader interface {
	// Watch starts a goroutine that emits events into the returned channel
	// until ctx is cancelled or T_close fires. `traceparent` is the
	// scenario's W3C traceparent string for this watch (accepted by all
	// readers so the signature is uniform; readers whose source carries
	// trace natively MAY use it for additional filtering, but it is NOT
	// the synthetic stamping mechanism). `start` is T_open (events before
	// start are filtered).
	//
	// Trace stamping contract: readers populate Event.Traceparent IFF they
	// can extract it from their underlying source (NATS reply header, slog
	// JSON field, JetStream message header). Storage readers (Mongo,
	// Cassandra) leave Traceparent empty. The Observer (see
	// internal/runtime/observer.go) applies SCOPE-AWARE synthetic stamping
	// at merge time: for any event with empty Traceparent whose OwnerSvc
	// is in the scenario's sequence, it stamps the scenario's traceparent.
	// Out-of-scope downstream events stay trace-less and fall to
	// profile-based background filtering at verdict time.
	//
	// Cleanup contract: Watch goroutines MUST exit when ctx is cancelled.
	// Implementations using OS resources (subprocesses, network
	// connections, JetStream ephemeral consumers) rely on ctx-cancellation
	// to release them. The returned channel MUST be closed when Watch
	// exits so the observer can detect completion.
	Watch(ctx context.Context, traceparent string, start time.Time) (<-chan Event, error)
}
