package mishap

import (
	"context"
	"fmt"
	"sync"
)

// FakeDockerCLI is a test double for DockerCLI. Calls are recorded
// in order; injected errors fire when a recorded call matches ErrOn.
//
// NOT a production type. Lives in a non-_test.go file because other
// packages' tests need to import it.
type FakeDockerCLI struct {
	mu    sync.Mutex
	Calls []string
	// ErrOn maps a call signature (e.g., "Restart(room-worker)") to an
	// injected error. Configure before any Apply runs; not safe to mutate
	// concurrently with method calls.
	ErrOn map[string]error
}

// NewFakeDockerCLI returns an empty fake.
func NewFakeDockerCLI() *FakeDockerCLI {
	return &FakeDockerCLI{Calls: []string{}, ErrOn: map[string]error{}}
}

func (f *FakeDockerCLI) record(call string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Calls = append(f.Calls, call)
	return f.ErrOn[call]
}

func (f *FakeDockerCLI) Restart(_ context.Context, c string) error {
	return f.record(fmt.Sprintf("Restart(%s)", c))
}

func (f *FakeDockerCLI) StartIfStopped(_ context.Context, c string) error {
	return f.record(fmt.Sprintf("StartIfStopped(%s)", c))
}
