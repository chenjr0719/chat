package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMergePerformance_LaterRanAtWins(t *testing.T) {
	a := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Latest: &CaseLatest{Verdict: "pass", RanAt: "2026-05-26T10:00:00Z"}},
	}}
	b := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Latest: &CaseLatest{Verdict: "fail", RanAt: "2026-05-26T11:00:00Z"}},
	}}

	merged := Merge(a, b)
	assert.Equal(t, "fail", merged.Cases["case-X"].Latest.Verdict)
}

func TestMergePerformance_BestKeepsMostFavorable(t *testing.T) {
	a := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Best: &CaseSummary{Verdict: "pass", RanAt: "t1"}},
	}}
	b := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Best: &CaseSummary{Verdict: "fail", RanAt: "t2"}},
	}}

	merged := Merge(a, b)
	assert.Equal(t, "pass", merged.Cases["case-X"].Best.Verdict)
}

func TestMergePerformance_WorstKeepsLeastFavorable(t *testing.T) {
	a := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Worst: &CaseSummary{Verdict: "pass", RanAt: "t1"}},
	}}
	b := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Worst: &CaseSummary{Verdict: "fail", RanAt: "t2"}},
	}}

	merged := Merge(a, b)
	assert.Equal(t, "fail", merged.Cases["case-X"].Worst.Verdict)
}

func TestMergePerformance_BestTieKeepsEarlierRanAt(t *testing.T) {
	a := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Best: &CaseSummary{Verdict: "pass", RanAt: "2026-05-26T10:00:00Z"}},
	}}
	b := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Best: &CaseSummary{Verdict: "pass", RanAt: "2026-05-26T11:00:00Z"}},
	}}

	merged := Merge(a, b)
	assert.Equal(t, "2026-05-26T10:00:00Z", merged.Cases["case-X"].Best.RanAt,
		"tie on verdict — earlier RanAt wins for Best")
}

func TestMergePerformance_WorstTieKeepsLaterRanAt(t *testing.T) {
	a := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Worst: &CaseSummary{Verdict: "fail", RanAt: "2026-05-26T10:00:00Z"}},
	}}
	b := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-X": {Worst: &CaseSummary{Verdict: "fail", RanAt: "2026-05-26T11:00:00Z"}},
	}}

	merged := Merge(a, b)
	assert.Equal(t, "2026-05-26T11:00:00Z", merged.Cases["case-X"].Worst.RanAt,
		"tie on verdict — later RanAt wins for Worst")
}

func TestMergePerformance_OneSidedCaseTakenWhole(t *testing.T) {
	a := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-Y": {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
	}}
	b := &PerformanceStore{Cases: map[string]*CaseEntry{
		"case-Z": {Latest: &CaseLatest{Verdict: "fail", RanAt: "t2"}},
	}}

	merged := Merge(a, b)
	assert.Equal(t, "pass", merged.Cases["case-Y"].Latest.Verdict)
	assert.Equal(t, "fail", merged.Cases["case-Z"].Latest.Verdict)
}
