package mishap

import (
	"context"
	"fmt"
	"sync"
)

// FakeChaosEngine is a test double for ChaosEngine. Calls are recorded
// in order; injected errors fire when a recorded call signature
// matches a key in ErrOn.
//
// NOT a production type. Lives in a non-_test.go file so other
// packages' tests (e.g. internal/runtime) can import it.
type FakeChaosEngine struct {
	mu    sync.Mutex
	Calls []string
	// ErrOn maps a call signature (e.g., "Partition(MongoProxy)") to
	// an injected error. Configure before any Apply runs; not safe to
	// mutate concurrently with method calls.
	ErrOn map[string]error
}

// NewFakeChaosEngine returns an empty fake.
func NewFakeChaosEngine() *FakeChaosEngine {
	return &FakeChaosEngine{Calls: []string{}, ErrOn: map[string]error{}}
}

func (f *FakeChaosEngine) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, call)
	return f.ErrOn[call]
}

func (f *FakeChaosEngine) Partition(_ context.Context, name string) error {
	return f.record(fmt.Sprintf("Partition(%s)", name))
}

func (f *FakeChaosEngine) Heal(_ context.Context, name string) error {
	return f.record(fmt.Sprintf("Heal(%s)", name))
}

func (f *FakeChaosEngine) Reset(_ context.Context) error {
	return f.record("Reset()")
}
