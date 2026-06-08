# Spec: Single-Scenario Targeted Execution

**Status:** Draft for review (no code yet)
**Phase:** 4.6 — Author Iteration Velocity
**Spec date:** 2026-06

## 1. Motivation

Today's authoring loop runs every scenario in `scenarios/drafts/`
end-to-end on each invocation. For an author iterating on a single
scenario the cost is ~15 seconds (11 scenarios × ~1-5s each) when
0.5-2s of focused execution would suffice. The 7-30× iteration-velocity
penalty discourages tight feedback loops.

`Sandbox.Setup` already drops Mongo collections, truncates Cassandra
tables, and resets the chaos engine at every scenario boundary
(`sandbox.go:204-214`). The infrastructure-reuse safety story is
trivial: per-scenario reset is the existing invariant. The remaining
gap is **runner-side execution path isolation** — currently the
runner has no surface for "execute exactly one scenario."

This phase introduces a `SCENARIO_TARGET` environment variable and a
`make single TARGET=…` Makefile target. The two-line author loop
becomes:

```sh
# edit scenarios/drafts/history-service-paginates-messages.yaml
make -C tools/integration-suite single TARGET=scenarios/drafts/history-service-paginates-messages.yaml
# read docs/integration-suite/last-run.md
```

Expected target run latency: <2s after the first invocation has warmed
the existing manual stack. The full-sweep `make local` flow is
preserved unchanged for CI and regression sweeps.

## 2. User-facing surface

### 2.1 The `make single TARGET=…` invocation

```sh
make -C tools/integration-suite single TARGET=scenarios/drafts/foo.yaml
```

- `TARGET` is a path to a single `.yaml` scenario file.
- The Makefile target wraps the same env-var bootstrap as `make local`,
  with one addition: `SCENARIO_TARGET=$(TARGET)` set into the runner's
  environment.
- The target depends on the same `preflight + preflight-containers`
  prerequisites as `make local` — the manual stack must be up
  (operator runs `make deps-up && make up` once per session).
- On successful invocation, the Makefile emits a single confirmation
  line to stdout naming the resolved target path so the operator sees
  exactly what's about to run.

### 2.2 Path resolution rules

The Makefile `cd`s into `$(TOOL_DIR)` (= `tools/integration-suite/`)
before invoking `go run ./cmd/runner`, so the runner's working
directory at parse time is the suite root. `SCENARIO_TARGET` values
are interpreted as follows:

| Value form | Resolution |
|---|---|
| Relative path (e.g. `scenarios/drafts/foo.yaml`) | Relative to `tools/integration-suite/` |
| Absolute path (e.g. `/home/user/chat/tools/integration-suite/scenarios/drafts/foo.yaml`) | Used verbatim |
| Empty / unset | Fall back to default full-directory sweep over `cfg.ScenariosDir` |

This is documented in a single comment in `cmd/runner/main.go` next to
the env-var lookup so the next author isn't surprised. No glob/pattern
support in v1 (§9 open question — defer until needed).

### 2.3 CLI-flag vs env-var design choice

`SCENARIO_TARGET` joins the existing env-var-based runner config
pattern (`AUTH_SERVICE_URL`, `NATS_URL`, `SCENARIOS_DIR`, etc. — see
`cmd/runner/main.go:33-50`). No CLI flags exist today. Introducing a
flag surface for one new knob would split the runner's config-input
model in half. The env-var approach extends the established pattern
at zero new infrastructure cost.

## 3. Configuration architecture

### 3.1 `rt.Config` addition

Add one field to `internal/runtime/runner.go:34` (the `Config` struct):

```go
// ScenarioTarget, when non-empty, restricts execution to exactly
// one scenario file. Bypasses the ScenariosDir walk. Path is
// resolved relative to the runner's CWD (which is
// tools/integration-suite/ when invoked via `make single`).
// Empty falls back to the full-directory sweep over ScenariosDir.
// See docs/spec-single-scenario-execution.md.
ScenarioTarget string
```

Population in `cmd/runner/main.go` (alongside the other env reads at
line 45):

```go
ScenarioTarget: env("SCENARIO_TARGET", ""),
```

Zero rename, zero reordering of existing fields. Backward-compat
guaranteed by the empty-string default.

### 3.2 Backward compatibility — explicit guarantee

