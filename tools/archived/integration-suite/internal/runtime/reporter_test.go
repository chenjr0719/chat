package runtime

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// --- RenderLastRunMD: the new per-case rendering ------------------------

func TestRenderLastRunMD_EmptyStoreEmitsHeaderOnly(t *testing.T) {
	s := NewPerformanceStore()
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "| case |")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	assert.Len(t, lines, 2, "header + separator only")
}

func TestRenderLastRunMD_OnePassingCase(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("scn[c=0 p=- x=-]", &CaseLatest{
		Verdict: "pass", RanAt: "t1", ReadsMatched: "2/2", Cascades: 1, DurationMs: 240,
	})
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "scn[c=0 p=- x=-]")
	assert.Contains(t, out, "pass")
	assert.Contains(t, out, "2/2")
	assert.Contains(t, out, "240ms")
}

func TestRenderLastRunMD_OneFailingCase(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("scn[c=0 p=- x=room-worker]", &CaseLatest{
		Verdict: "fail", RanAt: "t1", ReadsMatched: "1/2", Cascades: 1, DurationMs: 12100,
	})
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "fail")
	assert.Contains(t, out, "1/2")
	assert.Contains(t, out, "12.1s")
}

func TestRenderLastRunMD_OneSkippedCaseShowsReason(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordSkipped("scn[c=0 p=mongo x=-]", "x=room-worker fails alone", "t1")
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "skipped (x=room-worker fails alone)")
	// reads/cascades/duration should be em-dash for skipped
	assert.Contains(t, out, "—")
}

func TestRenderLastRunMD_NoiseAnnotation(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("scn[c=0 p=- x=room-worker]", &CaseLatest{
		Verdict: "pass", RanAt: "t1", ReadsMatched: "2/2", Cascades: 1, DurationMs: 3400,
		NoiseMatches: map[string]int{"restart_noise": 2},
	})
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "pass (rn=2)")
}

func TestRenderLastRunMD_DisconnectNoiseAnnotation(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("scn[c=0 p=mongo x=-]", &CaseLatest{
		Verdict: "pass", RanAt: "t1", ReadsMatched: "2/2", Cascades: 1, DurationMs: 1100,
		NoiseMatches: map[string]int{"disconnect_noise": 3},
	})
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "pass (dn=3)")
}

func TestRenderLastRunMD_BothNoiseAnnotations(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("scn[c=0 p=mongo x=room-service]", &CaseLatest{
		Verdict: "pass", RanAt: "t1", ReadsMatched: "2/2", Cascades: 1, DurationMs: 4200,
		NoiseMatches: map[string]int{"restart_noise": 2, "disconnect_noise": 3},
	})
	out := RenderLastRunMD(s)
	assert.Contains(t, out, "pass (rn=2, dn=3)")
}

func TestRenderLastRunMD_BestWorstColumns(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("scn[c=0 p=- x=-]", &CaseLatest{Verdict: "pass", RanAt: "t1"})
	s.RecordExecuted("scn[c=0 p=- x=-]", &CaseLatest{Verdict: "fail", RanAt: "t2"})
	out := RenderLastRunMD(s)
	// Best stays at pass, worst tracks fail.
	assert.Contains(t, out, "| pass | fail |", "best=pass worst=fail columns")
}

// --- Render(r *RunReport): the bridge wrapper ---------------------------

func TestRender_BridgeProducesNonEmptyReport(t *testing.T) {
	r := RunReport{
		RunID:    "abcd",
		Duration: "1s",
		Cases: []CaseReport{
			{ScenarioName: "scn-a", Subset: "happy", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "scn-b", Subset: "happy", Verdict: Verdict{Outcome: "fail"}},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "abcd")
	assert.Contains(t, out, "Confusion matrix")
	assert.Contains(t, out, "scn-a[c=0 p=- x=-]")
	assert.Contains(t, out, "scn-b[c=0 p=- x=-]")
	assert.Contains(t, out, "| case |", "renders the per-case table")
}

func TestRenderApproved_FiltersToApprovedOnly(t *testing.T) {
	r := RunReport{
		RunID: "z",
		Cases: []CaseReport{
			{ScenarioName: "a_draft", Subset: "happy", Status: "draft", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "b_approved", Subset: "happy", Status: "approved", Verdict: Verdict{Outcome: "pass"}},
		},
	}
	out := RenderApproved(&r)
	assert.Contains(t, out, "b_approved")
	assert.NotContains(t, out, "a_draft")
	assert.Contains(t, out, "approved")
}

func TestRender_IncludesHeadingAndMerge(t *testing.T) {
	r := RunReport{
		RunID: "z",
		Git:   GitInfo{HEAD: "abc1234", LatestMerge: "def5678"},
	}
	out := Render(&r)
	assert.Contains(t, out, "Integration tests")
	assert.Contains(t, out, "latest merge: def5678")
}

func TestRender_ConfusionMatrixKindByOutcome(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "p1", Kind: "positive", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "p2", Kind: "positive", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "p3", Kind: "positive", Verdict: Verdict{Outcome: "fail"}},
			{ScenarioName: "n1", Kind: "negative", Verdict: Verdict{Outcome: "pass"}},
			{ScenarioName: "n2", Kind: "negative", Verdict: Verdict{Outcome: "fail"}},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "Confusion matrix")
	assert.Contains(t, out, "+ve (through)")
	assert.Contains(t, out, "-ve (error/warning)")
}

func TestRender_FailureDetailsSectionPrintsReasonForEachFailingCase(t *testing.T) {
	// Phase 3.7: the Cases table shows pass/fail but the captured Gomega
	// reason from RunCase used to disappear. The "Failure Details"
	// section must surface it so triage doesn't need container logs.
	r := RunReport{
		Cases: []CaseReport{
			{
				ScenarioName: "Create Room", Subset: "case", Kind: "positive",
				Verdict: Verdict{Outcome: "pass"},
			},
			{
				ScenarioName: "Banned User", Subset: "case", Kind: "negative",
				Verdict: Verdict{
					Outcome: "fail",
					Reason:  "MatchShape: polled 3 events, none matched expected shape {status: accepted}",
				},
			},
		},
	}
	out := Render(&r)

	// Section header present.
	assert.Contains(t, out, "## Failure Details")
	// Failing case shows scenario name + status + the reason text inside a fenced block.
	assert.Contains(t, out, "### Banned User")
	assert.Contains(t, out, "MatchShape: polled 3 events")
	assert.Contains(t, out, "```")
	// Passing case must NOT be listed under Failure Details.
	assert.NotContains(t, out, "### Create Room")
}

func TestRender_FailureDetailsSectionAbsentWhenAllPass(t *testing.T) {
	r := RunReport{
		Cases: []CaseReport{
			{ScenarioName: "happy", Kind: "positive", Verdict: Verdict{Outcome: "pass"}},
		},
	}
	out := Render(&r)
	assert.NotContains(t, out, "## Failure Details", "section header must be suppressed when nothing failed")
}

func TestRender_FailureDetailsSkippedCasesIncluded(t *testing.T) {
	// A skipped case (chaos reset failed between cases) is a real signal —
	// don't hide it in the failure section.
	r := RunReport{
		Cases: []CaseReport{
			{
				ScenarioName: "Mongo Partition", Subset: "case", Kind: "positive",
				Verdict: Verdict{Outcome: "skipped", Reason: "chaos engine reset failed"},
			},
		},
	}
	out := Render(&r)
	assert.Contains(t, out, "## Failure Details")
	assert.Contains(t, out, "### Mongo Partition")
	assert.Contains(t, out, "chaos engine reset failed")
}
