package runtime

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// Phase 4.6 — interactive scenario dev mode. The menu loop opens
// after buildSession has populated connections + registries; nothing
// runs until the operator picks something. Per-pick the targeted
// YAML is re-read from disk so editor saves are picked up.
//
// See docs/spec-scenario-dev-mode.md.

// menuActionKind is the closed enum of menu actions.
type menuActionKind int

const (
	actionPickOne    menuActionKind = iota // 1..N
	actionPickAll                          // a
	actionPickFailed                       // f
	actionRescan                           // r
	actionQuit                             // q / Ctrl+D
	actionRepeat                           // <empty ENTER>
	actionNone                             // post-parse sentinel for "no action yet"
)

// menuAction is one parsed user input.
type menuAction struct {
	kind menuActionKind
	idx  int // 1-based, only for actionPickOne
}

// rowStatus is the per-scenario lifecycle state shown in the menu.
type rowStatus int

const (
	statusNotRun  rowStatus = iota // shown as "—"
	statusRunning                  // shown as "↻"
	statusPass                     // shown as "✓"
	statusFail                     // shown as "✗"
)

// scenarioRow is one displayable / pickable scenario.
type scenarioRow struct {
	path     string // absolute or runner-relative file path
	relPath  string // path relative to the scenarios root, for display (subdir nesting, §2.8)
	name     string // from Scenario.Name
	status   rowStatus
	duration time.Duration
	reason   string // abridged failure reason for ✗ rows
}

// menuState is the menu loop's working state.
type menuState struct {
	rows       []scenarioRow
	lastAction menuAction
}

// runMenuLoop drives the interactive menu against an open session.
// Quits on `q`, Ctrl+D (stdin EOF), or any unrecoverable error from
// the per-scenario runner. Per-pick I/O errors (parse failures,
// file-not-found on disk) are surfaced inline and the loop continues.
func runMenuLoop(ctx context.Context, sess *session, scenarioFiles []string) error {
	state := initMenuState(sess.Cfg.ScenariosDir, scenarioFiles)
	scanner := bufio.NewScanner(os.Stdin)

	for {
		renderMenu(os.Stdout, &state)

		if !scanner.Scan() {
			fmt.Println()
			fmt.Println("draining connections... bye")
			return nil // EOF → quit
		}
		input := strings.TrimSpace(scanner.Text())

		action, err := parseInput(input, len(state.rows), state.lastAction)
		if err != nil {
			fmt.Println("  ", err)
			continue
		}

		switch action.kind {
		case actionQuit:
			fmt.Println("draining connections... bye")
			return nil
		case actionRescan:
			state.rows = rescanRows(sess.Cfg.ScenariosDir, state.rows)
			continue
		case actionPickOne, actionPickAll, actionPickFailed:
			runAction(ctx, sess, &state, action)
			if err := writeInteractiveReports(sess); err != nil {
				return err
			}
			state.lastAction = action
		case actionRepeat, actionNone:
			// parseInput collapses repeat → the prior action's kind,
			// and rejects actionNone before this switch ever sees it.
			// Defensive default so the exhaustive linter is satisfied.
			continue
		}
	}
}

