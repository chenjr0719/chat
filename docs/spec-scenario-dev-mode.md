# Spec: Scenario Development Mode

**Status:** Draft for review (no code yet)
**Phase:** 4.6 — Author Iteration Velocity (replaces the rolled-back SCENARIO_TARGET design)
**Spec date:** 2026-06

## 1. Motivation

Today's runner is batch-shaped: every `make run` invocation walks all
11 scenarios, writes `last-run.md`, and exits. For an author iterating
on ONE scenario the per-iteration cost is ~15 seconds — Go compile +
docker checks + full sweep — when 200ms of focused execution would
suffice. The 75× iteration-velocity penalty discourages tight feedback
loops.

The earlier `SCENARIO_TARGET` design (rolled back at commit `857a7d0`)
attempted to fix this with a per-invocation env var pointing at a
single YAML file. The design worked but had two structural issues:

1. **Process spin-up cost.** Each `make single TARGET=…` invocation
   recompiled the Go binary (~200-500ms), opened fresh
   Mongo/NATS/Cassandra connections, and re-loaded the catalog.
   The fundamental author latency was ~1.5s — better than 15s, but
   not the ~200ms a warm long-lived process could deliver.
2. **No way to iterate across scenarios.** TARGET was locked at
   process startup. Switching scenarios meant a fresh invocation.

This spec replaces that design with a **long-lived interactive
process** driven by a stdin menu. Connections stay warm across
iterations; the Go binary compiles once; the menu lets you pick any
scenario at any time.

Expected per-pick latency: ~150-400ms (Sandbox.Setup + execution +
report write). The author edits YAML in their editor, hits a
single digit + ENTER, sees the result inline.

## 2. The two modes

Single env var: `INTERACTIVE`. Two clean opposites.

| `INTERACTIVE` | Behavior |
|---|---|
| Unset / `false` (default) | Today's batch sweep — runs all scenarios, exits. **Zero change for everyone.** CI flows untouched. |
| `true` | Interactive menu opens immediately. **Nothing runs until you pick.** Connections stay warm. Quit via `q` or Ctrl+C. |

No TTY autodetection. No `SKIP_SWEEP` combinatorics. Pure opt-in.

**Rationale for opt-in default:**

- Preserves today's muscle memory and CI flow with mathematical
  certainty — if `INTERACTIVE` is never set, behavior is
  bit-identical.
- Single env var to add when you want the menu; no flag wars; no
  surprise behavior on `make run` for anyone.
- TTY-detection (the "smart default" pattern) was considered and
  rejected: it has edge cases (tmux, IDE-integrated terminals,
  weird CI runners with pseudo-TTYs) that are easier to debug when
  the contract is "explicit env var or nothing."

## 3. User-facing surface

### 3.1 Invocation

```sh
INTERACTIVE=true make -C tools/integration-suite run
```

Same Makefile target as today (`run` → `local` alias is preserved).
The env var is the only knob.

### 3.2 Menu render

```
$ INTERACTIVE=true make -C tools/integration-suite run
loaded catalog: 2 verbs, 5 readers, 2 services
opened mongo / nats / cassandra connections (~200ms)
scenarios in scenarios/drafts/ (11):

  [1]  create-room-sandbox                          —
  [2]  broadcast-worker-fans-out-room-event         —
  [3]  channel-name-too-long-rejected               —
  [4]  empty-create-request-rejected                —
  [5]  history-service-paginates-messages           —
  [6]  message-gatekeeper-rejects-unsubscribed-…    —
  [7]  message-send-persists-canonical-and-…        —
  [8]  room-service-create-room-persists-via-…      —
  [9]  room-worker-persists-canonical-create        —
 [10]  room-worker-rejects-missing-valkey-key       —
 [11]  verified-user-creates-channel-room           —

▶ pick [1-11] | a=all | f=failed | r=rescan | q=quit : _
```

### 3.3 Action vocabulary

| Input | Action |
|---|---|
| `1` … `N` | Reload that scenario's YAML from disk; run it; update result column; redisplay menu. |
| `a` | Run all listed scenarios in sequence; update all results. |
| `f` | Run only scenarios currently showing `✗`. No-op with friendly message if none failed. |
| `r` | Re-scan `scenarios/drafts/` directory. Pick up new YAML files; drop removed ones; reset annotations to `—` for new entries. |
| `q` | Drain Mongo / NATS / Cassandra connections; exit 0. |
| Ctrl+C | Same as `q`. |
| `<empty ENTER>` | Repeat last action. Prompt suffix shows what — `[5]`, `[a]`, `[f]`. Initial prompt has no `<enter>` hint until first pick. |

