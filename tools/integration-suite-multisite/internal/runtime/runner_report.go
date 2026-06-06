package runtime

import (
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// Stubbed for multi-site refactor; Task 14 will reimplement.

// scenarioVerdict is the per-scenario outcome used by the stubbed runner.
type scenarioVerdict struct {
	Outcome string
	Reason  string
}

// recordScenario adapts a scenarioVerdict into the CaseReport / CaseLatest
// shape so the reporter and PerformanceStore keep working. This is a
// stub — Task 14 will reimplement with proper per-case recording.
func recordScenario(perf *PerformanceStore, report *RunReport,
	s *scenario.Scenario, v scenarioVerdict, dur time.Duration) {
	caseID := s.Name + "/stub"
	latest := &CaseLatest{
		RanAt:      report.StartISO,
		Verdict:    v.Outcome,
		DurationMs: dur.Milliseconds(),
	}
	if v.Outcome == "fail" {
		latest.ErrorBlocks = []ErrorBlock{{Attempt: 1, Label: v.Reason}}
	}
	perf.RecordExecuted(caseID, latest)
	report.Cases = append(report.Cases, CaseReport{
		ScenarioName: s.Name,
		Subset:       "scenario",
		Status:       v3Status(s),
		Kind:         s.Tag,
		Duration:     dur,
		Verdict:      Verdict(v),
	})
}

// v3Status mirrors v2's status(*Scenario) — returns "approved" for the
// gated CI-relevant scenarios and "draft" otherwise.
func v3Status(s *scenario.Scenario) string {
	if s.Status == "approved" {
		return "approved"
	}
	return "draft"
}
