// Package pollers bridges the live data sources (Mongo, Cassandra,
// JetStream, container logs) into Gomega's polling-function model.
//
// Phase 4.0 architectural pivot: pollers are **universal primitives**
// — each one accepts per-assertion arguments from the YAML scenario
// rather than being pre-bound to a specific table / stream / collection.
// This keeps the runner 100% application-agnostic; all
// domain-specific knowledge (table names, subject filters, collection
// names) lives in the scenario YAML, not the Go code.
//
// Built-in primitives (registered by RegisterBuiltinPollers):
//
//   - mongo_find         args: collection, filter
//   - cassandra_select   args: query, params? (binds StartTime to the
//     first `?` placeholder when params unset)
//   - jetstream_consume  args: stream, filter_subject
//   - logs_tail          args: container, service?
//   - reply              no args; dispatcher pushes synchronously
//   - nats_subscribe     args: subject (Phase 4.5; implements Warmer
//     so the Core NATS subscription opens BEFORE the verb fires)
//
// Stateful primitives (jetstream_consume, logs_tail) maintain an
// internal per-args cache so a scenario that asserts twice against the
// same (stream, filter_subject) tuple shares one underlying consumer.
// Stateless primitives (mongo_find, cassandra_select) run the query
// fresh each poll.
//
// See ARCHITECTURE.md §3 for the streaming-assertion model.
package pollers

import (
	"fmt"
	"sort"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// Poller exposes a Gomega-compatible polling function for one
// primitive location.
//
// args is the per-assertion parameter map (see package doc for the
// per-primitive shape). Stateless primitives parse it on every
// PollFn call; stateful primitives cache the underlying machinery
// keyed by args identity.
//
// traceparent is the per-case W3C traceparent; pollers MAY use it to
// filter (only events tagged with this trace) or MAY ignore it (Mongo
// + Cassandra rows don't carry trace context). The contract is
// "best-effort filter to events relevant to this case".
type Poller interface {
	PollFn(args map[string]any, traceparent string) func() []readers.Event
}

// Warmer is an optional interface implemented by pollers that need
// to initialize state BEFORE the case-runner's dispatcher.Fire call.
// Phase 4.5's nats_subscribe primitive needs it: Core NATS has no
// replay, so subscriptions must be live at publish time.
//
// case_runner.go Step 3b type-asserts each expected block's poller
// against Warmer and calls Warm with the substituted args, BEFORE
// firing the verb. Pollers that don't need pre-fire initialization
// (mongo_find, cassandra_select, jetstream_consume, logs_tail,
// reply) simply don't implement Warmer; the type assertion misses
// and they're skipped.
//
// Warm errors hard-fail the case (spec §8 Q2): a Warm failure
// indicates operator misconfiguration (bad subject, missing creds)
// — degrading would silently turn the case into a guaranteed
// assertion timeout with a misleading "no events" reason. Hard-fail
// surfaces the actual error in the run report.
type Warmer interface {
	Warm(args map[string]any) error
}

// Registry maps reader-location name → Poller. Built once at runner
// startup; consulted by RunCase per assertion.
type Registry struct {
	pollers map[string]Poller
}

// NewRegistry returns an empty Registry. RegisterBuiltinPollers
// (Task 14) wires the production set.
func NewRegistry() *Registry {
	return &Registry{pollers: map[string]Poller{}}
}

// Register adds or replaces the poller for the named location.
func (r *Registry) Register(location string, p Poller) {
	r.pollers[location] = p
}

// Get returns the registered poller for the named location. Unknown
// location → error naming both the unknown location and the registered
// set (sorted) so the operator sees both.
func (r *Registry) Get(location string) (Poller, error) {
	if p, ok := r.pollers[location]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("pollers: unknown location %q; available: %v", location, r.Locations())
}

// Locations returns the registered location names in sorted order.
func (r *Registry) Locations() []string {
	out := make([]string, 0, len(r.pollers))
	for k := range r.pollers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