### 3.4 Status indicators (per scenario row)

| Glyph | Meaning |
|---|---|
| `—` | Not yet run this session |
| `↻` | Currently running |
| `✓ <duration>` | Passed (e.g. `✓ 198ms`) |
| `✗ <duration>` | Failed (e.g. `✗ 5.0s`) |

### 3.5 Inline result format

```
▶ pick [1-11] | a=all | f=failed | r=rescan | q=quit : 5
[5] history-service-paginates-messages... ↻
[5] history-service-paginates-messages... ✗ FAIL (5.0s)
   closest mismatch: matches_shape: field "body_json.messages":
   expected element [0] not found at or after observed[0]
   full report → docs/integration-suite/last-run.md
```

The abridged reason is the matcher's `bestReason` (already captured by
the existing diagnostic chain). The full report is always written to
`last-run.md` for deeper inspection.

### 3.6 Invalid YAML on reload

If the user is mid-edit and the YAML doesn't parse:

```
[5] history-service-paginates-messages... ✗ parse error
   scenario.LoadFile: unmarshal: yaml: line 47: did not find expected key
   (fix YAML and pick again; menu state preserved)
```

The menu does NOT crash. The previous result annotation for that
scenario is preserved. The user fixes the YAML and re-picks.

### 3.7 Deleted file detection

If a scenario file is deleted between `r` rescans and the user picks
its number, the pick errors gracefully:

```
[7] message-send-persists-...: file no longer exists; try 'r' to rescan
```

## 4. Architecture

### 4.1 Lifecycle diagram

```
main.go:
  read INTERACTIVE env
  build rt.Config (existing path)
  call rt.Run(ctx, cfg)
  exit with returned code

rt.Run becomes:
  buildSession(cfg)               ← NEW: extract connection setup
                                    + catalog load + registries
  findScenarios(cfg.ScenariosDir) (unchanged)
  if cfg.Interactive:
      runMenuLoop(session, scenarios)   ← NEW
  else:
      runSweep(session, scenarios)      ← extracted from current Run body
      writeReports(session, report)
  drainSession(session)
  return exit code

runMenuLoop:
  renderMenu(session, scenarios)
  for each line from stdin:
      action = parseInput(line, lastAction)
      switch action:
          case picks: runScenarios(session, picks); writeReports; render
          case quit:  return
          case rescan: rescanDir; render
          case invalid: render with hint
```

### 4.2 Session abstraction

```go
// Session holds everything the runner builds once at startup and
// reuses across both the batch sweep and the interactive menu loop.
// Closing the session drains every connection.
type Session struct {
    Catalog       *catalog.Catalog
    MongoClient   *mongo.Client
    AdminConn     *nats.Conn       // nil-tolerant
    Cassandra     *gocql.Session   // nil-tolerant
    Dispatcher    *Dispatcher
    SeedEffectReg *seedeffect.Registry
    MatcherReg    *matchers.Registry
    Perf          *PerformanceStore
    Report        *RunReport
    Cfg           *Config
    // … all the per-scenario dependencies that runV3Scenario consumes
}

func buildSession(ctx context.Context, cfg *Config) (*Session, error) {
    // existing Run() body, lines 80-200ish, refactored to return *Session
}

func (s *Session) Drain(ctx context.Context) {
    // existing defer chain (mongo Disconnect, nats Drain, cassandra Close)
}
```

The existing `runV3Scenario(ctx, s *scenario.ScenarioV3, deps *v3Deps)`
function is the per-scenario unit of work. Both `runSweep` and the
menu loop call it.

### 4.3 Menu loop — `internal/runtime/menu.go` (new)

```go
type menuState struct {
    scenarios   []scenarioRow
    lastAction  menuAction
    rerender    bool
}

type scenarioRow struct {
    path       string
    name       string         // from scenario.Name
    status     rowStatus      // notRun, running, pass, fail
    duration   time.Duration
    reason     string         // abridged failure reason
}

type rowStatus int
const (
    statusNotRun rowStatus = iota
    statusRunning
    statusPass
    statusFail
)

type menuAction struct {
    kind menuActionKind        // pickOne, pickAll, pickFailed, rescan, quit, repeat
    idx  int                   // for pickOne (1-based)
}

func runMenuLoop(ctx context.Context, sess *Session, files []string) error {
    state := initState(sess, files)
    scanner := bufio.NewScanner(os.Stdin)
    for {
        render(state)
        if !scanner.Scan() {
            return nil   // EOF (stdin closed, e.g. Ctrl+D) → quit
        }
        action, err := parseInput(scanner.Text(), state.lastAction)
        if err != nil {
            fmt.Println(" ", err)
            continue
        }
        if action.kind == actionQuit {
            return nil
        }
        if action.kind == actionRescan {
            state.scenarios = rescan(sess.Cfg.ScenariosDir, state.scenarios)
            continue
        }
        runActions(ctx, sess, &state, action)
        writeReports(sess)
        state.lastAction = action
    }
}
```

