package runtime

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sort"
)

// PerformanceStore is the persisted case-record file (§5.4).
// Backs latest/best/worst tracking and history-aware ignore.
type PerformanceStore struct {
	SchemaVersion int                   `json:"schemaVersion"`
	LastRunAt     string                `json:"lastRunAt,omitempty"`
	Cases         map[string]*CaseEntry `json:"cases"`
}

// CaseEntry is the per-case record. Best/Worst track only executed
// runs; Latest reflects the current run (including skipped).
type CaseEntry struct {
	Latest *CaseLatest  `json:"latest,omitempty"`
	Best   *CaseSummary `json:"best,omitempty"`
	Worst  *CaseSummary `json:"worst,omitempty"`
}

// CaseLatest captures everything from this run.
type CaseLatest struct {
	RanAt        string         `json:"ranAt"`
	Verdict      string         `json:"verdict"` // pass | fail | skipped
	SkipReason   string         `json:"skipReason,omitempty"`
	ReadsMatched string         `json:"readsMatched,omitempty"`
	Cascades     int            `json:"cascades,omitempty"`
	DurationMs   int64          `json:"durationMs,omitempty"`
	ErrorBlocks  []ErrorBlock   `json:"errorBlocks,omitempty"`
	NoiseMatches map[string]int `json:"noiseMatches,omitempty"`
}

// CaseSummary captures the verdict + timestamp for best/worst.
type CaseSummary struct {
	Verdict string `json:"verdict"`
	RanAt   string `json:"ranAt"`
}

// ErrorBlock captures one retry attempt's failures.
type ErrorBlock struct {
	Attempt int              `json:"attempt"`
	Label   string           `json:"label"`
	Events  []map[string]any `json:"events"`
}

// NewPerformanceStore returns an empty store ready to be populated.
func NewPerformanceStore() *PerformanceStore {
	return &PerformanceStore{SchemaVersion: 1, Cases: map[string]*CaseEntry{}}
}

// LoadPerformance reads + parses the file at path. Missing file =>
// empty store. Drops legacy pre-Phase-1 case IDs (m= format) with a
// warning — see DropLegacyMongoKindIDs.
func LoadPerformance(path string) (*PerformanceStore, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return NewPerformanceStore(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read performance.json: %w", err)
	}
	s := NewPerformanceStore()
	if err := json.Unmarshal(b, s); err != nil {
		return nil, fmt.Errorf("parse performance.json: %w", err)
	}
	if s.Cases == nil {
		s.Cases = map[string]*CaseEntry{}
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = 1
	}
	DropLegacyMongoKindIDs(s)
	return s, nil
}

// SavePerformance writes the store as canonical JSON, creating parent dirs.
func SavePerformance(path string, s *PerformanceStore) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir performance.json: %w", err)
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal performance.json: %w", err)
	}
	return os.WriteFile(path, b, 0o644)
}

// RecordExecuted updates Latest/Best/Worst for a case that actually ran.
func (s *PerformanceStore) RecordExecuted(caseID string, latest *CaseLatest) {
	entry := s.Cases[caseID]
	if entry == nil {
		entry = &CaseEntry{}
		s.Cases[caseID] = entry
	}
	entry.Latest = latest
	summary := &CaseSummary{Verdict: latest.Verdict, RanAt: latest.RanAt}
	if entry.Best == nil || verdictBetter(latest.Verdict, entry.Best.Verdict) {
		entry.Best = summary
	}
	if entry.Worst == nil || verdictBetter(entry.Worst.Verdict, latest.Verdict) {
		entry.Worst = summary
	}
}

// RecordSkipped marks the case as skipped this run; best/worst untouched.
func (s *PerformanceStore) RecordSkipped(caseID, reason, ranAt string) {
	entry := s.Cases[caseID]
	if entry == nil {
		entry = &CaseEntry{}
		s.Cases[caseID] = entry
	}
	entry.Latest = &CaseLatest{
		RanAt:      ranAt,
		Verdict:    "skipped",
		SkipReason: reason,
	}
}

// IDs returns case IDs in a stable sorted order (for deterministic reports).
func (s *PerformanceStore) IDs() []string {
	out := make([]string, 0, len(s.Cases))
	for id := range s.Cases {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// legacyMongoKindRe matches the pre-Phase-1 case-ID format that used
// the m= axis (m=- / m=pause / m=disconnect / m=<pod>). The Phase-1
// case-grid replaces it with p= (mongo / cassandra partition targets).
var legacyMongoKindRe = regexp.MustCompile(`\[c=[01]\s+m=`)

// DropLegacyMongoKindIDs removes every case-ID row keyed with the
// pre-Phase-1 m= axis and logs the count at slog.Warn so engineers
// see the reset. The mongo-pause / mongo-disconnect kinds were
// replaced by toxiproxy-backed partitions whose mechanics differ
// enough that comparing old timings/verdicts to new ones is
// misleading — a one-time history reset is the documented behavior
// (spec §5.5).
//
// Idempotent: on a store with no legacy rows it is a no-op and
// produces no log line.
func DropLegacyMongoKindIDs(s *PerformanceStore) {
	if s == nil || s.Cases == nil {
		return
	}
	var dropped []string
	for id := range s.Cases {
		if legacyMongoKindRe.MatchString(id) {
			dropped = append(dropped, id)
		}
	}
	if len(dropped) == 0 {
		return
	}
	sort.Strings(dropped)
	for _, id := range dropped {
		delete(s.Cases, id)
	}
	sample := dropped
	if len(sample) > 3 {
		sample = sample[:3]
	}
	slog.Warn("dropped legacy pre-Phase-1 perf-history rows (m= axis)",
		"count", len(dropped),
		"sample", sample,
	)
}

// verdictBetter returns true if a is more-favorable than b in the
// pass > fail ordering. skipped is outside the ordering (never enters
// best/worst).
func verdictBetter(a, b string) bool {
	rank := map[string]int{"pass": 2, "fail": 1}
	return rank[a] > rank[b]
}
