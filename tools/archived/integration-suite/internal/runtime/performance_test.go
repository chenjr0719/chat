package runtime

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPerformanceStore_LoadEmpty(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "performance.json")
	require.NoError(t, os.WriteFile(p, []byte(`{"schemaVersion":1,"cases":{}}`), 0o644))

	s, err := LoadPerformance(p)
	require.NoError(t, err)
	assert.Equal(t, 1, s.SchemaVersion)
	assert.Empty(t, s.Cases)
}

func TestPerformanceStore_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "performance.json")

	s := &PerformanceStore{
		SchemaVersion: 1,
		Cases: map[string]*CaseEntry{
			"room-create-001[c=0 p=- x=-]": {
				Latest: &CaseLatest{
					RanAt:   "2026-05-26T12:00:00Z",
					Verdict: "pass",
				},
				Best:  &CaseSummary{Verdict: "pass", RanAt: "2026-05-26T12:00:00Z"},
				Worst: &CaseSummary{Verdict: "pass", RanAt: "2026-05-26T12:00:00Z"},
			},
		},
	}
	require.NoError(t, SavePerformance(p, s))

	loaded, err := LoadPerformance(p)
	require.NoError(t, err)
	require.NotNil(t, loaded.Cases["room-create-001[c=0 p=- x=-]"])
	assert.Equal(t, "pass", loaded.Cases["room-create-001[c=0 p=- x=-]"].Latest.Verdict)
}

func TestVerdictBetter_Ordering(t *testing.T) {
	// pass > fail (skipped not in ordering)
	assert.True(t, verdictBetter("pass", "fail"))
	assert.False(t, verdictBetter("fail", "pass"))
	assert.False(t, verdictBetter("pass", "pass"))
}

func TestPerformanceStore_UpdateRecordsLatestAndBest(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("case-A", &CaseLatest{Verdict: "pass", RanAt: "t1"})
	assert.Equal(t, "pass", s.Cases["case-A"].Best.Verdict)
	assert.Equal(t, "pass", s.Cases["case-A"].Worst.Verdict)

	s.RecordExecuted("case-A", &CaseLatest{Verdict: "fail", RanAt: "t2"})
	assert.Equal(t, "pass", s.Cases["case-A"].Best.Verdict, "best stays at pass")
	assert.Equal(t, "fail", s.Cases["case-A"].Worst.Verdict, "worst updates to fail")
	assert.Equal(t, "fail", s.Cases["case-A"].Latest.Verdict)
}

func TestPerformanceStore_SkippedLeavesBestWorstUntouched(t *testing.T) {
	s := NewPerformanceStore()
	s.RecordExecuted("case-A", &CaseLatest{Verdict: "pass", RanAt: "t1"})
	s.RecordSkipped("case-A", "x=room-worker fails alone", "t2")
	assert.Equal(t, "pass", s.Cases["case-A"].Best.Verdict)
	assert.Equal(t, "pass", s.Cases["case-A"].Worst.Verdict)
	assert.Equal(t, "skipped", s.Cases["case-A"].Latest.Verdict)
	assert.Equal(t, "x=room-worker fails alone", s.Cases["case-A"].Latest.SkipReason)
}

func TestPerformanceStore_DropsLegacyMongoKindIDsOnLoad(t *testing.T) {
	s := &PerformanceStore{
		SchemaVersion: 1,
		Cases: map[string]*CaseEntry{
			"room-create-001[c=0 m=- x=-]":                    {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
			"room-create-001[c=0 m=pause x=-]":                {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
			"room-create-001[c=1 m=disconnect x=room-worker]": {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
			// Phase-1 row format — must be preserved.
			"room-create-001[c=0 p=mongo x=-]": {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
		},
	}
	DropLegacyMongoKindIDs(s)

	// All m= rows are dropped.
	assert.Empty(t, s.Cases["room-create-001[c=0 m=- x=-]"])
	assert.Empty(t, s.Cases["room-create-001[c=0 m=pause x=-]"])
	assert.Empty(t, s.Cases["room-create-001[c=1 m=disconnect x=room-worker]"])
	// p= row is untouched.
	require.NotNil(t, s.Cases["room-create-001[c=0 p=mongo x=-]"])
	assert.Len(t, s.Cases, 1)
}

func TestPerformanceStore_DropLegacyMongoKindIDs_NoOpOnCleanStore(t *testing.T) {
	s := &PerformanceStore{
		SchemaVersion: 1,
		Cases: map[string]*CaseEntry{
			"scn[c=0 p=- x=-]":     {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
			"scn[c=0 p=mongo x=-]": {Latest: &CaseLatest{Verdict: "pass", RanAt: "t1"}},
		},
	}
	before := len(s.Cases)
	DropLegacyMongoKindIDs(s)
	assert.Equal(t, before, len(s.Cases), "clean store must not lose rows")
}
