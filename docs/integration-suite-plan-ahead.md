# Integration suite — plan ahead

**Status:** research / scoping note. Captured 2026-06 during a deep
discussion of the tool's design and where it should evolve.

**Purpose:** snapshot the current state and the directions we've
identified, so a future maintainer can ground a real spec on this
without rediscovering the analysis. **This is not itself a spec.** Two
follow-on specs are anticipated (§4).

---

## 1. What the tool is today

`tools/integration-suite/` is a black-box, scenario-driven conformance
suite for the chat backend. One YAML per scenario; the runner
authenticates as a real user, drives services over the transports they
actually speak, polls the locations where their effects land, and
asserts via Gomega streaming matchers. Output is
`docs/integration-suite/last-run.md` (Markdown report + confusion
matrix) plus `performance.json` (per-case latest/best/worst).

**Reference docs (today's design — current, accurate):**
- `tools/integration-suite/README.md` — quick start, env vars
- `tools/integration-suite/ARCHITECTURE.md` — runtime model
- `tools/integration-suite-multisite/SCENARIO-REFERENCE.md` — YAML grammar (active)
- `tools/integration-suite-multisite/AUTHORING.md` — workflow (active)
- `tools/integration-suite-multisite/RUNBOOK.md` — operate + read results (active)
- `tools/archived/integration-suite/STALE.md` — legacy single-site suite, feature reference only
- `tools/archived/integration-suite/docs/sync-register.md` — drift register (archived)
- `tools/archived/integration-suite/docs/oss-replacement-research.md` — OSS swap analysis (archived)
- `docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md` — multi-site spec (implemented, see `docs/integration-suite-multisite-findings.md`)

**The frame in one diagram:**

```
   FIXED FRAME (Go — changes rarely)              EVOLVING SURFACE (YAML — changes weekly)
   ─────────────────────────────────              ──────────────────────────────────────
   2 verbs:  nats_request, jetstream_publish      scenarios/drafts/*.yaml
   6 pollers (universal primitives):                each names the actual subjects,
     reply, mongo_find, cassandra_select,           collections, streams, services
     jetstream_consume, nats_subscribe,             via per-assertion `args:` blocks
     logs_tail
   3 mishaps: crash, mongo-partition,             catalogs/services/*.yaml
              cassandra-partition                   declares service profiles (pods)
   matches_shape matcher (incl. ROSM)             catalogs/{verbs,readers,mishaps}/*.yaml
                                                    closed vocabulary; validator gate
```

---

## 2. Issues identified during the discussion

### 2.1 Per-case state inheritance is a footgun

Today's case loop (`internal/runtime/runner_scenario.go:99`) runs
Setup once per scenario, then iterates cases with only
`Chaos.Reset()` between them. Mongo, Cassandra, JetStream streams,
and stateful poller buffers all carry forward.

**Concrete failure** (traced in `scenarios/drafts/create-room-sandbox.yaml`):
- Case 1 (`happy-path-room-created`) creates room "Engineering Chat";
  Mongo holds it.
- Case 2 (`withstands-mongo-partition`) fires the SAME create-room
  verb. room-service sees the duplicate name and short-circuits
  before touching Mongo. The 500ms Mongo partition is never exercised.
- The assertion `mongo_find where name = Engineering Chat` returns
  Case 1's row. Case 2 reports **pass** without testing what it
  claims to test. **Placebo.**

**Root cause:** Setup is cached for the whole scenario (JWT mint is
expensive — ~80ms × N users) but the *data-touching* Setup steps
(drop / truncate / re-seed) are cached too. They don't have to be.

**Cheap fix that preserves performance:** re-run only the data steps
(Mongo drop / Cassandra truncate / seed re-apply) between cases,
keeping JWTs + Placeholders cached. Cost ≈ +55ms × cases × scenarios
(~3s added to a ~30s full run).

### 2.2 Cases-as-variants is the wrong primitive

`AUTHORING.md` Rule 4 says "Cases see each other's effects — that's
intentional." In practice across the 11 scenarios under
`scenarios/drafts/`, only one (history pagination) genuinely chains
cases. The rest treat cases as variants on the same fire — which is
exactly where the inherited-state footgun bites.

**Better mental model** (proposed during the discussion): one
scenario = one **input** (the verb being tested) + two **envelopes**
(positive: graceful success; negative: graceful rejection). A
**chaos engine** generates iterations with different mishaps; each
iteration must land in one envelope to pass. Anything else
(matched neither) = corruption = real bug.

```
                            matched +ve   matched -ve   UNDEFINED
   no-mishap                    18            2            0
   crash                         4            6            0     ← graceful
   mongo-partition               3            7            0     ← graceful
   cassandra-partition           2            7            1     ← BUG
```

The third column is the strongest pass criterion. Today's
`tag: positive/negative` per case is a procedural skeuomorph of
unit-test style; envelopes are the right shape for black-box
conformance.

### 2.3 Multi-step narratives need first-class support

Some flows are genuinely sequential ("alice creates room → bob joins
→ alice sends message → bob reads"). The current `cases:` array
expresses this poorly (cases-as-narrative shares the state footgun)
and the single-`base_input` model doesn't express it at all.

*Concretely, the present-day symptom in multi-site is "one fire per
scenario" — `input:` is a single struct, not a list. This blocks
JetStream redelivery / Nak-retry assertions, canonical Nats-Msg-Id
dedup tests, message-gatekeeper double-send dedup, `tcount` CAS
concurrency, and subsequent-thread-reply scenarios. The DAG model
below is the structural answer; see §2.7 for the smaller seed-grammar
gaps surfaced alongside this one during authoring.*

**Discussion conclusion:** input should be a **DAG of tasks**
(Airflow-style), not a single fire. Each task is one verb + subject
+ payload + credential, with explicit dependencies:

```yaml
input:
  - id: t1
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.${siteA}.create
    payload: { name: Engineering }
    credential: ${alice.credential}
  - id: t2
    verb: nats_request
    subject: chat.user.${bob.account}.request.room.${siteA}.join
    depends_on: [t1]                                    # waits for t1's reply
    payload: { roomId: ${t1.reply.body_json.roomId} }   # references upstream
    credential: ${bob.credential}
  - id: t3
    verb: nats_request
    subject: chat.user.${alice.account}.room.${t1.reply.body_json.roomId}.${siteA}.msg.send
    depends_on: [t2]
    payload: { text: "hi" }
    credential: ${alice.credential}
```

**Why DAG over a new verb:**
- Naturally expresses sequential, parallel, and mixed flows.
- Sibling tasks (no `depends_on` between them) fire concurrently — the
  replica/race-test model falls out for free.
- Upstream task outputs become substitution sources (`${t1.reply.*}`)
  — clean continuation grammar.
- Failed upstream task (e.g. error reply) cleanly halts dependents.
- Composes with the envelope model: assertions attach to the whole
  DAG outcome, not to individual tasks.

### 2.4 Replica fan-out folds into DAG

What was proposed earlier as a `replicas: 3` field on a user becomes
unnecessary — three sibling tasks with no `depends_on` between them
fire concurrently. The DAG model subsumes replica fan-out.

For ergonomics, a `for_each` over a list could compress N siblings
into one task definition — but the underlying execution model is
identical.

### 2.5 Multi-site composes

The approved multi-site spec
(`docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md`)
describes spatial fan-out (per-site polling, fan-out pollers,
`_site` provenance). The envelope-and-DAG model described here is
temporal (per-iteration chaos) and structural (DAG instead of cases).

The two compose cleanly:
- Each DAG task names its firing credential → implies firing site.
- Each iteration of the chaos engine runs the whole DAG against the
  multi-site stack; fan-out pollers see both sites; envelopes
  filter by `_site` as already designed.

### 2.6 Input order — author-declared topologies

Building on §2.3 (DAG-of-tasks) and §2.5 (chaos engine): the **shape**
of the DAG is itself a stress dimension. Author defines tasks once,
then lists named orderings to test:

```yaml
input:                              # the WHAT (tasks defined once)
  - id: t1
    ...
  - id: t2
    payload: { roomId: ${t1.reply.body_json.roomId} }  # data dep on t1
  - id: t3
    payload: { roomId: ${t1.reply.body_json.roomId} }  # data dep on t1

input_order:                        # the HOW/WHEN (orderings to test)
  case_a: t1 >> [t2, t3]            # t1 first, then t2 + t3 in parallel
  case_b: t1 >> t2 >> t3            # serial chain
  case_c: t1 >> t3 >> t2            # serial, different order

chaos:                              # common across orderings
  iterations: 20
  kinds: any
expected:                           # common envelopes
  positive: [...]
  negative: [...]
```

**Notation** (Airflow-borrowed, deliberately small):
- `a >> b` — a finishes before b starts
- `a >> [b, c]` — a finishes; b and c fire in parallel
- `[a, b] >> c` — a and b parallel; c waits for both
- `[a, b]` — all parallel
- `a >> b >> c` — chain

**Runner behavior:** the matrix is now 2-dimensional:

```
for each entry in input_order:                    ◄── outer: ordering
   for iter in 1..chaos.iterations:               ◄── inner: chaos pick
      fresh_state()                               ◄── before EVERY iteration
      pick = rand.pick(chaos.kinds, seed)
      spawn mishap.Apply(pick)
      execute DAG in this ordering
      assert against Or(positive, negative)
      record (ordering, iter, chaos, outcome)
```

3 orderings × 20 iterations = 60 test executions per scenario; one
hand-authored scenario.

**Validation:** runner derives hard data dependencies statically from
payload substitutions (e.g., `${t1.reply.body_json.roomId}` in t2's
payload implies t2 → t1). Each entry in `input_order` is checked
against these; violations rejected at scenario-load time before any
I/O. So `t1 >> t3 >> t2` is fine if t2 has no data dep on t3, and
rejected if it does.

**Why author-declared over auto-enumeration:**
- Authors carry intent ("I want these 3 orderings stressed", not all
  18 valid permutations).
- Named cases survive into the report (`case_c × crash` is more
  readable than `ordering #7 of 18`).
- Bounded — no combinatorial blow-up on scenarios with N≥4 siblings.
- Auto-enumeration could still be a runner flag for exploratory
  authoring (`input_order: auto`), but the default is explicit.

**Coverage example:**

```
                                matched +ve   matched -ve   UNDEFINED
   case_a × no-mishap                20            0             0
   case_a × mongo-partition          18            2             0     ← graceful
   case_a × crash                    19            1             0     ← graceful
   case_b × no-mishap                20            0             0
   case_b × mongo-partition          15            5             0     ← graceful
   case_c × no-mishap                 0           20             0     ← correctly always negative
   case_c × mongo-partition           0           20             0
   case_c × crash                     0           19             1     ← UNDEFINED = bug in negative path
```

The last row is the property: "even when send precedes join
(impossible-but-attempted), and even under crash, the system
gracefully refuses — never corrupts." The single UNDEFINED is the
real bug.

### 2.7 Seed-grammar gaps surfaced during authoring

Two concrete gaps where current seed primitives don't reach what
production code reads, blocking specific scenario classes today.
Smaller-scope than the DAG/envelope direction above — concrete
extensions that don't pre-judge the bigger spec, and could ship
independently when a scenario demands either.

**T1 — room metadata not addressable in seed**

`SeedRoom` is `{ID, Name, Type, CreatedAt}` only
(`internal/scenario/types.go:76-81`). The message-gatekeeper
large-room gate reads `rooms.userCount` via
`roommetacache.FetchFromMongo`
(`message-gatekeeper/handler.go:232-236`). With no seedable value,
the cached read always sees 0; the gate is never tripped; the
entire large-room post-restriction code path is unreachable from
any scenario.

*Fix shape:* extend `SeedRoom` to
`{ID, Name, Type, CreatedAt, UserCount}`. Engine writes the value
into Mongo on insert. One struct field + one bson serialize;
trivial extension. If other fields surface (e.g.
`restricted`, `externalAccess`) over time, extend the same way.

**T3 — Mongo seed only addresses three collections**

`SiteSeed` knows `users` / `rooms` / `memberships` only
(`internal/scenario/types.go:47-52`). A scenario that needs a
pre-existing `thread_rooms` doc, a `thread_subscriptions` entry,
or an arbitrary field outside the three known shapes has no
declarative path. Forces `pre_fire_scripts` (mongosh) for what
should arguably be in-grammar — and pre_fire_scripts loses
substitution + validator awareness.

*Fix shape:* parallel `mongo_data:` top-level block, structurally
the same shape as `cassandra_data:`:

```yaml
mongo_data:
  - site: site-a
    collection: thread_rooms
    docs:
      - _id: tr-1
        roomId: r-shared
        parentMsgId: m-abc
        createdAt: ${now - 5m}
```

Site-scoped (Mongo is per-site, unlike shared Cassandra).
Substitution + `${now ± d}` tokens apply.

**Note on T2 (one fire per scenario).** That's the present-day
symptom of §2.3 — the DAG-of-tasks proposal is the structural
answer. Not duplicated here.

### 2.8 Scenario organization — arbitrary directory structure

Today `SCENARIO-REFERENCE.md` §1 mandates a flat layout under
`scenarios/drafts/` (and `scenarios/approved/`): one scenario per
file, no subdirectories. That was a self-imposed rule from when
there were three scenarios and grouping was overkill. As the
scenario set grows, authors will want to organize by service
(`messages/`, `rooms/`, `history/`), by phase (`infra-sanity/`,
`pipelines/`, `federation/`), or by domain — without the harness
caring.

Proposal: lift the flat-layout restriction. Allow arbitrary
subdirectory nesting under both `scenarios/drafts/` and
`scenarios/approved/`. Scenario discovery walks recursively rather
than globbing one directory.

**Rules that fall out:**

- **Symmetric across `drafts/` and `approved/`.** CI scoring keys
  off the `status: approved` field, not the directory — directory
  structure is purely author organization.
- **`scenario:` field stays the perf-key.** Loader enforces
  uniqueness of the `scenario:` field across all loaded files at
  validate time. Two scenarios in different subdirs with the same
  `scenario:` field = loader error. This keeps `performance.json`
  history stable across re-organizations (moving a YAML between
  subdirs doesn't churn the perf-key) and preserves the existing
  per-scenario latest/best/worst tracking.
- **`pre_fire_scripts:` resolution is unchanged.** Paths are
  resolved relative to the YAML's directory (already documented at
  `SCENARIO-REFERENCE.md` §4.5) — co-located scripts continue to
  work in any nesting depth.

**Reporting:**

- **Non-interactive (`make local`) report.** On a failing scenario,
  the file path appears alongside the scenario heading in
  `last-run.md`. Operator can `vi` straight to the file without
  hunting.
- **Interactive menu.** Each row shows the path alongside the
  scenario name (e.g. `messages/send-and-persist`). Sort order is
  alphabetic by full path — simplest predictable default; grouping
  by top-level directory with separators is a UX consideration left
  to whoever implements.

**Natural follow-up:** path-prefix filtering. Once subdirs exist,
the obvious next ask is `make local SCENARIOS=messages/` (or the
interactive equivalent `pick all under messages/`) to run a subset.
Not in this proposal but worth naming so it's not invented
ad-hoc later.

**Implementation cost:** half a day, no spec needed.
- Discovery: `filepath.Walk` instead of `filepath.Glob` (~10 lines).
- `scenario.Scenario.SourcePath` already exists for `pre_fire_scripts`
  resolution; reuse it for reporting + menu display.
- Loader: cross-file uniqueness check on `scenario:` field at
  validate time.
- Reporter: append path next to scenario heading on failure
  (one line).
- Interactive menu: show path in each row (~5 lines).

Independent of the envelope+DAG direction in §2.3-§3; ship when
demand arrives.

**Shipped.** `filepath.WalkDir`-based discovery was already in place
(both `findScenarios` and `LoadAllParsedInDir`), so subdirectory
nesting under `scenarios/drafts/` works today. Layered on top:

- `scenario.CheckScenarioNameUniqueness` enforces cross-file
  uniqueness of the `scenario:` field — hard error at validate AND
  at runner startup (before infra boots). `runtime.loadAndCheckUniqueness`
  is the runner-side adapter.
- `ScenarioReport.SourcePath` carries the YAML path through to
  `renderFailureDetails`, which prints `- file: \`<path>\`` under
  each failing scenario heading in `last-run.md`.
- `scenarioRow.relPath` (set from `Cfg.ScenariosDir`) shows the
  per-row file path in the interactive menu next to the scenario
  name.
- `SCENARIO-REFERENCE.md` §1 documents the new layout + uniqueness
  rule; the prior "flat layout" prohibition is gone.

Path-prefix filtering (`make local SCENARIOS=messages/`) named as
the natural follow-up is **not yet shipped** — wait for actual
demand from authors.

### 2.9 Tool primitives must distinguish broken plumbing from absent observation

A class of bug worth naming explicitly: a poller whose substrate
fails (container can't be found, NATS subscription never opens,
Mongo query errors out) produces **zero events**, and the matcher
can't tell that apart from "the behavior under test didn't
happen." The two failure modes look identical at the assertion
layer, and one is benign (the system correctly didn't do the
thing) while the other is the tool lying about what it observed.

This is especially dangerous for `not: true` assertions, which
say "this thing should NOT appear." A broken-plumbing poller
reads zero events, and `not: true` on zero events trivially
passes — **silently green even when the thing did appear.** A
worst-case assertion that "the system should NEVER log
'permission denied'" passes on every run, even if the system logs
it on every request.

The `logs_tail` case (Run 72d4, fixed in commit 5ee1a74) was the
first surfaced instance: the testcontainers-booted stack named
its service containers randomly, the docker-compose label query
returned nothing, the literal-name fallback returned an
unresolvable string, `docker logs <bad-name>` exited non-zero,
and three layers of `//nolint:errcheck` discarded the error. The
visible effect was "0 events polled, Eventually timed out."

**Principle:** every poller that wraps an external substrate
should distinguish three states explicitly:
- **Substrate healthy, observation present** — emit events; matcher does its job.
- **Substrate healthy, observation absent** — emit nothing; matcher's not-true correctly passes.
- **Substrate broken** — surface a hard error (or at minimum a loud `slog.Warn` with the substrate's diagnostic) — never let it look like state 2.

The current poller registry (`internal/runtime/pollers/`) holds
six primitives. Each one wraps an external substrate that can
fail in similar shape. The `logs_tail` fix added the loud-warning
discipline at the `cmd.Wait()` boundary; the same audit should be
done for the other five:

| Poller | Substrate | Audit state |
|---|---|---|
| `reply` | dispatcher's reply buffer | wrong verb (reply only fires for nats_request) — covered in §7.2 pitfall |
| `mongo_find` | Mongo driver query | **audited + loudened.** Dropped `//nolint:errcheck` on `cur.Close(ctx)`; replaced the stringy `strings.Contains(err.Error(), "context")` guard with `errors.Is(err, context.Canceled\|DeadlineExceeded)`; pre-cursor `Find` failure, mid-stream `Close` failure, decode failure, and cursor-iteration failure each carry a distinct warning naming collection+filter and the likely substrate cause. |
| `cassandra_select` | gocql iter | **audited + loudened.** `iter.Close()` was already checked; warnings rewritten to name the query/params/decoded row prefix so substrate breakage (down node, missing keyspace, schema drift, bad CQL) is unmistakable in the log — "zero events" warnings explicitly say `NOT 'absent', they are 'never observed'`. |
| `jetstream_consume` | js.Stream subscribe | **audited + loudened.** Reader: misleading `"jetstream.rooms-canonical:"` error prefix retired (reader is generic across streams); observer-buffer drop now slog.Warn on first + every 100th + on teardown (previously silent — the worst §2.9 shape for a `not: true` assertion since the message arrived but the matcher never saw it); `DeleteConsumer` teardown failure warns (was `_ = `; ErrConsumerNotFound suppressed since InactiveThreshold may have reaped first). Poller: `getOrOpen` and missing-conn warnings rewritten with the "substrate not exercised" / "zero events are NOT 'absent'" idiom, available-site list added on missing-conn. |
| `nats_subscribe` | core NATS subscribe | subscribe error returned by Warm — already loud, modulo same disciplines as logs_tail |
| `logs_tail` | docker logs subprocess | **fixed** in 5ee1a74; see commit body for the trail |

**§2.9 audit complete.** All six pollers either loudened in this
sweep (logs_tail / cassandra_select / mongo_find / jetstream_consume)
or judged already loud (reply / nats_subscribe). The matcher-vs-
system test-placement corollary above remains the durable rule
for new pollers.

**Test-placement corollary.** When verifying *poller behavior*
(does the matcher behave correctly with present-vs-absent events
under not:true?) the right surface is a **Go unit test** under
`internal/matchers/` or `internal/readers/`, not a YAML scenario.
A YAML scenario asserting "this matcher should have failed" is
either (a) perpetually red in the report — noise that authors
have to learn to ignore — or (b) wrapped in awkward
self-referential scaffolding. Go tests run in milliseconds, live
next to the code they exercise, and don't churn the
scenarios/drafts/ confusion matrix. A scenario is for "the system
does X under Y verb fire"; a Go test is for "the matcher returns
Z when given W input."

*Resolved.* The `logs-tail-regression-guard-not-must-fail-when-line-present`
YAML was retired in commit `f218bd7` (tester's cycle commit). The
property it protected — "MatchShape correctly identifies a present
matching event, and Gomega's ShouldNot flips that into a loud
failure" — is now carried by
`internal/runtime/matchshape_test.go::TestMatchShape_SingleEventMatchingExpectedMatches`
and
`internal/runtime/matchshape_test.go::TestMatchShape_NegatedFailureMessageNamesMatchingEvent`,
both with header comments naming the relationship so a future dev
doesn't re-introduce the YAML.

### 2.10 Cross-scenario contamination via in-process service caches

A soundness gap surfaced while authoring the large-room cap
scenarios: the sandbox truncates Mongo + Cassandra between
scenarios but the service containers stay up for the whole run and
retain their **in-process caches** (sub-cache, room-meta-cache,
user-cache — each with ~2-minute TTLs). Within the TTL window, a
second scenario referencing the same cache key (e.g. same
`(account, roomID)` pair) sees the FIRST scenario's projection,
even with byte-identical store state at Setup.

Concrete failure shape (Run 649f → 1982 in the latest cycle):

```
   T0  scenario A fires:
       (alice@r-busy, roles=[member])
       gatekeeper caches this projection (TTL ~2 min)
   
   T1  Sandbox.Setup runs between A and B
       drops Mongo collections, truncates Cassandra
       ✓ store state byte-identical to T0
       ✗ gatekeeper still holds (alice@r-busy, [member]) in memory
   
   T2  scenario B fires:
       expects (alice@r-busy, roles=[owner])
       Mongo says [owner] (Setup re-seeded it)
       gatekeeper reads its own cache, sees [member], DOESN'T re-fetch
       canBypassLargeRoomCap → no owner role → caps the post
       ✗ scenario B fails, but for the wrong reason
   
   reorder scenario A and B → B passes for the wrong reason
```

This is the worst class of bug a test tool can have — silent,
order-dependent, and falsely-greens negative scenarios just as
easily as it falsely-reds positive ones. Logged as F-009 in the
findings doc.

**The structural fix is in chat-app code.** Either:
- Env-driven cache TTL override per service (`*_CACHE_TTL=0` in the
  test stack), or
- Admin cache-flush endpoint each cache-holding service exposes
  (NATS or HTTP).

Both belong to the chat-app team; the test tool's role is the
mitigation discipline below until one lands.

**Tool-side mitigation discipline (until F-009 closes):**

Scenarios that touch the same actor + same cache-keyed entity
across a run must use unique cache keys. In practice that means:

| Cache | Key shape | Unique-key discipline |
|---|---|---|
| Gatekeeper sub-cache | `(roomID, account)` | Different `roomID` across scenarios that share an actor |
| Gatekeeper room-meta-cache | `roomID` | Same — different `roomID` |
| User-cache | `userID` | Different actor alias (which → different `userID`) |

Worked: every gatekeeper-large-room-* scenario uses a unique room
ID (`r-busy-member`, `r-busy-owner`, etc.) rather than reusing
`r-busy` across alice's roles. Adds 1 line per scenario; avoids
the cache stale-read entirely.

The discipline is a footgun: any author who forgets it triggers
the contamination, silently. Document it loudly in AUTHORING.md
when this finally hits a real scenario that can't easily use unique
keys (e.g. scenarios coordinating across the same room ID for
federation-receive testing — there, the chat-app fix is the
unblock).

**Why this lives in plan-ahead and not just findings.md:** because
the discipline change is OURS. F-009 is for the chat-app team's
ruling on the structural fix; this section is the work the test
tool has to do in parallel — AUTHORING.md note, possibly a loader
check that flags duplicate `(account, roomID)` pairs across
scenarios in the same run, possibly a lint rule. Defer the
implementation until the discipline is broken by a real scenario
that can't easily route around it; until then, the warning in
AUTHORING.md is enough.

**Estimated implementation:**
- AUTHORING.md note: 15 min.
- Loader cross-scenario duplicate-key check: ~1 hour (walks all
  loaded scenarios, builds the cache-key tuple per scenario, errors
  if any tuple appears twice).

Both ship-when-demand-arrives.

**Status update — collection-side check complete.** The plan-ahead
worklist's "per-collection drop-list audit" item is shipped. A
chat-app `db.Collection(...)` sweep confirmed `sandboxOwnedCollections`
covers every collection chat-app services WRITE to in normal
operation. Three read-only collections (`apps`, `bot_cmd_menu`,
`custom_emojis`) are intentionally excluded — they're populated by
external admin tooling, not by chat-app services during a request
cycle. One latent collection (`room_data_keys` from `pkg/atrest`)
is excluded today because `ATREST_ENABLED=false` in the multi-site
stack; if at-rest ever enables, the drop list must extend.

The audit history is recorded verbatim in the `sandboxOwnedCollections`
comment block (`internal/runtime/sandbox.go`) so a future maintainer
can re-run the same survey and check for drift.

**Loader-side cache-key check (warn-only)** shipped in `e8bd46b` —
fires three warnings on the current 23-scenario set. Conversion to
hard error pending tester-coordinated scenario rename pass.

---

## 3. The model these proposals converge to

```yaml
scenario: room-creation-survives-arbitrary-chaos

sites:                              # per-site (Mongo-backed) data — from multi-site spec
  site-a:
    seed:
      users:
        alice: { verified: true }
        bob:   { verified: true }

cassandra_data: []                  # shared cluster — from multi-site spec

input:                              # tasks defined once (new — §2.3)
  - id: create
    verb: nats_request
    subject: chat.user.${alice.account}.request.room.${siteA}.create
    payload: { name: Engineering }
    credential: ${alice.credential}
  - id: join
    verb: nats_request
    subject: chat.user.${bob.account}.request.room.${siteA}.join
    payload: { roomId: ${create.reply.body_json.roomId} }   # data dep on create
    credential: ${bob.credential}
  - id: send_msg
    verb: nats_request
    subject: chat.user.${alice.account}.room.${create.reply.body_json.roomId}.${siteA}.msg.send
    payload: { text: "hi" }                                  # data dep on create
    credential: ${alice.credential}

input_order:                        # orderings to stress (new — §2.6)
  case_a: create >> [join, send_msg]      # join + send racing each other
  case_b: create >> join >> send_msg      # serial happy path
  case_c: create >> send_msg >> join      # send-before-join (negative-ish)

expected:
  positive:                         # envelope, not per-case (new — §2.2)
    - { location: reply,      match: { task: create, body_json: { status: accepted } } }
    - { location: reply,      match: { task: join,   body_json: { status: accepted } } }
    - { location: mongo_find, match: { _site: site-a, name: Engineering, members: { contains: ${bob.id} } } }
  negative:
    - { location: reply, match: { task: send_msg, body_json: { error: "*" } } }
    - { location: mongo_find, match: { _site: site-a, name: Engineering, last_message: "hi" }, not: true }

chaos:                              # generator (new — §2.2) — common across orderings
  iterations: 20
  kinds: any                        # picks from catalogs/mishaps/ per iteration
  concurrency: 1                    # >1 for race tests
  seed: env:CHAOS_SEED              # reproducibility
```

Total per scenario: `|input_order| × chaos.iterations` executions.
With 3 orderings × 20 iterations = 60 stress tests from one
hand-authored scenario.

**Runtime** (outer = ordering case, inner = chaos iteration; both fresh).
Per §5 Q1+Q2 (fire-anyway semantics), task or mishap failures do NOT
halt downstream tasks — pollers keep collecting through the timeout
and the envelope is evaluated against the full accumulated observation
set. Per Q11 (multi-mishap), a chaos pick is a LIST.

```
for each entry in input_order:                    ◄── outer: ordering case
   for iter in 1..chaos.iterations:               ◄── inner: chaos iteration
      fresh_state()         ◄── §3.1: drop+reseed Mongo + truncate+reseed Cassandra
                                 + purge whole JetStream streams + reopen stateful pollers
      picks = chaos.select(scenario.seed, iter)   ◄── LIST per Q11; random|exhaustive|none per Q12
      for each pick: go mishap.Apply(pick)        ◄── parallel chaos
      execute DAG per this ordering              ◄── fire-anyway; failures don't halt
      Eventually(...).Should(Or(MatchEnvelope(positive), MatchEnvelope(negative)))
      record (ordering, iter, picks, outcome, raw events on UNDEFINED)
```

### 3.1 "Fresh state" — the rigorous definition

Per the discussion, every iteration needs:

| Surface | Action | Notes |
|---|---|---|
| Mongo collections | drop + re-apply `seed.<site>.seed.{users,rooms,memberships}` | from existing Setup steps 6+11 |
| Cassandra tables | truncate + re-apply top-level `cassandra_data` | from existing Setup steps 7+12 |
| User profile docs | re-apply | part of Mongo reset |
| JetStream stream contents | `js.PurgeStream()` for each scenario-touched stream | NEW — not currently called |
| Stateful poller buffers (jetstream_consume, nats_subscribe, logs_tail) | close + reopen | each has `Close()` today; reopening is one extra call |
| User JWTs / nkeys / Placeholders | **keep cached** | JWT mint is the expensive Setup step |
| Toxiproxy state | reset | already done between cases today |
| Valkey room keys | keep cached | only re-seed if `seed.room-keys.json` changed |

Cost estimate: ~150-250ms per iteration. 20 iterations × 11 scenarios
adds ~50s to a ~30s baseline run. Acceptable for the property-test
coverage gain.

### 3.2 Framings established in the discussion

**(a) "Chaos" and "mishap" are the same thing.** The YAML uses
`chaos:` as the author-facing block name. The Go package
`internal/mishap/` keeps its name for now (planned rename when the
spec lands). Documentation and reporter output should standardize on
**chaos** as the user-facing term. `mishap` is internal vocabulary
only.

**(b) Cross-site is one unified system, not two parallel systems.**
The multi-site spec (`2026-06-03-integration-suite-multisite-design.md`)
correctly describes per-site Mongo/NATS/Valkey/Toxiproxy with shared
Cassandra, bridged by a NATS supercluster. But the **execution
model** in the envelope+DAG world is:

- One DAG executes against the unified system per iteration — NOT
  once per site, NOT interleaved per site.
- Seed data per site DEFINES what each site holds at iteration start;
  the DAG acts on the unified system; envelopes describe expected
  cross-site state.
- `_site:` in match filters routes assertions to the right backend
  (e.g., "the federation tail must land in site-b's Mongo"), but the
  iteration itself is a single execution against the unified whole.

This is sharper than the multi-site spec's prior framing (which
implied per-site fan-out at the execution level). When the
envelope+DAG spec gets written, the multi-site spec should be
updated to align with this unified-system view.

**(c) Pollers collect throughout the timeout, not first-match.**
Today's `Eventually(...).Should(MatchShape(...))` short-circuits on
first success — fine for single-task scenarios. The envelope model
needs different semantics: pollers keep accumulating events through
the full timeout window, and the envelope is evaluated against the
total observation. This affects `MatchShape` (or its envelope-aware
replacement) — needs explicit handling in the spec.

---

## 4. Sequencing — which spec first?

Two separate specs were anticipated, each non-trivial:

| Spec | Scope | Status |
|---|---|---|
| **Multi-site** | Spatial fan-out, NATS supercluster, per-site Mongo, shared Cassandra, federation Sources, `_site` provenance | **Implemented** as `tools/integration-suite-multisite/`. See `docs/integration-suite-multisite-findings.md` for the chat-app-team-facing findings (F-001 OUTBOX owner, F-002 federation topology). Single-site archived at `tools/archived/integration-suite/`. |
| **Envelope + DAG + chaos engine** | Replace `cases:` with `input: [DAG]` + `expected: {positive,negative}` + `chaos:` loop; per-iteration fresh state | **Not yet specced** — this doc remains the launchpad |
| **Seed-grammar extensions (T1, T3)** | Concrete in-grammar fixes for room metadata and arbitrary Mongo doc seeding (see §2.7) | **Surfaced; not shipped.** Ship when a scenario demands either. Cheaper than envelope+DAG; independent of it. |
| **Scenario organization (§2.8)** | Allow arbitrary subdirectory nesting under `scenarios/drafts/` and `scenarios/approved/`; show path in failure reports + interactive menu | **Shipped.** Recursive discovery + `scenario:`-field uniqueness check (validate + runner startup) + path in `last-run.md` failure detail + path column in interactive menu. Path-prefix filtering is the named follow-up; ship on demand. |
| **Substrate-error audit (§2.9)** | Drop `//nolint:errcheck` suppressions at substrate boundaries across the six pollers; convert silent failures to loud `slog.Warn` (or hard errors). `logs_tail` done; five more to audit. | **One done; rest pending.** 1-2 hours per remaining poller. Independent of any spec direction. |
| **Cross-scenario cache isolation (§2.10)** | Tool-side mitigation discipline (unique cache keys per scenario) + optional loader check for duplicate `(account, roomID)` pairs. Pairs with chat-app finding F-009 for the structural fix. | **Discipline named; check not built.** ~15 min for the AUTHORING.md note; ~1 hour for the loader check. Ship when a scenario can't easily route around the discipline. |

**Sequencing — the original "which spec first" question
is partially answered.** Multi-site shipped first (case-based world
intact) because the federation question was urgent. The
envelope+DAG model is now the natural next lift — it stacks on the
multi-site shape rather than competing with it. Original intuition
("envelope+DAG first; multi-site folds in afterward") is moot.

**Open question on order:**
- Building multi-site FIRST onto the case-based world is more code that
  the envelope+DAG model would then simplify (cases→iterations rewrite).
- Building envelope+DAG FIRST keeps the existing single-site stack;
  multi-site lands later into a smaller surface.
- **Intuition:** envelope+DAG first; multi-site folds in afterward
  without rework. But this is a sequencing call for the human, not me.

---

## 5. Open questions — status as of 2026-06 discussion

Most are now answered; remaining ones flagged `[DEFERRED]` need a
focused conversation when the spec gets written. **Legend:**
✅ decided · ⏸ deferred · 🟡 tentative (proposed default, needs final confirmation)

### Model
1. ✅ **DAG semantics under task failure** — **fire anyway.** If `t1`'s
   reply is an error, `t2` still fires. Pollers keep collecting
   throughout the timeout; the envelope is evaluated against the full
   accumulated observation set. *Rationale: tasks are user actions,
   not procedural dependents; the runner doesn't second-guess.*
2. ✅ **DAG semantics under mishap** — **same as #1, fire anyway.**
   Tasks are independent operations from the user's perspective; a
   mishap-induced timeout on `t1` doesn't skip `t2`.
3. ✅ **Per-task assertions.** Matcher grammar adds a `task: <id>`
   selector inside `match:` shapes — `match: { task: create,
   body_json: { status: accepted } }` filters to that task's events.
4. 🟡 **Substitution from upstream** — **reply-only.** `${<task>.reply.…}`
   pulls from the task's synchronous reply. Reading from emitted
   events (`${t1.events.MESSAGES_CANONICAL_site-a[0].body}`) was
   considered and rejected; no scenario today needs it. *Pending
   final confirmation if a future scenario does need it.*
5. ✅ **No shorthand for `input_order`** — always required, even for
   single-task scenarios. Grammar stays simple and consistent.

### Input order (§2.6)
6. ✅ **Notation parser.** Tokenize on `>>`; recognise `[...]` groups.
   Reject malformed (nested brackets, undefined task ids).
7. ✅ **Data-dep inference.** Runner walks every task's
   payload + subject + credential for `${<task>.reply.…}` tokens to
   derive hard data-dep edges. Each entry in `input_order` is checked
   against these at scenario-load.
8. ✅ **`input_order:` is mandatory.** No implicit/default ordering;
   loader rejects scenarios missing the block.
9. ✅ **No `input_order: auto`** flag. Authors always list orderings
   explicitly; orderings should be deliberate stress choices, not
   exhaustive permutations of seed data.
10. 🟡 **No per-ordering chaos override.** Common `chaos:` at scenario
    level applies to all orderings uniformly. *Pending final
    confirmation; YAGNI for v1.*

### Chaos engine ("chaos" and "mishap" are interchangeable — see §3.2)
11. ✅ **Multiple mishaps per iteration from the start.** No v1/v2
    phasing. Chaos picker returns a LIST per iteration; runner spawns
    N concurrent `Apply` goroutines.
12. ✅ **Three modes: `random` (default), `exhaustive`, `none`.** Set
    via `chaos.mode:`. `none` is the baseline (no-mishap) sweep.
    Random selection uses Go stdlib `math/rand/v2` — no new dep.
13. ⏸ **Targeted vs untargeted chaos.** Two paths under consideration:
    a) **Smart-targeted** — derive chaos targets from the locations a
       scenario actually touches (envelope filters mention `_site:
       site-a` → target site-a's backends). Requires scenario-walk
       analysis at load time.
    b) **Random** — `chaos.kinds: any` picks from the whole catalog,
       no targeting awareness.
    *User leaning: explore smart-targeted; fall back to random if
    complexity is high. Needs focused conversation.*
14. 🟡 **Reproducibility via `CHAOS_SEED=42`.** Likely yes — Go stdlib
    `rand/v2.New(rand.NewPCG(seed, 0))` is one line for deterministic
    picks. Tolerate residual poller-timing flake. *Tentative.*

### Fresh state
15. ✅ **Valkey stays persistent across iterations.** Production room
    keys persist; no current scenario tests key rotation mid-flow.
    Reset only if/when such a scenario appears.
16. ✅ **Purge whole streams between iterations.** One
    `js.PurgeStream()` call per scenario-touched stream — simpler
    than subject-level filtering.

### Reporter
17. ⏸ **Confusion matrix dimensions.** Defer until chaos shape settles
    (Q13). Likely `(ordering × chaos_set) × envelope_matched`.
18. ✅ **Minimal logging.** Errors as they appear, exact event shape
    unprocessed, with location. No summarization, no fancy
    formatting. Reporter is a pass-through, not a formatter.

### Multi-site composition
19. ✅ **One DAG per iteration against the unified 2-site system** —
    NOT once per site, not interleaved. Cross-site is one system; see
    §3.2 framing. Seed data per site defines what each site holds;
    input acts on the union; envelopes account for federation effects
    via `_site:` filters.
20. ⏸ **Chaos targeting in multi-site.** Defer alongside Q13. Smart
    targeting would only chaos backends the scenario actually
    touches; random picks from any site's pool. *User leaning:
    smart targeting; needs focused conversation.*

### Migration
21. ✅ **Rewrite the 11 existing scenarios from scratch.** No
    mechanical migration; the model has changed enough that
    re-authoring per the new grammar is cleaner than translation.
22. ✅ **No special handling for single-fire scenarios** — covered by
    Q5/Q8. Author writes `input: [<one-task>]` + explicit
    `input_order: { case_a: t1 }`.

---

## 6. Things explicitly NOT in scope

- Replacing the 6 universal pollers — they're correct as-is.
- New chaos kinds beyond what's needed for multi-site (the
  `outbox-source-pause-500ms` from the multi-site spec).
- Load testing — that lives in `tools/loadgen/`.
- A scenario authoring UI — YAML stays the source of truth.
- Cross-scenario state sharing — each scenario stays independent.
- **Mechanical migration tooling for the 11 existing scenarios.** Per
  Q21, authors rewrite from scratch under the new model. No migration
  binary needed.
- **Auto-generated DAG sequences from a domain model.** Authors
  declare orderings (§2.6); the runner never invents legal task
  combinations. That's a different tool category (stateful
  property-based testing) — out of scope here.
