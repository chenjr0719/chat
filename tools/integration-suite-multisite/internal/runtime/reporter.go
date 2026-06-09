package runtime

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ScenarioReport is one scenario's verdict and metadata.
// CaseReport is kept as a type alias so existing tests compile unchanged.
type ScenarioReport struct {
	// Name is the scenario's display name (from Scenario.Name).
	Name string
	// ScenarioName is an alias for Name retained for backward compatibility.
	// Callers should use Name going forward.
	ScenarioName string
	// SourcePath is the YAML file's path relative to the runner CWD
	// (or absolute). Surfaced in failure detail so the operator can
	// `vi` straight to the file — important now that subdirectory
	// nesting under scenarios/drafts/ is allowed (plan-ahead §2.8).
	SourcePath string
	// Subset is kept for backward compatibility but unused in multi-site flow.
	Subset   string
	Status   string
	Kind     string // "positive" / "negative"; feeds the confusion matrix
	Duration time.Duration
	Verdict  Verdict
}

// CaseReport is a type alias for ScenarioReport kept for backward
// compatibility with existing tests. New code should use ScenarioReport.
type CaseReport = ScenarioReport

// Verdict is the case outcome surface — the trimmed shape consumed
// by the reporter. The rich v2 Classify outputs (MissingPositives,
// UnexpectedCascades, per-step results) are gone: Phase 3 captures
// failures as joined Gomega messages on Reason.
type Verdict struct {
	Outcome string // "pass" | "fail" | "skipped"
	Reason  string
}

// RunReport is the aggregate of one suite invocation.
type RunReport struct {
	RunID    string
	StartISO string
	Duration string
	// Scenarios holds all per-scenario results for this run.
	Scenarios []ScenarioReport
	// Cases is an alias for Scenarios retained for backward compatibility.
	Cases []ScenarioReport
	Git   GitInfo // captured at the start of the run
	Perf  *PerformanceStore
}

// Render produces the markdown body of the per-run report. Renders
// all cases regardless of status; the approved-only filter is applied
// by the caller via RenderApproved.
func Render(r *RunReport) string {
	return render(r, false)
}

// RenderApproved is like Render but only includes cases with
// Status == "approved".
func RenderApproved(r *RunReport) string {
	filtered := *r
	filtered.Cases = filtered.Cases[:0:0]
	for i := range r.Cases {
		if r.Cases[i].Status == "approved" {
			filtered.Cases = append(filtered.Cases, r.Cases[i])
		}
	}
	return render(&filtered, true)
}

// render bridges Part-1 CaseReports into a PerformanceStore and
// delegates to RenderLastRunMD. Each Part-1 scenario becomes a
// happy-case row keyed `<scenario>[c=0 p=- x=-]`.
func render(r *RunReport, approvedOnly bool) string {
	var b strings.Builder

	mergeHash := r.Git.LatestMerge
	if mergeHash == "" {
		mergeHash = "—"
	}
	scope := "total"
	if approvedOnly {
		scope = "approved"
	}
	fmt.Fprintf(&b, "# Integration tests — %s   (latest merge: %s)\n\n", scope, mergeHash)
	fmt.Fprintf(&b, "Run:        %s   (runID %s)\nDuration:   %s\nTotal:      %d test cases\n\n",
		r.StartISO, r.RunID, r.Duration, len(r.Cases))

	// Confusion matrix + status breakdown — kept for run-summary value.
	posTrue, posFalse := 0, 0
	negTrue, negFalse := 0, 0
	approvedPass, approvedFail := 0, 0
	draftPass, draftFail := 0, 0
	for i := range r.Cases {
		c := &r.Cases[i]
		pass := c.Verdict.Outcome == "pass"
		negative := c.Kind == "negative"
		switch {
		case negative && pass:
			negTrue++
		case negative && !pass:
			negFalse++
		case !negative && pass:
			posTrue++
		default:
			posFalse++
		}
		switch {
		case c.Status == "approved" && pass:
			approvedPass++
		case c.Status == "approved" && !pass:
			approvedFail++
		case pass:
			draftPass++
		default:
			draftFail++
		}
	}
	b.WriteString("## Confusion matrix\n\n")
	b.WriteString("                        true (pass)   false (fail)\n")
	fmt.Fprintf(&b, "  +ve (through)         %-13d %-13d\n", posTrue, posFalse)
	fmt.Fprintf(&b, "  -ve (error/warning)   %-13d %-13d\n\n", negTrue, negFalse)

	b.WriteString("## Status breakdown\n\n")
	b.WriteString("              pass    fail\n")
	fmt.Fprintf(&b, "APPROVED      %-7d %-7d\n", approvedPass, approvedFail)
	fmt.Fprintf(&b, "DRAFT         %-7d %-7d\n\n", draftPass, draftFail)

	// Build a temporary PerformanceStore from the current run's cases,
	// merging in any pre-loaded best/worst history from r.Perf so the
	// per-case table reflects both this run's verdict and the historical
	// best/worst columns.
	tmp := NewPerformanceStore()
	if r.Perf != nil {
		// Seed with prior history so best/worst flow into the render.
		for id, e := range r.Perf.Cases {
			tmp.Cases[id] = e
		}
	}
	for i := range r.Cases {
		c := &r.Cases[i]
		caseID := caseIDFor(c.ScenarioName)
		latest := &CaseLatest{
			RanAt:      r.StartISO,
			Verdict:    verdictString(c.Verdict.Outcome),
			DurationMs: c.Duration.Milliseconds(),
		}
		if c.Verdict.Reason != "" && latest.Verdict == "fail" {
			latest.ErrorBlocks = []ErrorBlock{{Attempt: 1, Label: c.Verdict.Reason}}
		}
		tmp.RecordExecuted(caseID, latest)
	}

	b.WriteString("## Cases\n\n")
	b.WriteString(RenderLastRunMD(tmp))

	// Failure Details section — Phase 3.7. The Cases table above shows
	// only `pass` / `fail` / `skipped` per case; the actual Gomega
	// failure message that RunCase captured into CaseVerdict.Reason
	// would otherwise stay invisible. Surface it directly so an
	// engineer triaging the report doesn't need to grep container logs.
	if details := renderFailureDetails(r.Cases); details != "" {
		b.WriteString("\n## Failure Details\n\n")
		b.WriteString(details)
	}

	return b.String()
}

