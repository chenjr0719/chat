package runtime

import (
	"time"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
)

// recordCase adapts a CaseVerdict outcome into the existing CaseReport /
// CaseLatest shape so the reporter (last-run.md confusion matrix +
// table) and PerformanceStore (best/worst tracking) keep working
// unchanged. Case ID format: `<scenario>/<case-name>` — distinct from
// v2's `<scenario>[c=X p=Y x=Z]` so the two paths don't collide in
// performance.json.
func recordCase(perf *PerformanceStore, report *RunReport,
	s *scenario.Scenario, c *scenario.Case, v CaseVerdict, dur time.Duration) {
	caseID := s.Name + "/" + c.Name
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
		Subset:       "case", // distinguishes from v2's "happy" / "mishap"
		Status:       v3Status(s),
		Kind:         c.Tag, // "positive" | "negative" — feeds confusion matrix
		Duration:     dur,
		Verdict:      Verdict{Outcome: v.Outcome, Reason: v.Reason},
	})
}

// recordSkipped records a case that wasn't attempted because the
// between-case chaos reset failed. The reporter renders it as a
// "skipped" row alongside genuine pass/fail rows.
func recordSkipped(perf *PerformanceStore, report *RunReport,
	s *scenario.Scenario, c *scenario.Case, reason string) {
	caseID := s.Name + "/" + c.Name
	perf.RecordSkipped(caseID, reason, report.StartISO)
	report.Cases = append(report.Cases, CaseReport{
		ScenarioName: s.Name,
		Subset:       "case",
		Status:       v3Status(s),
		Kind:         c.Tag,
		Verdict:      Verdict{Outcome: "skipped", Reason: reason},
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
