# Architecture — integration-suite

A scenario-driven, **black-box** conformance suite for the chat
backend. The runtime authenticates as a real user, drives the
already-running services over the transports they actually speak,
watches the locations where their effects land, then asserts what it
observed via Gomega streaming matchers.

## 0. The model in one diagram

```
SCENARIO (one YAML)
   │
   ▼
┌────────── Sandbox (one per scenario) ──────────┐
│ Setup (13 steps):                               │
│   1.  validate seed.users[] flags vs catalog    │
│   2.  validate seed.rooms / memberships block   │
│   3.  validate seed.cassandra_data block        │
│   4.  capture sb.StartTime (T_open)             │
│   5.  Chaos.Reset()                             │
│   6.  drop {users, rooms, subscriptions} Mongo  │
│   7.  truncate sandbox-owned Cassandra tables   │
│   8.  materialize SeedUsers; mint NATS creds    │
│   9.  insert minimal user-profile docs          │
│   10. build Placeholders for ${alias.*}         │
│   11. insert seeded rooms/subscriptions/members │
│   12. insert seeded Cassandra rows              │
│   13. build PollerReg from BuiltinDeps          │
│                                                 │
│ For each case in declaration order:             │
│   ├─ Chaos.Reset() (between cases)              │
│   ├─ RunCase:                                   │
│   │    merge case.input over base_input         │
│   │    build sub Context (Site, Placeholders,   │
│   │      Services)                              │
│   │    if c.Mishap: factory → Executor          │
│   │      go Apply(trigger pre-closed)           │
│   │      defer Cleanup(30s ctx)                 │
│   │    for each expected[] with Warmer poller:  │
│   │      Warm(args) BEFORE the fire             │
│   │    Dispatcher.Fire(verb, sub, payl, cred,   │
│   │      tp) → reply lands in ReplyReader       │
│   │    for each expected[]:                     │
│   │      Eventually(poller.PollFn).             │
│   │        Should(MatchShape(match))            │
│   │      (or Consistently().ShouldNot for not)  │
│   │      → fail handler captures into Verdict   │
│   └─ recordCase → CaseReport + PerformanceStore │
│                                                 │
│ Teardown (via defer):                           │
│   pollerCleanup (close JetStream consumers,     │
│     NATS subscriptions, log tails)              │
│   Chaos.Reset()                                 │
└─────────────────────────────────────────────────┘
   │
   ▼
RunReport / last-run.md / performance.json
```

## 1. Repo layout