| Surface | Pre-4.6 behavior | Post-4.6 behavior | Risk |
|---|---|---|---|
| `make local` (CI flow) | walks `cfg.ScenariosDir` | walks `cfg.ScenariosDir` (`SCENARIO_TARGET` never set) | None |
| `make local` with `USE_INFRA=true` | walks + boots own stack | unchanged | None |
| Direct `go run ./cmd/runner` without env override | walks `cfg.ScenariosDir` | unchanged | None |
| `make validate` (validator command) | independent path (`cmd/validator/main.go`) | untouched | None |
| `make single TARGET=…` | did not exist | new path | new surface — opt-in |

The empty-string fallback is the load-bearing backward-compat
mechanism. Any operator or CI flow that doesn't set `SCENARIO_TARGET`
sees zero behavior change.

## 4. Loader isolation — validation discipline

### 4.1 Current behavior

`internal/runtime/runner.go:166` calls `findScenarios(cfg.ScenariosDir)`
which walks the directory and returns every `.yaml` path. The runner
then iterates those paths via `LoadFile` per scenario; if ANY scenario
parses cleanly, that one runs. Parse errors on sibling files are
captured into the run report but don't block other scenarios.

This is the correct multi-scenario behavior — but it means a
half-written sibling draft in `scenarios/drafts/` shows up in
`last-run.md` as a noisy "failed to parse" entry every time the author
iterates on a different scenario.

### 4.2 New behavior — isolated targeted load

When `cfg.ScenarioTarget != ""`:

1. **Skip** `findScenarios(cfg.ScenariosDir)` entirely.
2. **Run** `os.Stat(cfg.ScenarioTarget)` — see §5 for failure handling.
3. **Return** `[]string{cfg.ScenarioTarget}` as the sole scenario file.
4. **Proceed** to the standard per-file `LoadFile` + execute loop —
   the rest of the runner is untouched.

The catalog validation step (`cat.Load(cfg.CatalogsDir)` at
`cmd/runner/main.go` / `runner.go`) is **NOT skipped**. Catalog
loading is cheap (milliseconds) and catches drift in primitive /
verb / service definitions that the targeted scenario may depend on.
The skip is exclusively for sibling-scenario parse pollution.

### 4.3 Recommended code shape

In `runner.go` (proposed sketch, not yet written):

```go
// runner.go around line 166
var scenarioFiles []string
if cfg.ScenarioTarget != "" {
    if _, err := os.Stat(cfg.ScenarioTarget); err != nil {
        return nil, fmt.Errorf(
            "SCENARIO_TARGET file %q not found: %w",
            cfg.ScenarioTarget, err)
    }
    scenarioFiles = []string{cfg.ScenarioTarget}
    slog.Info("SCENARIO_TARGET active; bypassing directory sweep",
        "target", cfg.ScenarioTarget)
} else {
    scenarioFiles, err = findScenarios(cfg.ScenariosDir)
    if err != nil {
        return nil, fmt.Errorf("find scenarios: %w", err)
    }
}
```

The `slog.Info` line gives the operator visible confirmation in stdout
that the run is targeted. Pair with the Makefile's `@echo` so the
confirmation surfaces twice — once before `go run` (Makefile), once
inside the runner (slog).

## 5. Failure framework

### 5.1 Hard-fail on missing file

A typo'd `TARGET` in the author's iteration loop is the most likely
failure path. Silent fallback ("0 scenarios loaded, run passed") is
the worst possible behavior — the operator gets a green badge for no
work done. **The targeted path must hard-fail loudly.**

Mechanism: `os.Stat(cfg.ScenarioTarget)` at runner startup. Three
distinct error surfaces:

| Stat result | Error wording (proposed) |
|---|---|
| `os.ErrNotExist` | `SCENARIO_TARGET file "<path>" not found: stat <path>: no such file or directory` |
| `os.ErrPermission` | `SCENARIO_TARGET file "<path>" not readable: <perm error>` |
| File exists but is a directory | `SCENARIO_TARGET "<path>" is a directory, not a scenario file` |

The runner returns the error from `rt.Run(...)`; `cmd/runner/main.go`
already logs run failures via `slog.Error("run failed", "err", err)`
and exits with code 2 (`main.go:93-95`). No new error-handling surface
needed.

### 5.2 What `os.Stat` does NOT cover

- Malformed YAML in the targeted file → caught by `LoadFile` further
  down the pipeline; surfaces in the per-scenario report
- Scenario YAML missing required fields (`scenario:`, `source:`,
  `cases:`) → caught by `loadV3` at `scenario/loader.go:50-83`
- Unknown primitive locations → caught at assertion-execution time

These are all the existing scenario-load surfaces — `SCENARIO_TARGET`
introduces no new error classes for them; only the "file isn't there"
case is new.

### 5.3 The Makefile-level echo

In addition to the runner's `slog.Info`, the Makefile's `single`
target should emit one confirmation line:

