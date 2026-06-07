# Architecture — integration-suite-multisite

A scenario-driven, **black-box** conformance suite for the chat backend
running across two federated sites. The runtime authenticates as real
users on either site, drives the already-running services over the
transports they actually speak, watches the locations where their
effects land (on either site), then asserts what it observed via Gomega
streaming matchers.

This is the multi-site fork of `tools/integration-suite/`. The
single-site tool is unchanged — see its own `ARCHITECTURE.md` for the
single-site model.

---

## 0. Tool design contract

The tool is a **feature-agnostic black-box harness.** Its grammar
describes seed state, the fire, and the expected outcomes. The tool
does not encode knowledge of any specific scenario, app feature, or
production code path beyond what is required to materialise
YAML-declared state.

Two rules govern every tool change:

**Rule 1 — Scenarios should not require tool changes.** New scenarios
for new features should require zero tool changes. If they do, either
the grammar is incomplete (extend it generically — for ALL future
scenarios), or the scenario was misauthored (fix the scenario, not
the tool).

**Rule 2 — Tool behavior must be explicit in the YAML.** Every state
the tool materialises is declared in the scenario file. The tool
never infers state the author didn't write. The author can read the
YAML and predict every Mongo document, NATS subject, and Cassandra
row the sandbox will produce.

For every proposed tool change, run both gates:

```
   1.  Would this be needed for an arbitrary new scenario testing an
       UNRELATED feature?
            yes  →  grammar completion. Ship it.
            no   →  scenario-specific. Refuse. Extend grammar or fix
                    the scenario instead.

   2.  Is the new behavior explicit in the YAML the author writes, or
       implicit in tool inference?
            explicit  →  principled. Ship it.
            implicit  →  shortcut. Revisit until explicit.
```

A change that fails either gate is a leak: it encodes app knowledge
into the harness, which then becomes brittle the next time the app
adds a feature the encoded rule didn't anticipate.

### The tool's limits

The tool has a bounded scope. Outside those bounds is the operator's
problem, not the tool's. Treating these limits as soft — quietly
extending the tool to handle "just one more thing" — is the most
common way harnesses turn into the system they were supposed to
test. We document the limits explicitly so the temptation to cross
them is visible every time.

The tool will:
- materialise YAML-declared state into the appropriate backend;
- orchestrate containers, networks, and per-site env wiring;
- apply federation Sources between the two NATS clusters (the harness
  analog of setting up the federation link);
- observe state via the universal pollers;
- report what the system returned, verbatim.

The tool will NOT:
- create app-side streams, collections, or tables that no service
  bootstraps. If production assumes a resource exists because ops/IaC
  put it there, and ops/IaC isn't running in the test, the failure
  stands as a finding.
- write app-side documents the scenario didn't declare.
- guess what production "probably meant" when something is missing.
- provide workarounds, recovery steps, or fix-up procedures for
  failures it surfaces. *How* the operator gets unblocked — pre-
  creating a missing stream, fixing the app, reshaping the scenario —
  is the operator's call, not the tool's documentation surface.

Concrete examples of resources the harness does not create:

- **`OUTBOX_<site>` JetStream streams.** Owned by ops/IaC in
  production (per `CLAUDE.md` §"Stream bootstrap ownership"). No chat
  service bootstraps them. If a scenario fires production code that
  publishes to `outbox.<site>.>`, the publish returns whatever NATS
  returns when the stream is absent. That's the finding.
- Any other operationally-scaffolded resource.

When the failure points at a gap outside the tool's scope, the tool's
job is done: the system's verbatim error is the finding. What to do
about it lives wherever ops decisions live, not here.

### Infra-sanity scenarios — the harness's own health check

Scenarios named `infra-sanity-*` (`status: approved`) verify the
stack itself before any app-behavior scenario is even attempted. They
are the harness's first line of defence: if an infra-sanity scenario
fails, downstream failures are not informative and reading them
wastes time. Authoring rules for sanity tests live in
`AUTHORING.md`. The convention exists because the alternative — a
single suite that mixes app tests with implicit stack assumptions —
hides failures that should stop the run.

### Assertion completeness — a scenario authoring discipline

For every fire that exercises a multi-step pipeline, assert at every
layer the pipeline can be observed at (reply, originating stream,
intermediate streams, peer-site streams, downstream persistence,
logs). Skipping intermediate layers makes failures ambiguous and
lets silent correctness drift survive refactors. The full discipline
and worked examples live in `AUTHORING.md`.

### What a failing scenario means