// renderFailureDetails emits one block per failing case in cases, in
// declaration order. Returns "" when there are no failures so the
// caller can decide whether to print the header. Skipped cases are
// included (a chaos-reset failure is a real signal worth seeing).
func renderFailureDetails(cases []CaseReport) string {
	var b strings.Builder
	for i := range cases {
		c := &cases[i]
		if c.Verdict.Outcome == "pass" {
			continue
		}
		fmt.Fprintf(&b, "### %s — %s\n\n", c.ScenarioName, c.Verdict.Outcome)
		if c.SourcePath != "" {
			fmt.Fprintf(&b, "- file: `%s`\n", c.SourcePath)
		}
		fmt.Fprintf(&b, "- subset: `%s`  kind: `%s`  duration: %s\n",
			c.Subset, c.Kind, formatDuration(c.Duration.Milliseconds()))
		if c.Verdict.Reason == "" {
			b.WriteString("- reason: _(no reason captured)_\n\n")
			continue
		}
		// Reason is the joined Gomega failure message — often multi-line.
		// Render inside a fenced block so newlines and indentation
		// (Gomega's "Expected ... to ...") survive intact.
		b.WriteString("- reason:\n\n```\n")
		b.WriteString(c.Verdict.Reason)
		if !strings.HasSuffix(c.Verdict.Reason, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	}
	return b.String()
}

// RenderLastRunMD produces the human-readable per-case report
// from a PerformanceStore. Called at end of every run (§5.1).
func RenderLastRunMD(s *PerformanceStore) string {
	var b strings.Builder
	b.WriteString("| case | latest | best | worst | reads | cascades | duration |\n")
	b.WriteString("|------|--------|------|-------|-------|----------|----------|\n")
	for _, id := range s.IDs() {
		e := s.Cases[id]
		latest := formatLatest(e.Latest)
		best := formatSummary(e.Best)
		worst := formatSummary(e.Worst)
		reads, cascades, dur := "—", "—", "—"
		if e.Latest != nil && e.Latest.Verdict != "skipped" {
			if e.Latest.ReadsMatched != "" {
				reads = e.Latest.ReadsMatched
			}
			cascades = strconv.Itoa(e.Latest.Cascades)
			dur = formatDuration(e.Latest.DurationMs)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s |\n",
			id, latest, best, worst, reads, cascades, dur)
	}
	return b.String()
}

func formatLatest(l *CaseLatest) string {
	if l == nil {
		return "—"
	}
	if l.Verdict == "skipped" {
		return fmt.Sprintf("skipped (%s)", l.SkipReason)
	}
	parts := []string{l.Verdict}
	if rn := l.NoiseMatches["restart_noise"]; rn > 0 {
		parts = append(parts, fmt.Sprintf("rn=%d", rn))
	}
	if dn := l.NoiseMatches["disconnect_noise"]; dn > 0 {
		parts = append(parts, fmt.Sprintf("dn=%d", dn))
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return fmt.Sprintf("%s (%s)", parts[0], strings.Join(parts[1:], ", "))
}

func formatSummary(s *CaseSummary) string {
	if s == nil {
		return "—"
	}
	return s.Verdict
}

func formatDuration(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// caseIDFor synthesizes the Part-2 case-ID form for a Part-1 scenario.
// Part-1 only has happy cases; mishap-expanded cases supply non-default
// c/p/x annotations directly via Case.ID().
func caseIDFor(scenarioName string) string {
	return fmt.Sprintf("%s[c=0 p=- x=-]", scenarioName)
}

// verdictString maps Part-1's outcome strings ("pass" / "fail") to the
// Part-2 verdict vocabulary. Anything other than "pass" is treated as
// "fail"; "skipped" comes only from RecordSkipped (mishap path), never
// from a CaseReport.
func verdictString(outcome string) string {
	if outcome == "pass" {
		return "pass"
	}
	return "fail"
}

// Write persists Render to a file path, creating parent dirs.
func Write(path string, r *RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(Render(r)), 0o644)
}

// WriteApproved persists RenderApproved to a file path.
func WriteApproved(path string, r *RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(RenderApproved(r)), 0o644)
}