```makefile
single: preflight preflight-containers
	@if [ -z "$(TARGET)" ]; then \
		echo "make single: TARGET is required (e.g. TARGET=scenarios/drafts/foo.yaml)"; \
		exit 2; \
	fi
	@echo "single → $(TARGET)"
	@cd $(TOOL_DIR) && \
		… (env bootstrap mirroring `local`) \
		SCENARIO_TARGET=$(TARGET) \
		go run ./cmd/runner
```

The early `if` block catches `make single` with no `TARGET=…` BEFORE
`go run` fires, saving the ~1s of process spin-up for the trivial
user-error case.

## 6. Test plan

### 6.1 Unit tests in `internal/runtime/runner_test.go`

Three scenario-loader-discipline tests (or co-located in a new
`runner_loader_test.go` if `runner_test.go` is heavyweight):

| # | Test | Assertion |
|---|---|---|
| 1 | `TestRunner_ScenarioTargetEmpty_FallsBackToDirSweep` | Empty `cfg.ScenarioTarget` produces a non-empty scenario list via `findScenarios` (mocks a 2-file temp dir; asserts len == 2). |
| 2 | `TestRunner_ScenarioTargetSet_ReturnsExactlyThatOne` | Set `cfg.ScenarioTarget` to a temp file; assert the runner picks that one (len == 1, path matches). |
| 3 | `TestRunner_ScenarioTargetMissing_ReturnsHardError` | Set `cfg.ScenarioTarget` to a path that doesn't exist; assert the run returns an error whose message contains `SCENARIO_TARGET file %q not found` and the offending path verbatim. |

These tests should NOT spin up a full sandbox — they can short-circuit
at the scenario-loading layer. The runner's existing test surface
(`runner_v3_test.go`) provides patterns for partial-init testing
(scenario-load-only without dispatching cases).

### 6.2 Path-resolution test (optional, low priority)

If the spec author wants belt-and-suspenders, a fourth test pinning
relative-path resolution:

| # | Test | Assertion |
|---|---|---|
| 4 | `TestRunner_ScenarioTargetRelativePath_ResolvedFromCWD` | Use `t.TempDir()` as cwd via `t.Chdir`; set `cfg.ScenarioTarget = "subdir/foo.yaml"` (relative); assert `os.Stat` resolves against the cwd correctly. |

This test would catch a future refactor that accidentally normalises
the path against a wrong root (e.g., repo root vs tool root).

### 6.3 No new integration tests

The Docker-tagged integration test surface (`*_integration_test.go`)
is unchanged. `SCENARIO_TARGET` is a runner-side knob; it does not
touch the sandbox, primitives, readers, or matchers. Existing
integration tests fully cover the downstream execution path that
runs whether one or N scenarios are loaded.

### 6.4 Verification gates

