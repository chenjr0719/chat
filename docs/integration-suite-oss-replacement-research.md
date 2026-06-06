# Research: OSS replacements for integration-suite subsystems

**Status:** research / not a decision. Captured 2026-06 during a cleanup
pass. Revisit before any rewrite; tool landscape moves fast.

**Context:** `tools/integration-suite/` is a black-box scenario test
platform — ~12k LOC of Go covering scenario YAML loading, sandbox
lifecycle, six universal data-source pollers, mishap injection,
programmatic infra, Markdown reporting, and an interactive REPL.
This doc inventories which subsystems are reinventing wheels and
which encode genuinely domain-specific value, so a future maintainer
can pick swaps confidently.

**Bottom line:** most of the suite is shaped around this app's
NATS-first, multi-data-store, multi-site architecture and has no
clean 1:1 OSS replacement. Three subsystems are reinventing wheels
and have real OSS equivalents worth considering.

---

## 1. High-leverage swaps

### 1.1 Reporter → Allure / ReportPortal / JUnit XML

**Today**
- `internal/runtime/reporter.go` (~500 LOC) renders a hand-rolled
  Markdown report: header (git HEAD), confusion matrix
  (positive/negative × pass/fail), full cases table
  (latest/best/worst durations), per-failure Gomega mismatch reason.
- `internal/runtime/performance.go` + `cmd/perfmerge` track per-case
  latest/best/worst in a custom JSON, with deterministic merge for
  CI artifact collation.

**Candidates**

| Tool | License | Footprint | Best fit |
|---|---|---|---|
| **Allure** | Apache 2.0 | Framework-agnostic; `allure-go` writes result JSON; `allure serve` opens a UI | Rich UI, history, retries, attachments, flake detection |
| **ReportPortal** | Apache 2.0 | Self-hosted server, JUnit ingestion | Analytics dashboard, ML-assisted failure classification |
| **JUnit XML** + GitHub UI | n/a | Just XML emission | Minimum viable swap |

**Capability gained**
- Trends across runs (today: only latest/best/worst — no time series).
- Per-case timeline, attachments (closest-mismatch payloads,
  container logs at failure time).
- Retries / flake detection.
- Polished web UI free.

**Tradeoff**
- Lose the bespoke confusion-matrix view. Mitigation: tag cases as
  `@layer:positive` / `@layer:negative`; Allure's behavior-grouping
  reproduces the matrix.

**Estimated LOC delta:** delete reporter.go + runner_report.go +
performance.go + perf_merge.go (~1,200 LOC); add Allure writer
wrapping each `CaseReport` (~150 LOC). **Net ≈ -1,050.**

**Risk:** low — additive at first (write both Markdown and Allure
JSON), can keep Markdown until the Allure flow is proven.

### 1.2 Matcher → pure `gomega/gstruct`

**Today**
- `internal/matchers/matches_shape.go` (~250 LOC) implements:
  - Subset deep match (extra fields ignored).
  - ROSM array branch (delegates to `gstruct.MatchKeys` with
    `IgnoreExtras`).
  - Struct-marshal-as-map fall-through so typed payloads
    (`ReplyPayload`, `NATSSubscribePayload`) match field-by-field.

**Candidate:** lean on Gomega's `gstruct` directly.

**Capability gained**
- `MatchAllKeys` / `MatchKeys(IgnoreExtras, ...)` natively gives
  subset deep match.
- `MatchAllFields(Fields{...})` handles structs without the
  marshal-unmarshal trick.
- `gstruct.Pointer`, `gstruct.PointTo`, `MatchElements` come free.

**Tradeoff**
- Authors writing scenarios think in JSON-shape, not Gomega DSL —
  but the matcher already exposes the YAML `match:` shape; the
  Go internals can swap underneath without YAML grammar change.

**Estimated LOC delta:** ~250 → ~80. **Net ≈ -170.**

**Risk:** low — internal refactor, no YAML grammar change.

### 1.3 Interactive menu → `bubbletea` / `gum`

**Today**
- `internal/runtime/menu.go` (~430 LOC) hand-rolls stdin scanning,
  action parsing (`1`..`N` / `a` / `f` / `r` / `q` / empty-enter),
  status glyph rendering, repeat-last handling, scenario rescan.