A scenario failure is **NOT** evidence of a tool bug by default. The
tool exists to find bugs in the app under test — that's its main
purpose. When a scenario fails, both interpretations are alive:

```
   failing assertion  ──┬──►  app bug  (the production code did
                        │              the wrong thing) — the
                        │              tool is doing its job
                        │
                        └──►  tool bug  (the harness materialised
                                         state wrong, polled the
                                         wrong place, mis-routed
                                         the fire) — fix the tool
```

Disambiguating between the two is the operator's job. Common signals:

- production code logs an error mid-flow → most likely an app bug;
  the harness reached the right code path and surfaced the real
  failure mode (this is what we want)
- production code never runs / never logs → could be either; check
  whether the seed state, fire subject, credentials are correct
- the same assertion fails when you fire from production-grade
  state (manual stack, production env) → app bug
- the assertion fails only under the harness's seed state → likely
  a tool grammar gap or misuse

When a finding turns out to be an app bug, that is the platform
working as designed. The two gates above are about the **harness
itself** being principled enough that operators trust the bugs it
surfaces are real.

The single-purpose-harness disclaimer at the top of the spec
(`docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md`)
is the *implementation* posture; this section is the *design* contract
the implementation must respect.

---

## 0.1 The 26-container stack

```
  ┌─────────────────────────────────────────────────────────────────┐
  │  Docker network: chat-local-multisite                           │
  │                                                                 │
  │  ┌─────────────┐  leafnode   ┌─────────────┐                   │
  │  │ nats-site-a │◄────────────│ nats-site-b │                   │
  │  │  4222/7422  │             │  4222       │                   │
  │  └──────┬──────┘             └──────┬──────┘                   │
  │         │ JetStream domain=site-a   │ JetStream domain=site-b  │
  │                                                                 │
  │  ┌──────────────┐         ┌──────────────┐                     │
  │  │ mongo-site-a │         │ mongo-site-b │                     │
  │  └──────────────┘         └──────────────┘                     │
  │                                                                 │
  │  ┌───────────────┐        ┌───────────────┐                    │
  │  │ valkey-site-a │        │ valkey-site-b │                    │
  │  └───────────────┘        └───────────────┘                    │
  │                                                                 │
  │  ┌─────────────────────────────────────────┐                   │
  │  │  cassandra  (shared — single cluster)   │                   │
  │  └─────────────────────────────────────────┘                   │
  │                                                                 │
  │  ┌────────────────────────────────────────────────────────┐    │
  │  │  toxiproxy (6 proxies, all programmatic)               │    │
  │  │  MongoProxy-site-a  :27017 → mongo-site-a:27017        │    │
  │  │  MongoProxy-site-b  :27018 → mongo-site-b:27017        │    │
  │  │  CassandraProxy-site-a :9042 → cassandra:9042          │    │
  │  │  CassandraProxy-site-b :9043 → cassandra:9042          │    │
  │  │  NATSProxy-site-a   :4222 → nats-site-a:4222           │    │
  │  │  NATSProxy-site-b   :4223 → nats-site-b:4222           │    │
  │  └────────────────────────────────────────────────────────┘    │
  │                                                                 │
  │  Services (18 total, 9 per site):                               │
  │  auth-service-{site-a,site-b}                                   │
  │  broadcast-worker-{site-a,site-b}                               │
  │  history-service-{site-a,site-b}                                │
  │  inbox-worker-{site-a,site-b}                                   │
  │  message-gatekeeper-{site-a,site-b}                             │
  │  message-worker-{site-a,site-b}                                 │
  │  notification-worker-{site-a,site-b}                            │
  │  room-service-{site-a,site-b}                                   │
  │  room-worker-{site-a,site-b}                                    │
  └─────────────────────────────────────────────────────────────────┘
```

**Total: 26 containers** — 2 NATS + 2 Mongo + 2 Valkey + 1 Cassandra +
1 Toxiproxy + 18 service replicas.

---

## 1. NATS cross-site transport and JetStream domains

Each site runs its own NATS server with `jetstream:` enabled and a
distinct `domain` per site (`site-a` / `site-b`). The two servers
are joined by a **leafnode link** rather than a supercluster
gateway:

- site-a hosts a leafnode listener on `:7422`.
- site-b's `remotes:` block dials `nats-leaf://nats-site-a:7422`
  using `backend.creds` (the same chatapp account user that every
  service authenticates as).

Leafnodes are the chosen transport because supercluster gateways do
not carry the `$JS.<peer>.API.*` subjects required by JetStream
Sources to pull from a remote domain (see
`docs/integration-suite-multisite-findings.md` F-001 for the
empirical proof). Leafnodes expose the full account subject space —
including `$JS.<domain>.API.*` — across the link, which is exactly
what cross-domain Sources need.

