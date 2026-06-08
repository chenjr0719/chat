package runtime

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
)

// stubMishapExecutor records that Apply saw the trigger fire and that
// Cleanup ran. Used to verify Task 18's executor lifecycle (Apply
// before assertions complete, Cleanup deferred at RunCase return).
type stubMishapExecutor struct {
	mu          sync.Mutex
	appliedSeen bool
	cleanedSeen bool
	applyDone   chan struct{} // closed when Apply returns
}

func (e *stubMishapExecutor) Apply(_ context.Context, trigger <-chan struct{}) error {
	<-trigger
	e.mu.Lock()
	e.appliedSeen = true
	e.mu.Unlock()
	close(e.applyDone)
	return nil
}

func (e *stubMishapExecutor) Cleanup(_ context.Context) error {
	e.mu.Lock()
	e.cleanedSeen = true
	e.mu.Unlock()
	return nil
}

func (e *stubMishapExecutor) Applied() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.appliedSeen
}

func (e *stubMishapExecutor) Cleaned() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.cleanedSeen
}

// buildTestSandboxWithMishap wires the minimum sandbox + mishap
// registry the Task 18 lifecycle test needs. Returns the stub executor
// so the test can assert Apply/Cleanup ran.
func buildTestSandboxWithMishap(t *testing.T, kind, factoryName string) (*Sandbox, *stubMishapExecutor) {
	t.Helper()

	sb, _ := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted"}},
	})

	exec := &stubMishapExecutor{applyDone: make(chan struct{})}

	reg := mishap.NewRegistry()
	reg.RegisterFactory(factoryName, func(_ mishap.FactoryContext, _ string) (mishap.Executor, error) {
		return exec, nil
	})

	sb.Deps.Chaos = mishap.NewFakeChaosEngine()
	sb.Deps.MishapRegistry = reg
	sb.Deps.FactoryByKind = map[string]string{kind: factoryName}

	return sb, exec
}

func TestRunCase_MishapAppliedAndCleanedUp(t *testing.T) {
	sb, exec := buildTestSandboxWithMishap(t, "test-mishap", "TestMishapFactory")

	c := scenario.Case{
		Name:   "with-mishap",
		Mishap: "test-mishap",
		Expected: []scenario.Expected{
			{
				Location: "reply",
				Match:    map[string]any{"status": "accepted"},
				Timeout:  scenario.Duration(500 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}

	verdict, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "pass", verdict.Outcome, verdict.Reason)

	// Apply ran (trigger was fired) and Cleanup ran (deferred at
	// RunCase return). Eventually because Apply runs in a goroutine
	// per spec §6.4 step 3 — the runtime makes no ordering guarantee
	// between Apply completion and RunCase return; the test waits
	// briefly for the goroutine to observe the close.
	assert.Eventually(t, exec.Applied, 200*time.Millisecond, 5*time.Millisecond,
		"Apply must see the trigger fire")
	assert.True(t, exec.Cleaned(), "Cleanup must run before RunCase returns")
}

func TestRunCase_NoMishapWhenCaseMishapEmpty(t *testing.T) {
	// Sanity: a case without a mishap doesn't try to look up an empty
	// factory name (which would be a clear runtime error).
	sb, exec := buildTestSandboxWithMishap(t, "test-mishap", "TestMishapFactory")

	c := scenario.Case{
		Name: "no-mishap",
		// Mishap intentionally empty.
		Expected: []scenario.Expected{
			{
				Location: "reply",
				Match:    map[string]any{"status": "accepted"},
				Timeout:  scenario.Duration(200 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}

	verdict, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "pass", verdict.Outcome, verdict.Reason)

	// The mishap executor must not have been touched.
	assert.False(t, exec.Applied(), "no mishap kind → Apply must NOT run")
	assert.False(t, exec.Cleaned(), "no mishap kind → Cleanup must NOT run")
}

func TestRunCase_UnknownMishapKindReturnsError(t *testing.T) {
	// Catalog drift: c.Mishap references a kind not in FactoryByKind.
	// The runner surfaces it as a runtime error (not an assertion
	// failure) so the scenario authoring layer hears about it loudly.
	sb, _ := buildTestSandboxWithMishap(t, "test-mishap", "TestMishapFactory")

	c := scenario.Case{
		Name:   "bad-mishap",
		Mishap: "never-registered",
		Expected: []scenario.Expected{
			{Location: "reply", Match: map[string]any{"status": "accepted"},
				Timeout: scenario.Duration(50 * time.Millisecond),
				Polling: scenario.Duration(10 * time.Millisecond)},
		},
	}

	_, err := RunCase(context.Background(), sb, &c)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never-registered")
}