**Candidates**

| Tool | License | Shape |
|---|---|---|
| **`charmbracelet/bubbletea`** | MIT | In-process Go TUI library (Elm-style update loop) |
| **`charmbracelet/gum`** | MIT | Shell-callable; `gum choose --multi-select`, `gum spin`, `gum table` |

**Capability gained**
- Arrow-key navigation.
- Search-as-you-type filter for long scenario lists.
- Multi-select (`a`/`f` semantics emerge naturally).
- Live spinner during `runScenario` (today: prints `[name]... ` then
  `✓ <dur>` after).

**Estimated LOC delta:** ~430 → ~100. **Net ≈ -330.**

**Risk:** low — orthogonal subsystem; CI batch path (non-INTERACTIVE)
unaffected.

---

## 2. Marginal / situational swaps

### 2.1 Catalog validator → JSON Schema

**Today:** `internal/catalog/validator.go` + `cmd/validator` walk YAML
and check `knownExecutors` / `knownServices` Go maps.

**Candidate:** `santhosh-tekuri/jsonschema` (or `qri-io/jsonschema`)
+ per-catalog `*.schema.json` files.

**Capability gained**
- IDE autocomplete on scenarios (most YAML editors honor
  `# yaml-language-server: $schema=…`).
- One validator binary across all catalogs.