Each server runs **standalone JetStream** — no `cluster:` block.
Combined with the SubjectTransform on the federation Source (§2)
and operator-owned `pre_fire_scripts` that stand up OUTBOX before
the fire, this topology delivers all 5 surfaces of cross-site
scenarios end-to-end (Run 71). F-001 captures the full
iteration trail that landed here.

The runner dials each site's NATS directly (not via Toxiproxy) using
the `WithDomain(siteID)` JetStream option so that stream operations
and consumers are anchored to the correct JetStream domain.

---

## 2. Federation via JetStream Sources

After the 18 service containers are healthy, the runner:

1. Waits until `INBOX_site-a` and `INBOX_site-b` exist on their
   respective JetStream domains (created by `inbox-worker-site-a` and
   `inbox-worker-site-b` at startup).
2. Reads `catalogs/federation.yaml`, which declares which streams
   source from which remote streams.
3. Applies `UpdateStream` on each INBOX to add a `Sources` entry
   pointing at the remote OUTBOX, **with a `SubjectTransform`** that
   rewrites the inbound subject to match the INBOX schema:
   - `INBOX_site-a.Sources` ← `OUTBOX_site-b`, transform
     `outbox.site-b.to.site-a.>` → `chat.inbox.site-a.aggregate.>`
   - `INBOX_site-b.Sources` ← `OUTBOX_site-a`, transform
     `outbox.site-a.to.site-b.>` → `chat.inbox.site-b.aggregate.>`

The SubjectTransform is required because `inbox-worker`'s consumer
binds to `chat.inbox.{site}.aggregate.>`, not to the raw `outbox.>`
subject. The chat-app design owns this rewrite at the federation
layer — see `pkg/stream/stream.go` `Inbox()` docstring lines 64-69.
The test tool mirrors that production federation shape; it does not
invent it.

This gives the standard Outbox/Inbox cross-site event path. Services
publish to their local OUTBOX; the federation source pulls those
events into the remote INBOX (rewriting the subject on the way in);
remote `inbox-worker` processes them.

---

## 3. Toxiproxy as the chaos surface

All 18 services connect to their dependencies through the single
Toxiproxy container. The proxy list is created programmatically via
the Toxiproxy admin API at boot — no JSON preload file.

Services at `<svc>-site-a` receive:

```
MONGO_URI=mongodb://chat-local-toxiproxy:27017
NATS_URL=nats://chat-local-toxiproxy:4222
CASSANDRA_HOSTS=chat-local-toxiproxy:9042
VALKEY_ADDRS=valkey-site-a:6379   (no proxy — direct)
```

Services at `<svc>-site-b` receive:

```
MONGO_URI=mongodb://chat-local-toxiproxy:27018
NATS_URL=nats://chat-local-toxiproxy:4223
CASSANDRA_HOSTS=chat-local-toxiproxy:9043
VALKEY_ADDRS=valkey-site-b:6379   (no proxy — direct)
```

Per spec §5.3, **mishaps/chaos are disabled** in this phase. Toxiproxy
boots passively in the connection path and no toxics are injected.
Mishaps will re-enable once federation is reliably green.

---

## 4. Repo layout

```
tools/integration-suite-multisite/
│  Makefile                  validate / local
│  README.md                 quick start + open concerns
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
│  ├─ mishaps/               (passive — not injected in this phase)
│  ├─ services/              declared service pods
│  ├─ seed-effects/          verified.yaml — closed catalog
│  └─ federation.yaml        cross-site JS Sources declarations
│
├─ scenarios/
│  ├─ drafts/                YAMLs in development
│  └─ approved/              YAMLs gating CI (empty today)
│
├─ internal/
│  ├─ scenario/              Scenario types + loader + validators
│  ├─ catalog/               YAML → Go (readers, services, …)
│  ├─ verbs/                 NATSRequest, JetStreamPublish executors
│  ├─ readers/               NATSReply, ContainerLogs, JetStreamSubject,
│  │                         CassandraSelect, NATSSubscribe
│  ├─ matchers/              matches_shape (Gomega-delegated ROSM)
│  ├─ mishap/                (passive — wiring present, no injection)
│  ├─ infra/                 Programmatic 26-container stack
│  ├─ seedeffect/            Effect interface, VerifiedEffect, Registry
│  ├─ fixtures/              MintCredentials (VerifiedEffect consumes)
│  └─ runtime/
│     ├─ sandbox.go          per-scenario shared state (two sites)
│     ├─ runner_scenario.go  per-scenario loop (Setup → fire → assert)
│     ├─ runner.go           Run() — startup wiring + scenario walk
│     ├─ pollers/            Poller interface, six universal pollers
│     ├─ matchshape.go       MatchShape Gomega OmegaMatcher
│     ├─ dispatcher.go       Dispatcher.Fire(InputSpec, ctx, cred, tp)
│     ├─ substitute.go       ${alias.*} / ${now} / ${input.*}
│     ├─ tracing.go          NewTraceparent
│     ├─ reporter.go         ScenarioReport, RunReport, Verdict, Render
│     ├─ runner_report.go    recordScenario + statusFor
│     └─ performance.go      PerformanceStore (latest/best/worst)
│
├─ cmd/
│  ├─ runner/                main test runner
│  └─ validator/             catalog + scenario static validation
│
└─ seed/
   └─ loader.go              shared seed utilities
```

