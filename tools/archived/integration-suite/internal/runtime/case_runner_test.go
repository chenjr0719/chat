package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/matchers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/runtime/pollers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/verbs"
)

// stubExecutor captures the Input it received so the test can assert
// the dispatcher routed the merged subject/payload/cred correctly.
type stubExecutor struct {
	fired bool
	in    *verbs.Input
	out   verbs.Outcome
}

func (s *stubExecutor) Execute(_ context.Context, in *verbs.Input) verbs.Outcome {
	s.fired = true
	s.in = in
	return s.out
}

// stubPoller returns the same fixed event slice on every poll. Used to
// drive Gomega's Eventually loop deterministically.
type stubPoller struct{ events []readers.Event }

func (p *stubPoller) PollFn(_ map[string]any, _ string) func() []readers.Event {
	return func() []readers.Event { return p.events }
}

// buildTestSandbox wires the minimum sandbox an end-to-end case run
// needs: one verified user "alice", a Dispatcher routed to a stub
// executor under verb name "test_verb", and a PollerReg holding a
// stub poller at "reply".
func buildTestSandbox(t *testing.T, replyEvents []readers.Event) (*Sandbox, *stubExecutor) {
	t.Helper()

	verbReg := verbs.NewRegistry()
	exec := &stubExecutor{}
	verbReg.Register("test_verb", exec)

	pollerReg := pollers.NewRegistry()
	pollerReg.Register("reply", &stubPoller{events: replyEvents})

	sb := &Sandbox{
		Scenario: &scenario.Scenario{
			Name: "test-scenario",
			BaseInput: scenario.BaseInput{
				Verb:       "test_verb",
				Subject:    "chat.user.${alice.account}.request.room.create",
				Payload:    map[string]any{"name": "Engineering"},
				Credential: "${alice.credential}",
			},
		},
		Deps: SandboxDeps{
			Dispatcher: &Dispatcher{VerbReg: verbReg},
			MatcherReg: matchers.NewRegistry(),
		},
		Users: map[string]*seedeffect.SeedUser{
			"alice": {Alias: "alice", Account: "alice", ID: "u-alice", JWT: "jwt-alice", NkeySeed: "seed-alice"},
		},
		Placeholders: map[string]map[string]any{
			"alice": {"account": "alice", "id": "u-alice", "jwt": "jwt-alice", "nkey": "seed-alice"},
		},
		PollerReg: pollerReg,
	}
	return sb, exec
}

func TestRunCase_HappyPath(t *testing.T) {
	// Reply event matches the expected shape — Gomega.Eventually
	// succeeds on the first poll.
	sb, exec := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
	})
	c := scenario.Case{
		Name: "happy",
		Tag:  "positive",
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
	assert.True(t, exec.fired, "dispatcher must fire the verb before polling")
	require.NotNil(t, exec.in)
	assert.Equal(t, "chat.user.alice.request.room.create", exec.in.Subject,
		"subject substitution must resolve ${alice.account}")
	assert.Equal(t, "alice", exec.in.Credential.Account, "credential must resolve to alice from sb.Users")
}

func TestRunCase_AssertionFailsWhenNoMatchingEvent(t *testing.T) {
	// Reply event does NOT match — Gomega.Eventually times out and
	// records a failure. The verdict surfaces it.
	sb, _ := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
	})
	c := scenario.Case{
		Name: "fail",
		Tag:  "positive",
		Expected: []scenario.Expected{
			{
				Location: "reply",
				Match:    map[string]any{"status": "accepted"},
				Timeout:  scenario.Duration(100 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}

	verdict, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "fail", verdict.Outcome)
	assert.NotEmpty(t, verdict.Reason)
	assert.NotEmpty(t, verdict.Failures)
}

func TestRunCase_FirstFailureShortCircuitsRemainingExpected(t *testing.T) {
	// Two expected[] blocks; the first fails. The second must NOT be
	// evaluated (spec §6.4 step 6: "break on first failure").
	sb, _ := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
	})
	c := scenario.Case{
		Name: "two-asserts",
		Expected: []scenario.Expected{
			{
				Location: "reply",
				Match:    map[string]any{"status": "accepted"},
				Timeout:  scenario.Duration(50 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
			{
				Location: "nonexistent.location", // would fail registry.Get
				Match:    map[string]any{"x": 1},
				Timeout:  scenario.Duration(50 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}

	verdict, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "fail", verdict.Outcome)
	assert.Len(t, verdict.Failures, 1, "second expected must NOT run after first fails")
}

func TestRunCase_NotAssertionPassesWhenEventNeverArrives(t *testing.T) {
	// expected[].not=true is satisfied when the matching event never
	// shows up across the timeout window (Consistently().ShouldNot).
	sb, _ := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
	})
	c := scenario.Case{
		Name: "negative-must-not-happen",
		Tag:  "negative",
		Expected: []scenario.Expected{
			{
				Location: "reply",
				Match:    map[string]any{"status": "accepted"},
				Not:      true,
				Timeout:  scenario.Duration(50 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}

	verdict, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "pass", verdict.Outcome, verdict.Reason)
}

func TestRunCase_NotAssertionFailsWhenEventDoesArrive(t *testing.T) {
	// expected[].not=true fails if the event DOES match — surfaces
	// "must not happen" violations.
	sb, _ := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
	})
	c := scenario.Case{
		Name: "negative-fires",
		Expected: []scenario.Expected{
			{
				Location: "reply",
				Match:    map[string]any{"status": "accepted"},
				Not:      true,
				Timeout:  scenario.Duration(50 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}

	verdict, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "fail", verdict.Outcome)
	assert.NotEmpty(t, verdict.Reason)
}

func TestRunCase_CaseInputOverridesBaseInput(t *testing.T) {
	// c.Input.Subject overrides the base subject. The executor sees
	// the merged subject post-substitution.
	sb, exec := buildTestSandbox(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted"}},
	})
	overrideSubject := "chat.user.${alice.account}.request.room.delete"
	c := scenario.Case{
		Name:  "override",
		Input: &scenario.CaseInputOverride{Subject: &overrideSubject},
		Expected: []scenario.Expected{
			{Location: "reply", Match: map[string]any{"status": "accepted"},
				Timeout: scenario.Duration(500 * time.Millisecond),
				Polling: scenario.Duration(10 * time.Millisecond)},
		},
	}

	_, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	require.NotNil(t, exec.in)
	assert.Equal(t, "chat.user.alice.request.room.delete", exec.in.Subject)
}

func TestRunCase_UnknownLocationReturnsFail(t *testing.T) {
	// expected[].location not in PollerReg → fail with a clear
	// "unknown poller location" reason. Doesn't bubble out as an
	// error — Gomega-style assertion failure.
	sb, _ := buildTestSandbox(t, nil)
	c := scenario.Case{
		Name: "bad-loc",
		Expected: []scenario.Expected{
			{
				Location: "no.such.location",
				Match:    map[string]any{"x": 1},
				Timeout:  scenario.Duration(50 * time.Millisecond),
				Polling:  scenario.Duration(10 * time.Millisecond),
			},
		},
	}
	v, err := RunCase(context.Background(), sb, &c)
	require.NoError(t, err)
	assert.Equal(t, "fail", v.Outcome)
	assert.Contains(t, v.Reason, "no.such.location")
}