// initMenuState builds the initial menu state by loading each
// scenario's name from disk for display. Parse errors at this stage
// are non-fatal — the row keeps the path as a fallback name + a
// reason hint, so the operator can still pick it (e.g. to see the
// parse error inline) or `r` rescan after fixing.
func initMenuState(root string, files []string) menuState {
	rows := make([]scenarioRow, 0, len(files))
	for _, f := range files {
		row := scenarioRow{path: f, relPath: deriveRelPath(root, f), name: deriveDisplayName(f)}
		if item, err := scenario.LoadFile(f); err == nil {
			if s, ok := item.(*scenario.Scenario); ok && s.Name != "" {
				row.name = s.Name
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	return menuState{rows: rows, lastAction: menuAction{kind: actionNone}}
}

// rescanRows walks the scenarios directory again and produces a new
// row list. Existing rows whose paths are still present keep their
// status annotations; new rows start at statusNotRun. Removed rows
// are dropped silently.
func rescanRows(dir string, prev []scenarioRow) []scenarioRow {
	files, err := findScenarios(dir)
	if err != nil {
		fmt.Println("   rescan failed:", err)
		return prev
	}
	prevByPath := make(map[string]scenarioRow, len(prev))
	for _, r := range prev {
		prevByPath[r.path] = r
	}
	rows := make([]scenarioRow, 0, len(files))
	for _, f := range files {
		if old, ok := prevByPath[f]; ok {
			rows = append(rows, old)
			continue
		}
		row := scenarioRow{path: f, relPath: deriveRelPath(dir, f), name: deriveDisplayName(f)}
		if item, err := scenario.LoadFile(f); err == nil {
			if s, ok := item.(*scenario.Scenario); ok && s.Name != "" {
				row.name = s.Name
			}
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].name < rows[j].name })
	return rows
}

// parseInput maps a single trimmed line to a menu action. last is
// consulted only for the empty-ENTER repeat case. n is the current
// row count (for bounds checking).
func parseInput(input string, n int, last menuAction) (menuAction, error) {
	if input == "" {
		if last.kind == actionNone {
			return menuAction{}, fmt.Errorf("no last action to repeat — pick a scenario or type 'a', 'f', 'r', 'q'")
		}
		return last, nil
	}
	switch strings.ToLower(input) {
	case "a", "all":
		return menuAction{kind: actionPickAll}, nil
	case "f", "failed":
		return menuAction{kind: actionPickFailed}, nil
	case "r", "rescan":
		return menuAction{kind: actionRescan}, nil
	case "q", "quit", "exit":
		return menuAction{kind: actionQuit}, nil
	}
	idx, err := strconv.Atoi(input)
	if err != nil {
		return menuAction{}, fmt.Errorf("unrecognised input %q — pick [1-%d], 'a', 'f', 'r', or 'q'", input, n)
	}
	if idx < 1 || idx > n {
		return menuAction{}, fmt.Errorf("out of range — pick [1-%d]", n)
	}
	return menuAction{kind: actionPickOne, idx: idx}, nil
}

// runAction dispatches a pick into the per-scenario execution path.
// All three pick kinds reuse the same inner runRowByPath so the
// YAML-reload-per-iteration property holds uniformly.
func runAction(ctx context.Context, sess *session, state *menuState, a menuAction) {
	switch a.kind {
	case actionPickOne:
		if a.idx < 1 || a.idx > len(state.rows) {
			fmt.Println("   row no longer exists; try 'r' to rescan")
			return
		}
		runRow(ctx, sess, &state.rows[a.idx-1])
	case actionPickAll:
		for i := range state.rows {
			runRow(ctx, sess, &state.rows[i])
		}
	case actionPickFailed:
		failed := []int{}
		for i := range state.rows {
			if state.rows[i].status == statusFail {
				failed = append(failed, i)
			}
		}
		if len(failed) == 0 {
			fmt.Println("   no failed scenarios in current state — all clear")
			return
		}
		for _, i := range failed {
			runRow(ctx, sess, &state.rows[i])
		}
	case actionRescan, actionQuit, actionRepeat, actionNone:
		// runAction is only ever called by the loop after parseInput
		// has resolved the action to a pick-kind; defensive default
		// for the exhaustive linter.
	}
}

// runRow executes one scenario. Reloads YAML from disk every call so
// editor saves are picked up. On parse error, the row records the
// reason and stays in the menu (no crash).
func runRow(ctx context.Context, sess *session, row *scenarioRow) {
	fmt.Printf("[%s]... ", row.name)
	row.status = statusRunning

	start := time.Now()
	item, err := scenario.LoadFile(row.path)
	if err != nil {
		row.status = statusFail
		row.duration = time.Since(start)
		row.reason = "parse error: " + err.Error()
		fmt.Printf("✗ parse error (%s)\n   %s\n", fmtDuration(row.duration), err)
		return
	}
	s, ok := item.(*scenario.Scenario)
	if !ok {
		row.status = statusFail
		row.duration = time.Since(start)
		row.reason = fmt.Sprintf("unknown scenario type %T", item)
		fmt.Printf("✗ unknown type (%s)\n", fmtDuration(row.duration))
		return
	}
	if s.Name != "" {
		row.name = s.Name
	}

	// Replace-in-place: drop any prior rows for this scenario so the
	// report carries exactly one entry per scenario (mirroring the
	// performance.json per-test "latest" model). recordCase will
	// append fresh rows below; if the re-run produced a failure, the
	// new rows carry the new Reason verbatim.
	removeScenarioRows(sess.Report, s.Name)

	if err := runScenario(ctx, s, sess.Deps); err != nil {
		row.status = statusFail
		row.duration = time.Since(start)
		row.reason = "runtime: " + err.Error()
		fmt.Printf("✗ runtime error (%s)\n   %s\n", fmtDuration(row.duration), err)
		return
	}
	row.duration = time.Since(start)

	// Read pass/fail from the appended case-report rows. runScenario
	// always appends; the most recent N rows describe THIS scenario's
	// cases. A scenario "passes" iff every appended case verdict is
	// "pass". Reason is the first failing case's reason.
	pass, reason := summariseLastScenarioResult(sess.Report, s)
	if pass {
		row.status = statusPass
		row.reason = ""
		fmt.Printf("✓ PASS (%s)\n", fmtDuration(row.duration))
		return
	}
	row.status = statusFail
	row.reason = reason
	fmt.Printf("✗ FAIL (%s)\n", fmtDuration(row.duration))
	if reason != "" {
		fmt.Printf("   %s\n", abridge(reason, 200))
	}
	if sess.Cfg.InteractiveOutputPath != "" {
		fmt.Printf("   full report → %s\n", sess.Cfg.InteractiveOutputPath)
	}
}

// summariseLastScenarioResult walks the report's Cases tail to find
// the rows belonging to the most-recently-executed scenario. The
// per-case rows already carry "pass"/"fail" verdicts; the scenario
// passes iff all its cases pass. The reason is the first failing
// case's joined-failure reason.
func summariseLastScenarioResult(report *RunReport, s *scenario.Scenario) (bool, string) {
	if report == nil || len(report.Cases) == 0 {
		return false, "no case rows recorded"
	}
	// Find the contiguous tail block whose Scenario field matches s.Name.
	// Per recordCase, every case row carries the scenario name.
	tail := []int{}
	for i := len(report.Cases) - 1; i >= 0; i-- {
		if report.Cases[i].ScenarioName != s.Name {
			break
		}
		tail = append([]int{i}, tail...)
	}
	if len(tail) == 0 {
		return false, "no case rows for this scenario"
	}
	for _, i := range tail {
		if report.Cases[i].Verdict.Outcome != "pass" {
			return false, report.Cases[i].Verdict.Reason
		}
	}
	return true, ""
}

// renderMenu draws the numbered scenario list + status column +
// prompt. Width-aware truncation keeps long scenario names from
// breaking the column.
func renderMenu(w io.Writer, state *menuState) {
	fmt.Fprintln(w)
	fmt.Fprintf(w, "scenarios (%d):\n", len(state.rows))
	for i := range state.rows {
		// Two columns after the index: scenario name + relative file
		// path. Path makes subdirectory location visible (plan-ahead
		// §2.8) and lets the operator `vi` straight to the file from
		// the menu.
		row := &state.rows[i]
		fmt.Fprintf(w, "  [%2d]  %-44s  %-40s  %s\n",
			i+1, truncate(row.name, 44), truncate(row.relPath, 40), formatStatus(row))
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "▶ pick [1-%d] | a=all | f=failed | r=rescan | q=quit%s : ",
		len(state.rows), repeatHint(state.lastAction))
}

// repeatHint adds the " | <enter>=[X]" suffix when there is a
// previous action to repeat.
func repeatHint(last menuAction) string {
	switch last.kind {
	case actionPickOne:
		return fmt.Sprintf(" | <enter>=[%d]", last.idx)
	case actionPickAll:
		return " | <enter>=[a]"
	case actionPickFailed:
		return " | <enter>=[f]"
	default:
		return ""
	}
}

func formatStatus(row *scenarioRow) string {
	switch row.status {
	case statusNotRun:
		return "—"
	case statusRunning:
		return "↻"
	case statusPass:
		return fmt.Sprintf("✓ %s", fmtDuration(row.duration))
	case statusFail:
		return fmt.Sprintf("✗ %s", fmtDuration(row.duration))
	}
	return ""
}

func fmtDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

func abridge(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// removeScenarioRows filters report.Cases in place to drop every row
// whose ScenarioName matches name. Used by the menu loop before
// re-running a scenario so the report keeps exactly one entry per
// scenario (the latest result). Safe on nil report and on empty
// Cases. Stable order — surviving rows keep their relative order.
func removeScenarioRows(report *RunReport, scenarioName string) {
	if report == nil || len(report.Cases) == 0 {
		return
	}
	kept := report.Cases[:0]
	for i := range report.Cases {
		if report.Cases[i].ScenarioName != scenarioName {
			kept = append(kept, report.Cases[i])
		}
	}
	// Zero out the tail so the underlying slice doesn't retain
	// references to removed CaseReport fields (which can hold
	// non-trivial Reason strings).
	for i := len(kept); i < len(report.Cases); i++ {
		report.Cases[i] = CaseReport{}
	}
	report.Cases = kept
}

// deriveRelPath returns the file path relative to root for menu
// display. Used to surface subdirectory location (plan-ahead §2.8 —
// arbitrary nesting under scenarios/drafts/). Falls back to the
// basename when the relative computation would escape root, so the
// row still has something useful to show.
func deriveRelPath(root, path string) string {
	if root == "" {
		return filepath.Base(path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return filepath.Base(path)
	}
	return rel
}

// deriveDisplayName falls back to the file's basename (no extension)
// when the YAML can't be parsed yet to extract scenario.Name.
func deriveDisplayName(path string) string {
	base := path
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, ".yaml"); i > 0 {
		base = base[:i]
	}
	return base
}
