package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
)

func TestRecordCase_PassAppendsAndRecords(t *testing.T) {
	perf := NewPerformanceStore()
	report := &RunReport{StartISO: "2026-05-31T00:00:00Z"}
	s := &scenario.Scenario{Name: "scn", Status: "approved"}
	c := &scenario.Case{Name: "happy", Tag: "positive"}

	recordCase(perf, report, s, c, CaseVerdict{Outcome: "pass"}, 42*time.Millisecond)

	require.Len(t, report.Cases, 1)
	got := report.Cases[0]
	assert.Equal(t, "scn", got.ScenarioName)
	assert.Equal(t, "case", got.Subset)
	assert.Equal(t, "approved", got.Status)
	assert.Equal(t, "positive", got.Kind)
	assert.Equal(t, "pass", got.Verdict.Outcome)
	assert.Equal(t, 42*time.Millisecond, got.Duration)

	entry, ok := perf.Cases["scn/happy"]
	require.True(t, ok, "perf store must key by <scenario>/<case-name>")
	assert.Equal(t, "pass", entry.Latest.Verdict)
	assert.Equal(t, int64(42), entry.Latest.DurationMs)
	assert.Empty(t, entry.Latest.ErrorBlocks)
}

func TestRecordCase_FailWritesErrorBlock(t *testing.T) {
	perf := NewPerformanceStore()
	report := &RunReport{StartISO: "2026-05-31T00:00:00Z"}
	s := &scenario.Scenario{Name: "scn"}
	c := &scenario.Case{Name: "broken", Tag: "negative"}

	recordCase(perf, report, s, c, CaseVerdict{
		Outcome:  "fail",
		Reason:   "expected status=200, got 500",
		Failures: []string{"expected status=200, got 500"},
	}, 100*time.Millisecond)

	require.Len(t, report.Cases, 1)
	assert.Equal(t, "fail", report.Cases[0].Verdict.Outcome)
	assert.Equal(t, "expected status=200, got 500", report.Cases[0].Verdict.Reason)
	assert.Equal(t, "negative", report.Cases[0].Kind)

	entry := perf.Cases["scn/broken"]
	require.NotNil(t, entry)
	require.Len(t, entry.Latest.ErrorBlocks, 1)
	assert.Equal(t, "expected status=200, got 500", entry.Latest.ErrorBlocks[0].Label)
}

func TestRecordSkipped_RecordsSkippedRowAndPerf(t *testing.T) {
	perf := NewPerformanceStore()
	report := &RunReport{StartISO: "2026-05-31T00:00:00Z"}
	s := &scenario.Scenario{Name: "scn"}
	c := &scenario.Case{Name: "interrupted", Tag: "positive"}

	recordSkipped(perf, report, s, c, "chaos engine reset failed")

	require.Len(t, report.Cases, 1)
	assert.Equal(t, "skipped", report.Cases[0].Verdict.Outcome)
	assert.Contains(t, report.Cases[0].Verdict.Reason, "chaos engine reset failed")

	entry := perf.Cases["scn/interrupted"]
	require.NotNil(t, entry)
	assert.Equal(t, "skipped", entry.Latest.Verdict)
	assert.Equal(t, "chaos engine reset failed", entry.Latest.SkipReason)
}

func TestStatus_ApprovedAndDraftDefault(t *testing.T) {
	assert.Equal(t, "approved", v3Status(&scenario.Scenario{Status: "approved"}))
	assert.Equal(t, "draft", v3Status(&scenario.Scenario{Status: "draft"}))
	assert.Equal(t, "draft", v3Status(&scenario.Scenario{}), "missing status defaults to draft")
}