- **Implicit/shorthand `input_order:`.** Per Q5/Q8, the block is
  always required; no defaults inferred.

---

## 7. Glossary (terms used above)

- **Case** — today's per-scenario test variant. Goes away in the
  envelope+DAG model; replaced by **iteration**.
- **Iteration** — one chaos pick (LIST of mishaps) + one DAG execution
  + one envelope evaluation. Repeats N times per ordering case.
- **Ordering case** — a named entry in `input_order:` declaring one
  topology to stress (e.g. `case_a: t1 >> [t2, t3]`). Total scenario
  executions = |orderings| × iterations.
- **Envelope** — a list of `expected[]` blocks describing one
  acceptable outcome (positive or negative). Replaces `expected[]`
  attached to cases.
- **DAG** — directed acyclic graph of tasks forming the scenario's
  input. Each task is a single verb+subject+payload+credential.
- **Task** — one node in the DAG. Analogous to one fire in today's
  model.
- **Chaos / mishap** — interchangeable. Chaos is the user-facing
  term (YAML block name, reporter column); mishap is the internal Go
  package name (`internal/mishap/`) until renamed.
- **Fresh state** — the data-surface reset between iterations.
  Defined precisely in §3.1.
- **Unified-system view** — multi-site execution model: one DAG per
  iteration against the combined 2-site system, not once per site.
  See §3.2(b).
- **Fire-anyway semantics** — task failures and mishap-induced
  timeouts do NOT halt downstream tasks. Pollers keep collecting
  through the timeout. The envelope evaluates the full accumulated
  observation. See §5 Q1, Q2.
- **Fan-out (spatial)** — multi-site pollers querying both sites
  per assertion. From the multi-site spec.
- **Fan-out (temporal)** — multiple iterations of the same DAG
  under different chaos. From this doc.