```
tools/integration-suite/
│  Makefile                  validate / local / run / preflight
│  README.md                 quick start + env knobs
│  ARCHITECTURE.md           this doc
│  AUTHORING.md              authoring workflow
│  SCENARIO-REFERENCE.md     YAML field reference
│  RUNBOOK.md                run + teardown ops doc
│
├─ catalogs/                 YAML data
│  ├─ verbs/                 nats_request, jetstream_publish
│  ├─ readers/               reply, mongo_find, cassandra_select,
│  │                         jetstream_consume, nats_subscribe,
│  │                         logs_tail
│  ├─ matchers.yaml          matches_shape (with ROSM array semantics)
│  ├─ mishaps/               crash, mongo-partition-500ms,
│  │                         cassandra-partition-500ms
│  ├─ services/              room-service, room-worker (declared pods)
│  └─ seed-effects/          verified.yaml — closed catalog of seed effects
│
├─ scenarios/
│  ├─ drafts/                YAMLs in development
│  └─ approved/              YAMLs gating CI (empty today)
│
├─ internal/
│  ├─ scenario/              Scenario types + loader + seed-block validators
│  ├─ catalog/               YAML → Go (readers, mishaps, services, …)
│  ├─ verbs/                 NATSRequest, JetStreamPublish executors
│  ├─ readers/               NATSReply, ContainerLogs, JetStreamSubject,
│  │                         CassandraSelect, NATSSubscribe
│  ├─ matchers/              matches_shape (Gomega-delegated ROSM
│  │                         array branch + scalar field match)
│  ├─ mishap/                ChaosEngine, DockerCLI, registry,
│  │                         partition/crash factories
│  ├─ infra/                 Programmatic Docker stack (USE_INFRA=true)
│  ├─ seedeffect/            Effect interface, VerifiedEffect, Registry
│  ├─ fixtures/              MintCredentials only (VerifiedEffect consumes)
│  └─ runtime/
│     ├─ sandbox.go          per-scenario shared state
│     ├─ case_runner.go      RunCase + mishap injection + Warmer pre-fire
│     ├─ runner_scenario.go  per-scenario loop (Setup → cases → Teardown)
│     ├─ runner.go           Run() — startup wiring + scenario walk
│     ├─ menu.go             INTERACTIVE=true stdin-driven menu
│     ├─ pollers/            Poller interface, six universal poller
│     │                      implementations + Warmer abstraction
│     ├─ matchshape.go       MatchShape Gomega OmegaMatcher
│     ├─ dispatcher.go       Dispatcher.Fire(InputSpec, ctx, cred, tp)
│     ├─ substitute.go       ${alias.*} / ${site} / ${now} / ${input.*}
│     ├─ tracing.go          NewTraceparent
│     ├─ reporter.go         CaseReport, RunReport, Verdict, Render
│     ├─ runner_report.go    recordCase + recordSkipped + statusFor
│     ├─ performance.go      PerformanceStore (latest/best/worst)
│     └─ perf_merge.go       cmd/perfmerge helper
│
├─ cmd/
│  ├─ runner/                main test runner
│  ├─ validator/             catalog + scenario static validation
│  └─ perfmerge/             merge performance.json from CI artifacts
│
└─ seed/
   ├─ room-keys.json         pre-seeded Valkey roomIDs (room-worker carryover)
   └─ loader.go              LoadRoomKeys only
```

## 2. The Sandbox lifecycle in detail

### 2.1 Setup (`Sandbox.Setup`)

Thirteen steps (see header comment on `Sandbox.Setup`). The ordering
matters in three places:

- **Validation precedes I/O** (steps 1-3): Unknown true-valued flags or
  malformed seed blocks abort the scenario before any side-effect.
- **`StartTime` precedes data writes** (step 4 before steps 11-12):
  Seeded `createdAt` values and the poller filter boundary share the
  same anchor.
- **`Placeholders` precedes seeded inserts** (step 10 before 11-12):
  The Cassandra-seed engine substitutes `${alice.id}` etc.; the room
  inserter gains the same capability transparently.

### 2.2 Case loop (`runScenario`)

For each `c` in `s.Cases`:

1. **Between-case Chaos.Reset** clears any partition the previous
   case left behind. Failure here records the case as "skipped" and
   continues to the next.
2. **`RunCase(ctx, sb, &c)`** does the case:
   - Shallow-merge `c.Input` over `base_input` → `InputSpec`.
   - Build sub `Context{Site, Placeholders, Services}`.
   - If `c.Mishap != ""`: lookup `FactoryByKind[c.Mishap]` →
     `MishapRegistry.GetFactory` → build Executor. Spawn `Apply` in a
     goroutine against a **pre-closed trigger** (each case is its own
     fire point). Defer `Cleanup` on a fresh 30s context.
   - Resolve credential via `pickCredential` (handles
     `${alias.credential}` and `${service.<name>.credential}`).
   - **Warmer pre-fire**: walk `expected[]`; for each poller that
     implements `Warmer`, call `Warm(args)` BEFORE the verb fires.
     This is the architectural pivot for Core NATS — `nats_subscribe`
     has no replay, so the subscription must open first.
   - `Dispatcher.Fire` substitutes subject + payload, runs the verb,
     pushes reply into `ReplyReader.Inject` so the `reply` poller's
     buffer sees it.
   - For each `expected[]`:
     - Look up the poller; substitute `match` and `args` against `sub`;
     - `g.Eventually(pollFn, timeout, polling).Should(MatchShape(match, reg))`
       (or `Consistently…ShouldNot…` for `not: true`).
     - Check `ca.failed` — break on first failure.
   - Build `CaseVerdict` from the captured failure messages.