~150 LOC for the full menu surface including `render`, `parseInput`,
`runActions`, `rescan`, and the `scenarioRow` lifecycle.

### 4.4 Per-iteration YAML reload

Inside `runActions`, before each scenario fires:

```go
loaded, err := scenario.LoadFile(row.path)
if err != nil {
    row.status = statusFail
    row.reason = "parse error: " + err.Error()
    return
}
s := loaded.(*scenario.ScenarioV3)
// … existing runV3Scenario(ctx, s, deps) path
```

This is the property that catches editor saves — the file is re-read
every time, not cached.

### 4.5 Reports

Every `runActions` invocation updates `sess.Report` and writes
`last-run.md` (and `last-run-approved.md` when applicable). After a
single pick the report has one row; after `a` it has all 11.

Consistent with how today's runner overwrites `last-run.md` per
invocation.

## 5. Test plan

### 5.1 Pure-Go unit tests for `parseInput`

Table-driven test in `internal/runtime/menu_test.go`:

| Input | Last action | Expected parsed action |
|---|---|---|
| `"1"` | nil | `pickOne(1)` |
| `"11"` | nil | `pickOne(11)` |
| `"a"` | nil | `pickAll` |
| `"f"` | nil | `pickFailed` |
| `"r"` | nil | `rescan` |
| `"q"` | nil | `quit` |
| `""` (empty ENTER) | `pickOne(5)` | `pickOne(5)` (repeat) |
| `""` | `pickAll` | `pickAll` (repeat) |
| `""` | nil (no prior action) | error: "no last action to repeat" |
| `"0"` | nil | error: "out of range [1-N]" |
| `"99"` | nil (with 11 scenarios) | error: "out of range [1-11]" |
| `"xyz"` | nil | error: "unrecognised input" |

~12 tests, ~80 LOC.

### 5.2 State-transition tests

Driven via piped stdin into the menu loop, with a `Session` built
against in-memory fakes (no real Mongo/NATS/Cassandra). Two tests:

- **Sequence: `1`, `5`, `<enter>`, `q`** → asserts the in-memory state
  reflects three picks (`1` once, `5` twice via repeat), then exits.
- **Sequence: `f`, `q` with no failures** → asserts the friendly "no
  failed scenarios" message is printed and the menu re-renders.

These use `io.Pipe` to feed the loop and a fake `Session` whose
`runScenarioByPath` records calls.

### 5.3 Integration smoke (Docker-tagged, optional)

One Docker-tagged test that:

1. Builds a real `Session` against testcontainers stack.
2. Feeds the menu loop a script via stdin: `1`, `q`.
3. Asserts `last-run.md` contains exactly one scenario's row.

This is mostly a sanity check that the connection lifecycle holds
across menu iterations. Skip if Docker not available.

### 5.4 Coverage target

95%+ on `internal/runtime/menu.go`. The `parseInput` + state-machine
surface is pure-Go and trivially testable; only the gocql/mongo/nats
binding layer (which is already covered by existing integration tests
on `Session`-equivalent state) sits below the coverage line.

## 6. CI safety

| Surface | Pre-change | Post-change |
|---|---|---|
| `make run` (no env override) | Full sweep, exit | Full sweep, exit (bit-identical) |
| `make local` | Same | Same |
| `USE_INFRA=true make local` | Same | Same |
| CI workflow invoking `make local` | Sweep + exit | Sweep + exit (bit-identical) |
| `INTERACTIVE=true make run` | Did not exist | Menu opens; waits for stdin |

**No CI workflow change is required.** The `INTERACTIVE` env var is
purely opt-in; CI never sets it; CI behavior is mathematically
guaranteed to be unchanged.

If a future operator pipes `make run`'s output to a file from an
interactive shell AND has `INTERACTIVE=true` exported in their
session, the menu will hang waiting for stdin that's been redirected
elsewhere. This is documented as a gotcha in the runbook:
"`INTERACTIVE=true` requires a real stdin; don't combine with output
redirects."

## 7. Backward compatibility

- `rt.Config` gains one new field: `Interactive bool`. Defaults to
  `false` — preserves every existing call site.
- `rt.Run` extracts `buildSession()` + `runSweep()` as internal
  helpers. The public function signature is unchanged.
