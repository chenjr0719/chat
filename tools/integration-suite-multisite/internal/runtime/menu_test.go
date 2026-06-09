package runtime

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 4.6 — menu loop tests. See docs/spec-scenario-dev-mode.md §5.
//
// These exercise parseInput (pure function) + the per-row state
// machine (via runRow's lifecycle indicators) WITHOUT spinning up a
// Mongo / NATS / Cassandra connection. The connection lifecycle is
// covered by the existing runner_v3 + integration test surface.

// ─── parseInput — every action vocabulary entry + edge case ────────

func TestParseInput_PickOneAtBoundary(t *testing.T) {
	tests := []struct {
		input  string
		n      int
		wantOk bool
		wantIx int
	}{
		{"1", 11, true, 1},
		{"5", 11, true, 5},
		{"11", 11, true, 11},
		{"12", 11, false, 0}, // out of range high
		{"0", 11, false, 0},  // out of range low
		{"-1", 11, false, 0}, // negatives error at strconv.Atoi or as out-of-range
		{"99", 11, false, 0}, // way past N
	}
	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			a, err := parseInput(tc.input, tc.n, menuAction{kind: actionNone})
			if tc.wantOk {
				require.NoError(t, err)
				assert.Equal(t, actionPickOne, a.kind)
				assert.Equal(t, tc.wantIx, a.idx)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestParseInput_KeywordActions(t *testing.T) {
	cases := map[string]menuActionKind{
		"a":      actionPickAll,
		"A":      actionPickAll, // case-insensitive
		"all":    actionPickAll,
		"f":      actionPickFailed,
		"failed": actionPickFailed,
		"r":      actionRescan,
		"rescan": actionRescan,
		"q":      actionQuit,
		"quit":   actionQuit,
		"exit":   actionQuit,
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			a, err := parseInput(input, 11, menuAction{kind: actionNone})
			require.NoError(t, err)
			assert.Equal(t, want, a.kind)
		})
	}
}

func TestParseInput_EmptyEnter_NoLastAction_Errors(t *testing.T) {
	_, err := parseInput("", 11, menuAction{kind: actionNone})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no last action to repeat",
		"empty ENTER before any pick must hint at the action vocabulary")
}

func TestParseInput_EmptyEnter_RepeatsLastPickOne(t *testing.T) {
	last := menuAction{kind: actionPickOne, idx: 5}
	a, err := parseInput("", 11, last)
	require.NoError(t, err)
	assert.Equal(t, last, a, "empty ENTER must repeat the prior single-pick verbatim")
}

func TestParseInput_EmptyEnter_RepeatsLastPickAll(t *testing.T) {
	a, err := parseInput("", 11, menuAction{kind: actionPickAll})
	require.NoError(t, err)
	assert.Equal(t, actionPickAll, a.kind)
}

func TestParseInput_EmptyEnter_RepeatsLastPickFailed(t *testing.T) {
	a, err := parseInput("", 11, menuAction{kind: actionPickFailed})
	require.NoError(t, err)
	assert.Equal(t, actionPickFailed, a.kind)
}

func TestParseInput_UnrecognisedInputErrors(t *testing.T) {
	for _, bad := range []string{"xyz", "1.5", "1,3", "?", " "} {
		t.Run(bad, func(t *testing.T) {
			_, err := parseInput(strings.TrimSpace(bad), 11, menuAction{kind: actionNone})
			assert.Error(t, err)
		})
	}
}

// ─── menuState seeding + sort stability ────────────────────────────

func TestInitMenuState_SortsByName(t *testing.T) {
	// Pass a list of paths whose extracted display names are
	// intentionally not lexicographically sorted by path.
	files := []string{
		"/tmp/zulu.yaml",
		"/tmp/alpha.yaml",
		"/tmp/mike.yaml",
	}
	state := initMenuState("/tmp", files)
	require.Len(t, state.rows, 3)
	assert.Equal(t, "alpha", state.rows[0].name)
	assert.Equal(t, "mike", state.rows[1].name)
	assert.Equal(t, "zulu", state.rows[2].name)
}

