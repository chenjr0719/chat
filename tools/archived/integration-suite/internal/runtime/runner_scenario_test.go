package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/matchers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/verbs"
)

// buildRunnerDeps wires the unit-test minimal deps a runScenario call
// needs: a verb registered as "test_verb" returning the stub outcome
// the caller pre-set, and reader handles all nil (so the poller
// registry comes up empty — fine for tests that don't assert on
// non-reply locations).
func buildRunnerDeps(t *testing.T, replyEvents []readers.Event) (*runnerDeps, *PerformanceStore, *RunReport, *stubExecutor) {
	t.Helper()
	verbReg := verbs.NewRegistry()
	exec := &stubExecutor{}
	verbReg.Register("test_verb", exec)

	// We bypass the real Sandbox's PollerReg by injecting a stub
	// poller after Setup. To do that simply, this test path uses
	// nil readers and overrides PollerReg via the deps' OverrideSandbox
	// hook. Cleaner: just have RunCase see "reply" through the
	// sandbox-built registry — but builtins for "reply" require a
	// non-nil ReplyReader. We pass one so the registry has "reply".
	replyReader := readers.NewNATSReplyReader()
	// Pre-inject the desired reply events so the poller's buffer is
	// populated even though no Inject() call fires (the stub verb
	// doesn't do real NATS).
	// NB: we cannot inject directly into the poller buffer here —
	// the StreamPoller drains the reader's Watch channel. For unit
	// tests we'll override PollerReg right after Setup completes via
	// the deps hook below.

	perf := NewPerformanceStore()
	report := &RunReport{StartISO: "2026-05-31T00:00:00Z"}

	deps := &runnerDeps{
		Cfg: &Config{
			SiteID:  "test-site",
			AuthURL: "http://test-auth",
		},
		Dispatcher:    &Dispatcher{VerbReg: verbReg},
		MatcherReg:    matchers.NewRegistry(),
		SeedEffectReg: seedeffect.NewRegistry(),
		ChaosEngine:   mishap.NewFakeChaosEngine(),
		ReplyReader:   replyReader,
		Perf:          perf,
		Report:        report,
		// Test hook: after Setup builds PollerReg, swap in a stub
		// "reply" poller so the assertion can see our canned events.
		afterSetup: func(sb *Sandbox) {
			sb.PollerReg.Register("reply", &stubPoller{events: replyEvents})
		},
	}
	return deps, perf, report, exec
}

func TestRunScenario_RunsAllCasesInOrder(t *testing.T) {
	// Two cases — both should run, both should land in report.Cases
	// in declaration order.
	deps, perf, report, exec := buildRunnerDeps(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted"}},
	})
	s := &scenario.Scenario{
		Name: "two-case-scenario",
		BaseInput: scenario.BaseInput{
			Verb:    "test_verb",
			Subject: "test.subject",
			Payload: map[string]any{"k": "v"},
		},
		Cases: []scenario.Case{
			{
				Name: "case-one",
				Tag:  "positive",
				Expected: []scenario.Expected{
					{Location: "reply", Match: map[string]any{"status": "accepted"},
						Timeout: scenario.Duration(500 * time.Millisecond),
						Polling: scenario.Duration(10 * time.Millisecond)},
				},
			},
			{
				Name: "case-two",
				Tag:  "positive",
				Expected: []scenario.Expected{
					{Location: "reply", Match: map[string]any{"status": "accepted"},
						Timeout: scenario.Duration(500 * time.Millisecond),
						Polling: scenario.Duration(10 * time.Millisecond)},
				},
			},
		},
	}

	require.NoError(t, runScenario(context.Background(), s, deps))
	assert.True(t, exec.fired)

	// Both cases recorded in declaration order.
	require.Len(t, report.Cases, 2)
	assert.Equal(t, "two-case-scenario", report.Cases[0].ScenarioName)
	assert.Equal(t, "case", report.Cases[0].Subset)
	assert.Equal(t, "positive", report.Cases[0].Kind)
	assert.Equal(t, "pass", report.Cases[0].Verdict.Outcome)
	assert.Equal(t, "pass", report.Cases[1].Verdict.Outcome)

	// PerformanceStore keyed by <scenario>/<case-name> per spec §6.7.
	require.Contains(t, perf.Cases, "two-case-scenario/case-one")
	require.Contains(t, perf.Cases, "two-case-scenario/case-two")
	assert.Equal(t, "pass", perf.Cases["two-case-scenario/case-one"].Latest.Verdict)
}

func TestRunScenario_RecordsFailWhenAssertionFails(t *testing.T) {
	// Reply event doesn't match — the case lands as fail with the
	// reason captured.
	deps, _, report, _ := buildRunnerDeps(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
	})
	s := &scenario.Scenario{
		Name: "fail-scenario",
		BaseInput: scenario.BaseInput{
			Verb:    "test_verb",
			Subject: "test.subject",
			Payload: map[string]any{},
		},
		Cases: []scenario.Case{
			{
				Name: "fails",
				Tag:  "positive",
				Expected: []scenario.Expected{
					{Location: "reply", Match: map[string]any{"status": "accepted"},
						Timeout: scenario.Duration(50 * time.Millisecond),
						Polling: scenario.Duration(10 * time.Millisecond)},
				},
			},
		},
	}

	require.NoError(t, runScenario(context.Background(), s, deps))
	require.Len(t, report.Cases, 1)
	assert.Equal(t, "fail", report.Cases[0].Verdict.Outcome)
	assert.NotEmpty(t, report.Cases[0].Verdict.Reason)
}

func TestRunScenario_BetweenCaseChaosResetCalled(t *testing.T) {
	// runScenario must call ChaosEngine.Reset between cases (mirrors
	// runner_phases.go) — verified via FakeChaosEngine.Calls.
	deps, _, _, _ := buildRunnerDeps(t, []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted"}},
	})
	chaos := deps.ChaosEngine.(*mishap.FakeChaosEngine)

	s := &scenario.Scenario{
		Name: "chaos-reset-scenario",
		BaseInput: scenario.BaseInput{
			Verb:    "test_verb",
			Subject: "test.subject",
			Payload: map[string]any{},
		},
		Cases: []scenario.Case{
			{
				Name: "c1",
				Expected: []scenario.Expected{
					{Location: "reply", Match: map[string]any{"status": "accepted"},
						Timeout: scenario.Duration(500 * time.Millisecond),
						Polling: scenario.Duration(10 * time.Millisecond)},
				},
			},
			{
				Name: "c2",
				Expected: []scenario.Expected{
					{Location: "reply", Match: map[string]any{"status": "accepted"},
						Timeout: scenario.Duration(500 * time.Millisecond),
						Polling: scenario.Duration(10 * time.Millisecond)},
				},
			},
		},
	}

	require.NoError(t, runScenario(context.Background(), s, deps))

	resets := 0
	for _, c := range chaos.Calls {
		if c == "Reset()" {
			resets++
		}
	}
	// One from Setup, one from between-case reset, one from Teardown.
	// At minimum: 1 (Setup) + 1 (between-case) + 1 (Teardown) = 3.
	assert.GreaterOrEqual(t, resets, 3, "Reset must run at Setup, between cases, and at Teardown")
}