**Tradeoff:** schemas can't express the cross-reference checks
("every `factory:` in mishap YAML resolves to a registered Go
factory") — those stay in Go.

**Estimated LOC delta:** ~100 saved. Modest.

**Recommendation:** defer until the catalog grammar stabilizes and
authors complain about IDE support.

### 2.2 Chaos engine → Pumba (additive)

**Today:** `internal/mishap/` wraps Docker restart + Toxiproxy
partitions in Go. Three mishap kinds: `crash`,
`mongo-partition-500ms`, `cassandra-partition-500ms`.

**Candidate:** **Pumba** — Docker chaos CLI for `kill`, `pause`,
`netem` (latency/loss/corruption/duplication). Toxiproxy stays as
the underlying TCP-layer chaos.

**Capability gained**
- Much richer fault catalog than today's three kinds: clock skew
  (via `netem`), CPU starvation (via `pause`), packet loss,
  reordering, duplication.

**Tradeoff:** Pumba is shell-oriented; the suite's mishap factories
are tightly integrated with the case-lifecycle (`Apply` on pre-
closed trigger, `defer Cleanup`). The wrapper code is similar size
either way.

**Recommendation:** keep as additive — add a Pumba-backed factory
when the first scenario needs netem; don't rip out the existing
crash/partition kinds.

### 2.3 Whole scenario engine → Venom

**Today:** scenario YAML → load → run cases → assert via Gomega
pollers → report. Roughly the entire `internal/runtime/` tree.

**Candidate:** **OVH `venom`** — YAML scenarios + extensible
executor model (HTTP, SQL, Kafka, Redis, gRPC, exec; community NATS
executor exists) + JUnit reporting + Gomega-style assertions.

**Why skeptical**
- The Sandbox lifecycle (13 steps: validate seeds, drop collections,
  truncate tables, mint NATS JWTs, insert profile docs, seed
  rooms+memberships+cassandra rows, build placeholders) is
  **chat-app-specific** and would re-emerge as a Venom executor of
  similar size.
- The **Warmer** abstraction (subscriptions open BEFORE the verb
  fires — required for Core NATS replay-less subjects) doesn't map
  cleanly onto Venom's testcase-step model.
- Cross-step state (`${alice.account}`, `${input.payload.x}`,
  `${bucket(col)}`, `${now ± d}`) exists in Venom as variables but
  with a different scoping model.

**Estimated effort:** 3-4 weeks of rewrites for a roughly equivalent
capability surface.

**Recommendation:** don't pursue. The shape match looks tempting on
the surface but Venom doesn't reduce the chat-app-specific code; it
just moves it.

---

## 3. Subsystems where OSS doesn't help

| Subsystem | Why irreplaceable |
|---|---|
| **Sandbox 13-step Setup** | Encodes domain knowledge (which collections to drop, that room-service requires `EngName`/`ChineseName` in profile docs, that bucket math must precede Cassandra writes, that placeholders feed substitution). This IS the suite's value. |
| **Six universal pollers** | Each is the canonical client library wrapped (`mongo-driver`, `gocql`, `nats.go` Subscribe, `nats.go/jetstream` Consume, `docker logs -f`). The "universal" part — accepting per-args from YAML — is shape-specific to the app's transports; no framework expresses it for free. |
| **Verbs / dispatcher** | ~150 LOC wrapping `nc.Request` + `js.Publish`. No framework. |
| **Substitution engine** | `${alias.account}`, `${now ± d}`, `${bucket(col)}`, `$auto` — domain-specific tokens. Go `text/template` would express it but more verbosely. |
| **Programmatic infra (`internal/infra`)** | Already uses `testcontainers-go`. Could swap for `ory/dockertest` — equivalent, not a win. |
| **Seed effects (`VerifiedEffect`)** | Single HTTP POST to auth-service. Nothing to import. |
| **NATS-first transport assertions** | Karate has a NATS adapter, Venom has a community NATS executor, but neither expresses Core NATS + JetStream + the **dispatcher-fed reply stream** in a way that beats the current ~100 LOC. |

---

## 4. Priorities (ROI-ordered)

| Rank | Action | LOC saved | Capability gained | Risk |
|---|---|---|---|---|
| 1 | **Adopt Allure** (or JUnit XML + a viewer) | ~1,200 | Trends, history, flake detection, polished UI | Low — additive at first |
| 2 | **Matcher → `gomega/gstruct`** | ~170 | `MatchAllFields`, `MatchAllKeys`, pointer helpers | Low — internal refactor only |
| 3 | **Menu → `bubbletea`** | ~330 | Arrow nav, search, multi-select | Low — orthogonal subsystem |
| 4 | **Pumba** for additional mishap kinds | 0 (additive) | netem latency/loss/corruption/pause | Low |
| 5 | **JSON Schema** for catalogs | ~100 | IDE autocomplete | Low |
| 6 | **Venom** full replacement | ~-2,000 (negative) | Standard reporter, community executors | High — multi-week rewrite |

**Concrete pitch:** do #1 and #2 when there's appetite. Together
that's ~1,400 LOC removed, no behavior loss, real UI capability
gained, both reversible. The rest are nice-to-haves; #6 is a trap.

---

## 5. Out-of-scope tools considered and rejected

| Tool | Why no |
|---|---|
| **Karate Labs** | JVM. Has NATS + WebSocket + gRPC adapters; could in principle express scenarios — but Go-first team, no clear win over current Go runtime. |
| **Robot Framework** | Python. Keyword-driven; mature; but the keywords for NATS + Cassandra + JetStream would be bespoke same as today. |
| **Tavern** | YAML-over-pytest; HTTP-centric; no NATS/JetStream. |
| **Cucumber / godog** | The v1 suite was godog (Gherkin). Team explicitly moved away. |
| **k6 / Locust / Grafana k6** | Load testing. Different purpose. |
| **Cypress / Playwright** | Browser-centric. No NATS. |
| **Pact** | Contract testing. Different model. |
| **Schemathesis** | Property-based API testing. Doesn't fit scenario-driven model. |
| **WireMock / Hoverfly** | Service virtualization. Inverse goal. |
| **Newman / Postman** | REST-only. |
| **Hurl** | HTTP-only with plain-text assertions. Good for auth `/auth` POST in isolation; no NATS. |
| **kuttl** | Kubernetes test framework. Wrong context. |

---

## 6. Revisit triggers

Look at this doc again when:

- A new author complains the reporter doesn't show flake history
  → time to adopt Allure (item 1).
- The matcher grows past ~400 LOC adding bespoke field-shape
  primitives → time to swap to gstruct (item 2).
- The interactive menu sprouts a "filter by name" feature request
  → time to swap to bubbletea (item 3).
- A scenario needs latency injection (`netem`) → time to add Pumba
  factory (item 4).
- A new author misses YAML autocomplete → time to add JSON Schema
  (item 5).
- Someone proposes Venom or Karate as a wholesale replacement →
  re-read §1 and §3 and push back. The sandbox lifecycle and the
  Warmer/pre-fire integration are the moat, not the YAML loader.