func TestInitMenuState_PopulatesRelPathForNesting(t *testing.T) {
	// Plan-ahead §2.8: subdir nesting should show up in the menu
	// row's relPath so the operator sees subdirectory location.
	files := []string{
		"/scenarios/drafts/alpha.yaml",
		"/scenarios/drafts/messages/send.yaml",
		"/scenarios/drafts/rooms/create.yaml",
	}
	state := initMenuState("/scenarios/drafts", files)
	require.Len(t, state.rows, 3)
	gotRel := map[string]string{}
	for _, r := range state.rows {
		gotRel[r.name] = r.relPath
	}
	assert.Equal(t, "alpha.yaml", gotRel["alpha"])
	assert.Equal(t, "messages/send.yaml", gotRel["send"])
	assert.Equal(t, "rooms/create.yaml", gotRel["create"])
}

func TestDeriveRelPath_FallbackOnEscape(t *testing.T) {
	// A file outside root should fall back to the basename rather
	// than emitting "../" relative noise.
	assert.Equal(t, "elsewhere.yaml", deriveRelPath("/scenarios/drafts", "/other/elsewhere.yaml"))
	assert.Equal(t, "foo.yaml", deriveRelPath("", "/anywhere/foo.yaml"))
}

func TestInitMenuState_InitialStatusIsNotRun(t *testing.T) {
	state := initMenuState("/tmp", []string{"/tmp/foo.yaml"})
	require.Len(t, state.rows, 1)
	assert.Equal(t, statusNotRun, state.rows[0].status)
	assert.Empty(t, state.rows[0].reason)
}

// ─── repeat-hint rendering on the prompt line ──────────────────────

func TestRepeatHint_PickOneRendersIndex(t *testing.T) {
	assert.Equal(t, " | <enter>=[5]", repeatHint(menuAction{kind: actionPickOne, idx: 5}))
}

func TestRepeatHint_PickAllRendersA(t *testing.T) {
	assert.Equal(t, " | <enter>=[a]", repeatHint(menuAction{kind: actionPickAll}))
}

func TestRepeatHint_PickFailedRendersF(t *testing.T) {
	assert.Equal(t, " | <enter>=[f]", repeatHint(menuAction{kind: actionPickFailed}))
}

func TestRepeatHint_NoPriorActionRendersEmpty(t *testing.T) {
	assert.Empty(t, repeatHint(menuAction{kind: actionNone}))
}

// ─── format helpers ────────────────────────────────────────────────

func TestFormatStatus_GlyphsMatchSpec(t *testing.T) {
	assert.Equal(t, "—", formatStatus(&scenarioRow{status: statusNotRun}))
	assert.Equal(t, "↻", formatStatus(&scenarioRow{status: statusRunning}))
}

func TestFormatStatus_PassFailIncludeDuration(t *testing.T) {
	row := scenarioRow{status: statusPass, duration: 198_000_000} // 198ms
	assert.Equal(t, "✓ 198ms", formatStatus(&row))
	row = scenarioRow{status: statusFail, duration: 5_000_000_000} // 5s
	assert.Equal(t, "✗ 5.0s", formatStatus(&row))
}