Per house style:
- `go test -race ./...` whole-repo green
- `make lint` 0 issues
- `make -C tools/integration-suite validate` 11 drafts parsed
  (unchanged — `validate` doesn't consume `SCENARIO_TARGET`)
- Manual smoke: `make single TARGET=scenarios/drafts/empty-create-request-rejected.yaml`
  produces a `last-run.md` with exactly one scenario row

## 7. Operational documentation

### 7.1 RUNBOOK.md updates

Insert under the "Run the suite (the main loop)" section, before the
"Read the results" block:

```markdown
### Focused single-scenario loop (new in 4.6)

For iterating on a single scenario without the full-suite latency:

    make -C tools/integration-suite single TARGET=scenarios/drafts/foo.yaml

- Requires the manual stack (`make deps-up && make up`) — does NOT
  boot its own stack.
- Path is relative to `tools/integration-suite/` (or absolute).
- `last-run.md` will show ONE scenario row — re-run `make local` for
  the full-suite picture.
- Typo'd TARGET → hard fail with `SCENARIO_TARGET file %q not found`.
```

A new bullet in the gotchas table:

```markdown
| **Targeted run shows only ONE scenario** | `make single` writes a single-scenario `last-run.md`. Run `make local` afterward to refresh the full-suite view. The two modes share the same output path. |
```

### 7.2 Inline runner comment

One comment in `cmd/runner/main.go` next to the env-var lookup:

```go
// SCENARIO_TARGET — when set, runs exactly one scenario file
// (path relative to tools/integration-suite/ or absolute).
// Empty falls back to the full-directory sweep over SCENARIOS_DIR.
// See docs/spec-single-scenario-execution.md.
ScenarioTarget: env("SCENARIO_TARGET", ""),
```

### 7.3 Sync register entry

The new env var joins `docs/integration-suite-sync-register.md`
§4.9 (Default deployment values) — but it's not a *mirrored* value
(no upstream source-of-truth), so it goes in a new mini-section
"Runner-only knobs" or as a one-line note. Defer the placement
decision to the maintainer who lands this; the register's §1
discipline ("any new suite hardcode that mirrors an upstream source
MUST appear here") doesn't strictly apply to a runner-only knob.

## 8. Backward compatibility

Fully enumerated in §3.2. Net guarantee: **zero behavior change** when
`SCENARIO_TARGET` is unset. All existing CI flows, manual flows,
scenarios, tests, and documentation paths continue to behave
bit-identically to the pre-4.6 baseline.

## 9. Open questions for review

| # | Question | Default if no input |
|---|---|---|
| Q1 | Should `SCENARIO_TARGET` accept glob patterns (e.g. `scenarios/drafts/history-*.yaml`)? | **No (v1)** — one file at a time. Add when a real use case for "run these 3 scenarios" surfaces. |
| Q2 | Should `make single` also work under `USE_INFRA=true`? | **No** — `USE_INFRA` boots a fresh stack (~72s); the whole point of `single` is fast iteration against a warm manual stack. The two are philosophically opposed. |
| Q3 | Should `SCENARIO_TARGET=scenarios/drafts/` (a directory) trigger a directory walk scoped to that subdir? | **No** — keep one-file semantics. Directory walks are what the default path is for. |
| Q4 | Should the runner refuse to launch if `cfg.ScenariosDir` is also set when `cfg.ScenarioTarget` is set? | **No** — `ScenariosDir` is silently ignored when `ScenarioTarget` is set. Documenting this in the runbook is sufficient. |
| Q5 | Should we capture the SCENARIO_TARGET value in the run report metadata? | **Yes** — append to `RunReport.Metadata` (or equivalent) so the report carries the run's scope. Cheap, useful for triage. |
| Q6 | Should `make single` echo the resolved absolute path (after `cd $(TOOL_DIR)`) for unambiguous operator confirmation? | **Yes** — the Makefile already has `$(abspath)` available; `@echo "single → $(abspath $(TARGET))"` is one line. |

## 10. Non-goals (explicit)

- **Multi-scenario subset selection.** Use the default directory walk.
- **Scenario-filter expressions** (tags, regex, `@status:approved`).
  Out of scope; `scenarios/approved/` already exists for the
  CI-gating subset.
- **Watch mode** (auto-rerun on YAML save). Defer; `entr`/`fswatch` +
  the new `make single` invocation cover this without a runner
  change.
- **Parallel execution of N targeted scenarios.** v1 is one file.
- **`SCENARIO_TARGET` propagation through `USE_INFRA=true`.** §9 Q2.

## 11. Net change estimate

| Layer | Files | Lines (approx) |
|---|---|---|
| Config | `internal/runtime/runner.go` (add field + branch in scenario load) | ~15 |
| Main | `cmd/runner/main.go` (env read + inline comment) | ~5 |
| Tests | `internal/runtime/runner_test.go` (or new `runner_loader_test.go`) — 3-4 tests | ~80 |
| Makefile | `tools/integration-suite/Makefile` (`single` target + early-fail check + echo) | ~15 |
| Runbook | `tools/integration-suite/RUNBOOK.md` (one section + one gotcha row) | ~10 |
| Sync register | optional one-line note in §4.9 or new mini-section | ~3 |

Total: ~125 lines added; zero deletions; zero refactors to existing
runtime code paths.

## 12. Recommended landing path

1. **Land this spec** (commit + push).
2. **Single PR** — feature is small enough to do in one pass:
   - `rt.Config.ScenarioTarget` field + main.go env read
   - `runner.go` branch in scenario-load section
   - 3-4 unit tests
   - Makefile `single` target
   - RUNBOOK.md updates
3. **No demonstration PR needed** — the feature IS the demonstration.
   Authoring loop velocity is the user-facing proof.

PR reviewable in a single sitting (~125 lines, no architectural
surface change).

---

**Approval needed before any code lands.** Specifically:

- §2.2 path resolution rules (relative-to-tool-dir vs alternatives).
- §3.1 `rt.Config.ScenarioTarget` field placement + naming.
- §4.2 isolation from sibling-draft parse errors (the second
  approved refinement).
- §5 hard-fail mechanics via `os.Stat` + the three error-wording
  variants.
- §9 the six open questions — particularly Q5 (report metadata
  capture) and Q6 (Makefile echo).
- §12 PR sequencing (single PR, no demonstration PR).