- `internal/runtime/menu.go` is a new file; no existing imports affected.
- `cmd/runner/main.go` adds one env read (`INTERACTIVE`) and threads
  it into `cfg.Interactive`. Zero other changes.
- Scenarios, sandbox, primitives, readers, matchers, reporters — all
  untouched.
- The 11 currently-drafted scenarios run bit-identically whether
  invoked from sweep or menu — the per-scenario unit of work
  (`runV3Scenario`) is the same function in both paths.

## 8. Open questions for review

| # | Question | Default if no input |
|---|---|---|
| Q1 | Should `<empty ENTER>` work BEFORE any action has been picked? | **No** — error with "no last action to repeat." First input must be explicit. |
| Q2 | Should the menu support multi-pick (e.g. `1,3,5` or `1-3`)? | **No (v1)** — single pick, `a`, or `f` cover the use cases. Add if a real workflow needs it. |
| Q3 | Should the menu show color (green ✓, red ✗)? | **Defer** — single-char glyphs + reason text are sufficient. Add ANSI color in a follow-up if the b/w version feels flat. |
| Q4 | Should the menu show per-scenario `last`/`best`/`worst` durations from `performance.json`? | **No (v1)** — keep the menu focused on current session. Cross-session perf is what `performance.json` itself is for. |
| Q5 | Should `q` prompt for confirmation? | **No** — quit means quit; Ctrl+C is also quit. |
| Q6 | Should the menu support a `name <substring>` filter to run scenarios matching a prefix? | **Defer** — `r` + numbered list cover discoverability; substring search is a v2 nicety. |
| Q7 | Should the menu accept `1 5 11` (space-separated) as multi-pick? | **No (v1)** — same answer as Q2. |
| Q8 | Should the runbook get a new section, or just a gotcha row? | **New section** — "Interactive scenario dev loop" sits alongside the existing "Run the suite" section. |

## 9. Non-goals (explicit)

- **TTY autodetect.** Explicit env var or nothing.
- **File-watch / fsnotify.** Trigger is explicit ENTER, not autosave.
- **Persistent session state across `make run` invocations.** Each
  process is a fresh session; `—` annotations on startup.
- **Multi-pick / batched picks.** Single pick or `a`/`f` in v1.
- **Color output.** Single-char glyphs sufficient.
- **CLI flags.** Env var only — consistent with the rest of the
  runner config pattern.
- **`scenarios/approved/` filtering inside the menu.** Use
  `SCENARIOS_DIR=scenarios/approved make run INTERACTIVE=true` to
  scope the menu to the CI-gating subset.
- **Spec coverage of `INTERACTIVE=true` + `USE_INFRA=true`
  combination.** Both work; nothing special required; document as a
  one-liner.

## 10. Net change estimate

| Layer | Files | Lines (approx) |
|---|---|---|
| Config | `internal/runtime/runner.go` (add `Interactive` field) + `cmd/runner/main.go` (env read) | ~12 |
| Refactor | `internal/runtime/runner.go` (extract `buildSession`, `runSweep`, `drainSession`) | ~30 |
| Menu loop | `internal/runtime/menu.go` (new) | ~150 |
| Tests | `internal/runtime/menu_test.go` (new) | ~120 |
| Runbook | `tools/integration-suite/RUNBOOK.md` (new section) | ~25 |
| Sync register | `docs/integration-suite-sync-register.md` (one-line entry in §4.10) | ~3 |

Total: ~340 lines added; zero deletions; zero new dependencies.

## 11. Recommended landing path

1. **Land this spec** (commit + push).
2. **Single PR — the menu + refactor:**
   - Refactor `rt.Run` into `buildSession` + `runSweep` + `drainSession`.
   - Add `internal/runtime/menu.go` with the loop + action parser.
   - Add `cfg.Interactive` field; thread into `cmd/runner/main.go`.
   - Add `menu_test.go` covering parseInput + state transitions.
   - Update `RUNBOOK.md` with the new "Interactive scenario dev loop"
     section + gotcha row about output redirects.
   - Update `docs/integration-suite-sync-register.md` §4.10 with the
     new env var.

PR reviewable in one sitting; ~340 LOC, single new file, no
architectural surface change to the existing primitive matrix.

---

**Approval needed before any code lands.** Specifically:

- §2 the two-mode design + opt-in default (vs. TTY autodetect).
- §3.3 the action vocabulary (`1-N | a | f | r | q | <enter>`).
- §4.2 the `Session` abstraction extraction from `rt.Run`.
- §7 backward-compat guarantee (CI bit-identical).
- §8 the eight open questions — particularly Q1 (empty ENTER before
  first action) and Q3 (color).
- §11 single-PR sequencing (no separate refactor PR).