3. **`recordCase`** appends a `CaseReport` to `report.Cases` and runs
   `perf.RecordExecuted("<scenario>/<case-name>", latest)`. The
   confusion-matrix logic in `reporter.go` picks up the new rows
   without any case-runner-specific coupling.

### 2.3 Teardown (`defer Sandbox.Teardown`)

- Close every stateful poller's resources via the cleanup func
  `RegisterBuiltinPollers` returned: JetStream ephemeral consumers,
  Core NATS subscriptions, container log tails, the dispatcher-fed
  reply stream poller.
- `Chaos.Reset` one more time so the next scenario starts clean.
- Both wrapped in `sync.Once` so a panic + manual call don't
  double-fire.

## 3. The streaming assertion model

### 3.1 Poller interface

```go
type Poller interface {
    PollFn(args map[string]any, traceparent string) func() []readers.Event
}

type Warmer interface {
    Warm(args map[string]any) error
}
```

Universal primitives accept `args` per-assertion — the runtime is
**100% application-agnostic**, all subject names / table names /
collection names live in scenario YAML. Stateful primitives
(`jetstream_consume`, `logs_tail`, `nats_subscribe`) cache the
underlying machinery keyed by args identity so multiple assertions
against the same source share one consumer/subscription/tail.

### 3.2 MatchShape

```go
g.Eventually(poller.PollFn(args, tp), 5*time.Second, 100*time.Millisecond).
    Should(MatchShape(expected, matcherReg))
```

`MatchShape` is a Gomega `OmegaMatcher` that succeeds iff at least one
event in the polled `[]readers.Event` has a `Payload` satisfying the
expected shape under `matchers.MatchesShape` (subset deep match with
type-normalizing compare). The array branch implements **Relative
Order Subset Match (ROSM)**: when the expected match is a list, the
matcher delegates to `gstruct.MatchKeys` and walks the observed
slice looking for each expected element in declaration order. On
failure, `FailureMessage` surfaces the closest-event mismatch reason.

### 3.3 NewGomega(failHandler)

`gomega.NewGomega(ca.Handler())` wires Gomega's matcher protocol to a
per-case `caseAssertion` sink. The handler records the failure
message and lets the goroutine continue — unlike Ginkgo's default
which `runtime.Goexit()`. The case loop checks `ca.failed` after each
`Eventually`/`Consistently` and breaks on the first failure. Verdict
captures the joined messages.

## 4. Reporting

`recordCase` writes:

- `CaseReport{ScenarioName, Subset:"case", Status, Kind, Duration,
  Verdict}` into `report.Cases`. `reporter.go:render` walks `Cases`,
  bins by Kind ("positive" / "negative") × Outcome to render the
  confusion matrix.
- `perf.RecordExecuted("<scenario>/<case-name>", latest)`.

`last-run.md` rendering, the approved-only filter, git heading, and
best/worst tracking are independent of the case-runner.

## 5. The chaos engine

The `internal/mishap` package owns Docker + Toxiproxy primitives:

- `ChaosEngine` is the single facade — `Reset()` clears every
  partition; `WithPartition(target, duration, fn)` is the partition
  primitive a factory composes.
- `DockerCLI` wraps `docker restart`/`docker exec` for the `crash`
  kind.
- `registry.go` is a factory-by-kind map. Each case names the kind in
  YAML; `RunCase` looks up the factory at fire time.
- Factories are pure constructors — given `FactoryContext{ChaosEngine,
  DockerCLI, Pod, Duration}`, return an `Executor{Apply, Cleanup}`.
  Cleanup always runs even when Apply panics.

## 6. INTERACTIVE mode

`INTERACTIVE=true` flips the runner from a batch sweep into a stdin
menu. Scenarios are listed with status glyphs; the user picks which
to fire. Per-pick the YAML is re-read from disk so editor saves are
picked up between runs.

| Input | Action |
|-------|--------|
| `1` … `N` | Run that scenario |
| `a` | Run all in order |
| `f` | Run only `✗` (failing) scenarios |
| `r` | Re-scan `scenarios/drafts/` for new files |
| `q` / Ctrl+D | Drain connections, exit 0 |
| `<empty ENTER>` | Repeat last action |

CI is bit-identical to today when `INTERACTIVE` is unset.