func TestTruncate_LongNamesEndWithEllipsis(t *testing.T) {
	long := strings.Repeat("a", 60)
	got := truncate(long, 48)
	// `…` is a multibyte rune; byte-len is 50 but visual width is 48.
	assert.Equal(t, 48, len([]rune(got)))
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestTruncate_ShortNamesPassThrough(t *testing.T) {
	assert.Equal(t, "foo", truncate("foo", 48))
}

// ─── render snapshot ───────────────────────────────────────────────

func TestRenderMenu_IncludesPromptAndAllRows(t *testing.T) {
	state := menuState{
		rows: []scenarioRow{
			{name: "alpha", status: statusPass, duration: 100_000_000},
			{name: "beta", status: statusFail, duration: 5_000_000_000, reason: "boom"},
			{name: "gamma", status: statusNotRun},
		},
		lastAction: menuAction{kind: actionPickOne, idx: 2},
	}
	var buf bytes.Buffer
	renderMenu(&buf, &state)
	out := buf.String()
	assert.Contains(t, out, "scenarios (3):")
	assert.Contains(t, out, "[ 1]")
	assert.Contains(t, out, "alpha")
	assert.Contains(t, out, "✓ 100ms")
	assert.Contains(t, out, "[ 2]")
	assert.Contains(t, out, "beta")
	assert.Contains(t, out, "✗ 5.0s")
	assert.Contains(t, out, "[ 3]")
	assert.Contains(t, out, "gamma")
	assert.Contains(t, out, "—")
	assert.Contains(t, out, "▶ pick [1-3] | a=all | f=failed | r=rescan | q=quit | <enter>=[2] :")
}

// ─── deriveDisplayName fallbacks ──────────────────────────────────

func TestDeriveDisplayName_StripsExtensionAndPath(t *testing.T) {
	assert.Equal(t, "history-service-paginates-messages",
		deriveDisplayName("scenarios/drafts/history-service-paginates-messages.yaml"))
	assert.Equal(t, "bare",
		deriveDisplayName("bare.yaml"))
	assert.Equal(t, "no-extension",
		deriveDisplayName("no-extension"))
}

// ─── removeScenarioRows — replace-in-place plumbing ───────────────

func TestRemoveScenarioRows_FiltersTargetScenarioOnly(t *testing.T) {
	report := &RunReport{Cases: []CaseReport{
		{ScenarioName: "alpha", Verdict: Verdict{Outcome: "pass"}},
		{ScenarioName: "beta", Verdict: Verdict{Outcome: "fail", Reason: "old beta failure"}},
		{ScenarioName: "alpha", Verdict: Verdict{Outcome: "fail", Reason: "old alpha fail"}},
		{ScenarioName: "gamma", Verdict: Verdict{Outcome: "pass"}},
	}}

	removeScenarioRows(report, "alpha")

	require.Len(t, report.Cases, 2, "both alpha rows removed; beta + gamma preserved")
	assert.Equal(t, "beta", report.Cases[0].ScenarioName)
	assert.Equal(t, "gamma", report.Cases[1].ScenarioName)
	// beta's prior fail reason must survive untouched — replace-in-place
	// for one scenario must not perturb the others.
	assert.Equal(t, "old beta failure", report.Cases[0].Verdict.Reason)
}

func TestRemoveScenarioRows_PreservesOrderOfSurvivors(t *testing.T) {
	report := &RunReport{Cases: []CaseReport{
		{ScenarioName: "z"},
		{ScenarioName: "target"},
		{ScenarioName: "a"},
		{ScenarioName: "target"},
		{ScenarioName: "m"},
	}}

	removeScenarioRows(report, "target")

	require.Len(t, report.Cases, 3)
	assert.Equal(t, "z", report.Cases[0].ScenarioName)
	assert.Equal(t, "a", report.Cases[1].ScenarioName)
	assert.Equal(t, "m", report.Cases[2].ScenarioName)
}

func TestRemoveScenarioRows_NoMatchingScenarioIsNoOp(t *testing.T) {
	report := &RunReport{Cases: []CaseReport{
		{ScenarioName: "beta"},
		{ScenarioName: "gamma"},
	}}

	removeScenarioRows(report, "alpha-never-ran")

	require.Len(t, report.Cases, 2)
	assert.Equal(t, "beta", report.Cases[0].ScenarioName)
}

func TestRemoveScenarioRows_NilReportIsNoOp(t *testing.T) {
	// Defensive — runRow can pass sess.Report which is technically
	// always non-nil in production, but the helper guards against
	// future callers anyway.
	assert.NotPanics(t, func() { removeScenarioRows(nil, "anything") })
}

func TestRemoveScenarioRows_EmptyReportIsNoOp(t *testing.T) {
	report := &RunReport{Cases: []CaseReport{}}
	removeScenarioRows(report, "alpha")
	assert.Empty(t, report.Cases)
}

// TestRemoveScenarioRows_LatestFailureReasonReplacesOldPass simulates
// the user's exact concern: scenario passes, then fails on re-pick.
// The helper + recordCase append flow must leave the report showing
// the NEW failure, not the old pass row.
func TestRemoveScenarioRows_LatestFailureReasonReplacesOldPass(t *testing.T) {
	report := &RunReport{Cases: []CaseReport{
		{ScenarioName: "target", Verdict: Verdict{Outcome: "pass"}},
	}}
	// Operator re-picks "target"; it now fails.
	removeScenarioRows(report, "target")
	// recordCase would append the new failing case row here. In the
	// menu loop this happens inside runScenario; for the unit test
	// we simulate it manually.
	report.Cases = append(report.Cases, CaseReport{
		ScenarioName: "target",
		Verdict:      Verdict{Outcome: "fail", Reason: "fresh failure on rerun"},
	})

	require.Len(t, report.Cases, 1, "exactly one row per scenario after replace-in-place")
	assert.Equal(t, "fail", report.Cases[0].Verdict.Outcome)
	assert.Equal(t, "fresh failure on rerun", report.Cases[0].Verdict.Reason,
		"fresh failure reason must survive — the user's primary ask")
}

// TestWriteInteractiveReports_DoesNotTouchCanonicalReport is the
// regression guard for the file-level clobber. Interactive picks
// MUST write to cfg.InteractiveOutputPath and leave cfg.OutputPath
// (the canonical full-suite snapshot consumed by CI + humans)
// byte-identical to whatever the last `make local` run produced.
func TestWriteInteractiveReports_DoesNotTouchCanonicalReport(t *testing.T) {
	dir := t.TempDir()
	canonical := dir + "/last-run.md"
	approved := dir + "/last-run-approved.md"
	interactive := dir + "/last-run-interactive.md"

	// Seed a "previous full-suite" snapshot the menu must NOT overwrite.
	canonicalContent := "# canonical full-suite snapshot — DO NOT TOUCH\n"
	require.NoError(t, os.WriteFile(canonical, []byte(canonicalContent), 0o644))
	approvedContent := "# approved-only CI gate — DO NOT TOUCH\n"
	require.NoError(t, os.WriteFile(approved, []byte(approvedContent), 0o644))

	sess := &session{
		Cfg: &Config{
			OutputPath:            canonical,
			ApprovedOutputPath:    approved,
			InteractiveOutputPath: interactive,
		},
		Report: &RunReport{
			RunID: "test-run",
			Cases: []CaseReport{
				{ScenarioName: "alpha", Verdict: Verdict{Outcome: "pass"}},
			},
		},
	}

	require.NoError(t, writeInteractiveReports(sess))

	got, err := os.ReadFile(canonical)
	require.NoError(t, err)
	assert.Equal(t, canonicalContent, string(got),
		"interactive pick must NOT overwrite cfg.OutputPath — that's the file-level clobber the user reported")

	gotApproved, err := os.ReadFile(approved)
	require.NoError(t, err)
	assert.Equal(t, approvedContent, string(gotApproved),
		"interactive pick must NOT overwrite ApprovedOutputPath — that's the CI gate")

	_, err = os.Stat(interactive)
	require.NoError(t, err, "interactive output MUST exist after writeInteractiveReports")
}

// TestWriteInteractiveReports_UnsetPathSkipsWrite — defensive: if the
// operator doesn't configure INTERACTIVE_OUTPUT_PATH, we no-op rather
// than fall back to clobbering cfg.OutputPath.
func TestWriteInteractiveReports_UnsetPathSkipsWrite(t *testing.T) {
	dir := t.TempDir()
	canonical := dir + "/last-run.md"
	canonicalContent := "# untouched\n"
	require.NoError(t, os.WriteFile(canonical, []byte(canonicalContent), 0o644))

	sess := &session{
		Cfg: &Config{
			OutputPath:            canonical,
			InteractiveOutputPath: "",
		},
		Report: &RunReport{RunID: "test-run"},
	}

	require.NoError(t, writeInteractiveReports(sess))

	got, err := os.ReadFile(canonical)
	require.NoError(t, err)
	assert.Equal(t, canonicalContent, string(got),
		"unset InteractiveOutputPath must NOT fall back to overwriting OutputPath")
}