---

## 5. The Sandbox lifecycle

### 5.1 Setup (`Sandbox.Setup`)

Setup runs once per scenario. For each site declared in `sites:`,
the sandbox:

1. **Validates** the seed block (effect flags, room/membership shapes,
   Cassandra table/column names). Unknown flags abort before any I/O.
2. **Captures `StartTime`** (T_open) — poller filter boundaries and
   seeded `createdAt` values share this anchor.
3. **Drops collections** (`users`, `rooms`, `subscriptions`) in the
   site's Mongo database.
4. **Truncates** Cassandra tables owned by the sandbox (shared cluster,
   so both sites' seeds operate against the same keyspace).
5. **Materializes SeedUsers**: calls the per-site auth-service
   (`SITE_A_AUTH_SERVICE_URL` / `SITE_B_AUTH_SERVICE_URL`) to mint
   NATS JWTs; builds `Placeholders` for `${alias.*}` substitutions.
6. **Inserts user-profile docs** in the site's Mongo.
7. **Inserts seeded rooms / subscriptions / memberships** in the site's
   Mongo.
8. **Inserts seeded Cassandra rows** (shared cluster, partitioned by
   room_id + bucket computed via `${bucket(<col>)}`).
9. **Builds `PollerReg`** from `BuiltinDeps{SiteA, SiteB}`.

The ordering rule: validation before I/O; `StartTime` before writes;
`Placeholders` before inserts (so `${alice.id}` resolves in Cassandra
seed rows and room inserts alike).

### 5.2 Fire + assert (`runScenario`)

There is **no case loop** — each scenario has exactly one input and one
expected list.

1. **Dispatch**: substitute `input.subject`, `input.payload`, and
   `input.credential`; route to the site named in `input.site`;
   `Dispatcher.Fire(verb, sub, payload, cred, tp)`. For
   `nats_request` the synchronous reply is injected into the
   `ReplyReader` buffer.
2. **Warmer pre-fire**: walk `expected[]`; for each poller that
   implements `Warmer` (notably `nats_subscribe`), call `Warm(args)`
   before the verb fires. Core NATS has no replay — the subscription
   must open first.
3. **Assert**: for each `expected[]` entry:
   - Look up the poller; substitute `match` and `args`.
   - Pick the right site connection based on `expected[i].site`.
   - `g.Eventually(pollFn, timeout, polling).Should(MatchShape(match, reg))`
     (or `Consistently…ShouldNot…` for `not: true`).
   - Break on first failure; capture mismatch reason into `Verdict`.
4. **`recordScenario`** appends a `ScenarioReport` to `report.Scenarios`
   and runs `perf.RecordExecuted("<scenario>", latest)`.

### 5.3 Teardown (`defer Sandbox.Teardown`)

- Close every stateful poller's resources via the cleanup func that
  `RegisterBuiltinPollers` returned: JetStream ephemeral consumers,
  Core NATS subscriptions, container log tails.
- Both wrapped in `sync.Once` so a panic + manual call don't
  double-fire.

---

## 6. The verb/reader primitive catalog

The runtime ships six universal data-source primitives. Each is
application-agnostic — all subject names, table names, collection
names, and container names live in scenario YAML.

| Location           | Args                          | Site routing                              |
|--------------------|-------------------------------|-------------------------------------------|
| `reply`            | (none)                        | intrinsic — bound to `input.site`         |
| `mongo_find`       | `collection`, `filter`        | required `site:` on `expected[i]`         |
| `cassandra_select` | `query`, `params?`            | forbidden — shared cluster                |
| `jetstream_consume`| `stream`, `filter_subject`    | required `site:` on `expected[i]`         |
| `nats_subscribe`   | `subject`                     | required `site:` on `expected[i]`         |
| `logs_tail`        | `container`, `service?`       | required `site:` — resolves `<svc>-<site>`|

**Site values:** exactly `site-a` or `site-b`. Any other value is a
loader error.

---

## 7. The streaming assertion model

### 7.1 Poller interface

```go
type Poller interface {
    PollFn(args map[string]any, traceparent string) func() []readers.Event
}

type Warmer interface {
    Warm(args map[string]any) error
}
```

Stateful primitives (`jetstream_consume`, `logs_tail`, `nats_subscribe`)
cache the underlying machinery keyed by args identity, so multiple
assertions against the same source share one consumer/subscription/tail.

### 7.2 MatchShape

```go
g.Eventually(poller.PollFn(args, tp), 5*time.Second, 100*time.Millisecond).
    Should(MatchShape(expected, matcherReg))
```

`MatchShape` is a Gomega `OmegaMatcher` that succeeds iff at least one
event in the polled `[]readers.Event` has a `Payload` satisfying the
expected shape under `matchers.MatchesShape` (subset deep match with
type-normalizing compare). The array branch implements **Relative Order
Subset Match (ROSM)**: when the expected match is a list, the matcher
walks the observed slice looking for each expected element in declaration
order. On failure, `FailureMessage` surfaces the closest-event mismatch
reason.

### 7.3 NewGomega(failHandler)

`gomega.NewGomega(sa.Handler())` wires Gomega's matcher protocol to a
per-scenario `scenarioAssertion` sink. The handler records the failure
message and lets the goroutine continue — unlike Ginkgo's default which
calls `runtime.Goexit()`. The assertion loop checks `sa.failed` after
each `Eventually`/`Consistently` and breaks on the first failure.
`Verdict` captures the joined messages.

---

## 8. Reporting

`recordScenario` writes:

- `ScenarioReport{ScenarioName, Status, Tag, Duration, Verdict}` into
  `report.Scenarios`. `reporter.go:render` bins by `Tag` ("positive" /
  "negative") × Outcome to render the confusion matrix.
- `perf.RecordExecuted("<scenario>", latest)`.

Report paths (relative to repo root):

```
docs/integration-suite-multisite/last-run.md
docs/integration-suite-multisite/last-run-approved.md
docs/integration-suite-multisite/last-run-interactive.md
docs/integration-suite-multisite/performance.json
```

`@status:approved` scenarios form the CI-gating score. Drafts are
informational.

---

## 9. Runner-side environment

When `USE_INFRA=true` (the only supported mode), the runner calls
`infra.Up`, which:

1. Boots all 26 containers.
2. Returns a `Config{SiteA, SiteB}` with per-site URLs.

The runner dials resources **directly** via host-mapped ports — not
through Toxiproxy, because the runner process cannot resolve container
aliases. Per-site env vars:

```
SITE_A_NATS_URL
SITE_A_MONGO_URI
SITE_A_AUTH_SERVICE_URL
SITE_B_NATS_URL
SITE_B_MONGO_URI
SITE_B_AUTH_SERVICE_URL
```

---

## 10. Locked design decisions

Five decisions were locked during the design phase
(`docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md`):

**One input, one expected list.** The case-loop model from single-site
was dropped. A multi-site scenario is one fire across a federation
boundary and one set of assertions. Bundling multiple cases in a single
scenario would make it harder to attribute failures to individual
hypotheses; write separate scenarios for independent assertions.

**`site:` is required on inputs and site-scoped expected entries.**
The loader rejects scenarios that omit `site:` on `input`, on
`mongo_find`/`jetstream_consume`/`nats_subscribe`/`logs_tail` expected
entries, and scenarios that include `site:` on `reply` or
`cassandra_select` entries. This is enforced at load time, not at
runtime, so authoring errors surface before any container is booted.

**Seed is nested under `sites.<site>.seed`.** Top-level `seed:` is
forbidden. Users, rooms, and memberships are declared per-site so the
Sandbox can issue auth calls and Mongo writes to the correct site.
Cassandra data is at scenario top level because Cassandra is a shared
cluster.

**USE_INFRA=true is the only supported mode.** A two-site stack is
impractical to run by hand for ad-hoc development. The infra package
owns all 26 containers and their wiring.

**Mishaps deferred to a follow-up phase.** Per spec §5.3, Toxiproxy
proxies are in the connection path but no toxics are injected until
federation is reliably green. The `mishap/` package and Toxiproxy
wiring remain in the codebase so they can be re-enabled without
structural changes.
