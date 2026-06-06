# Integration suite — multi-site design

> ⚠️ **This is NOT a production tool.** It's a single-purpose test
> harness for the chat backend. The goal is "scenario YAML → run →
> report." No flexibility, no extensions, no production-grade
> reliability. If a feature isn't needed by a scenario, it doesn't
> get built. If something fails it's loud and obvious.

> 🔀 **FORK, not extension.** The single-site tool at
> `tools/integration-suite/` is **frozen** — no changes for this
> work. The multi-site tool lives at a NEW path:
> `tools/integration-suite-multisite/`. Bootstrap:
> `cp -r tools/integration-suite tools/integration-suite-multisite`,
> then edit the copy. Two tools coexist; the team accepts the
> duplication cost. Bug fixes after the fork must be applied to
> both deliberately. Throughout this doc, every code path / file
> path refers to the **multi-site copy** unless explicitly noted
> otherwise.

**Status:** approved design / not yet implemented. Captured
2026-06-03 during a brainstorming session; refined 2026-06 during
the developer's clarification round (see §11).

**Goal:** stand up a 2-site fork of the integration suite so
scenarios can seed data on either site, fire verbs against either
site, and assert cross-site federation (OUTBOX → INBOX JetStream
sourcing). The two sites form **one unified system under test** —
not two parallel systems. Multi-site exists so federation flows
(inbox/outbox, broadcast cross-site) are exercisable.

**The change in three sentences:**
1. NATS becomes a 2-node supercluster (gateway connections, ONE
   shared trust chain across both — see §6.1.1).
2. Mongo + Valkey + services duplicate per site (each site has its
   own); Toxiproxy and Cassandra are shared single containers.
3. Cassandra stays one shared cluster (production multi-DC model).

> 🚧 **MISHAPS / CHAOS ARE DISABLED for this work.** Get the 2-site
> system running and asserting federation FIRST. Do NOT touch the
> mishap subsystem (`internal/mishap/`, `mishap:` / `mishap_site:`
> YAML fields, the Toxiproxy chaos engine) as part of multi-site.
> All §5.3 content (FactoryContext changes, `cassandra-partition`
> site-scoping, `outbox-source-pause` federation mishap) is
> **deferred** until plain federation works end-to-end. Scenarios
> ship with no `mishap:` blocks. Toxiproxy still boots passively
> in the connection path, but no chaos is injected. Re-enable
> mishaps as a follow-up once green.

**Non-goals:**
- N>2 sites (always exactly `site-a` + `site-b`).
- Per-scenario site declaration (the 2-site infra is always
  running; scenarios declare what data they need, not what infra
  to spawn).
- Manual-stack support for multi-site — `docker-local/` compose
  files stay single-site. Multi-site requires `USE_INFRA=true`.
- Backwards compatibility with the single-site YAML grammar —
  multi-site scenarios are written from scratch in the new shape;
  single-site scenarios stay in the single-site tool, unchanged.
- Sharing code between the two tools at the package level. The
  fork is full; cross-tool imports are forbidden.

---

## 1. Design decisions (the brainstorm summary)

| # | Decision | Rationale |
|---|---|---|
| 1 | **Full multi-site parity.** Every scenario can run with seed users + rooms on any site, request can fire at any site, assertions can target any site's backends. Cross-site federation becomes one assertion type among many. | A federation-only mode would carve out a tiny corner of testing surface and leave most cross-site bugs unreachable. Parity is the right primitive. |
| 2 | **Always exactly 2 sites** (`site-a`, `site-b`). | Covers all federation patterns (peer-to-peer is the same shape for N=2 and N=3); infra cost scales linearly and we already accept 2x. 3+ sites buys little new coverage. |
| 3 | **YAML data grouped under `sites:` block** for per-site (Mongo-backed) data — each site nests its own `seed:` (users / rooms / memberships). **`cassandra_data` lives at scenario top level** since the Cassandra cluster is shared. | Cleanest grammar; explicit ownership; one parse path. Breaks all 11 existing scenarios — accepted as part of this work. |
| 4 | **Verb routing is implicit from credential.** Credentials carry their site at user-mint time; dispatcher reads it. No `site:` field on cases. | Authors think "alice creates a room" not "fire on NATS-A". The site fall-out is data-driven. |
| 5 | **`site:` field on input and every site-scoped `expected[]` block.** Pollers take site directly; no fan-out, no `_site` tag, no matcher change. (Round 4 — supersedes the original fan-out design.) | Author writes which site each block targets; pollers stay 1× backend per poll. |
| 6 | **Per-site duplication of NATS / Mongo / Valkey / 9 services. Cassandra AND Toxiproxy are shared (single container each).** Plus a NATS supercluster gateway. | NATS+Mongo+Valkey are independent per site (the federation boundary). Cassandra is multi-DC by design — one cluster spanning sites. Toxiproxy is a test-only artifact (not in production); one container hosts site-named proxies, so chaos is still site-scoped without a second container. |
| 7 | **Mishap site selection via `mishap_site:` field**, defaulting to the firing site. | Mishap kind stays generic; new sites don't double the catalog. Federation-bridge mishap (`outbox-source-pause-500ms`) is one new kind. |
| 8 | **NATS supercluster (gateway connections)** between NATS-A and NATS-B, not leafnodes or a Go bridge. | This is the production topology. JetStream Sources work natively across the gateway. |

---

## 2. Architecture overview

```
                   ┌──────────── Sandbox (one per scenario) ────────────┐
SCENARIO YAML      │                                                    │
   │               │  Setup (per-site loops over 13 steps + 1 federation):
   ▼               │     for each site in [site-a, site-b]:             │
sites:             │        validate seeds, drop colls, truncate        │
  site-a: {...}    │        Cassandra, mint users via that site's       │
  site-b: {...}    │        auth-service, insert profiles, seed         │
   │               │        rooms/Cassandra rows                        │
   ▼               │     Build Placeholders combining users across sites│
   runScenario     │     Apply federation Sources (idempotent, once)    │
                   │                                                    │
                   │  Single fire + asserts:                            │
                   │     dispatcher.Fire(input)                         │
                   │       → routes by input.site                        │
                   │     pollers take `site:` from expected[] block;    │
                   │       one backend hit per poll, no fan-out          │
                   │     MatchShape unchanged                            │
                   └────────────────────────────────────────────────────┘

INFRA (USE_INFRA=true): two parallel stacks bridged by NATS supercluster,
                        sharing one Cassandra cluster across both sites
   site-a stack: NATS-A + Mongo-A + Valkey-A + Toxiproxy-A
                 + 9 services (each configured SITE_ID=site-a, pointed at -A backends)
   site-b stack: NATS-B + Mongo-B + Valkey-B + Toxiproxy-B
                 + 9 services (each configured SITE_ID=site-b, pointed at -B backends)
   shared:       Cassandra (one cluster, one keyspace — mirrors production
                 multi-DC Cassandra; both sites' message-worker / history-service
                 connect to it via their respective Toxiproxies)

   NATS supercluster:
       nats-site-a:7222 ◄═══ gateway connection ═══► nats-site-b:7222
   JetStream Sources (applied post-services-up by the suite, not bootstrap):
       OUTBOX_site-a → INBOX_site-b (filter: outbox.site-a.to.site-b.>)
       OUTBOX_site-b → INBOX_site-a (filter: outbox.site-b.to.site-a.>)
```

**Key code-shape change:** `BuiltinDeps` flips from single handles to a
per-site map for site-scoped backends; Cassandra stays a single handle
since the cluster is shared.

```go
// before
type BuiltinDeps struct {
    MongoDB   *mongo.Database
    AdminConn *nats.Conn
    Cassandra *gocql.Session
    // ...
}

// after
type BuiltinDeps struct {
    Sites     map[string]*SiteBackends  // "site-a" → handles, "site-b" → handles
    Cassandra *gocql.Session            // shared across both sites
    // ...
}

type SiteBackends struct {
    MongoDB   *mongo.Database
    AdminConn *nats.Conn
    AuthURL   string
}
```

Toxiproxy is NOT in `SiteBackends` — it's a single shared container
(one admin URL, site-named proxies). And chaos is disabled for this
work anyway (see the deferral note above), so no Toxiproxy handle is
threaded into the pollers/sandbox during multi-site bring-up.

Every poller that touches a site-scoped backend (Mongo, NATS subjects,
JetStream streams, container logs) becomes "for each site in
deps.Sites: …" or "look up deps.Sites[targetSite]". `cassandra_select`
stays a single query against the shared cluster.

---

## 3. YAML grammar

### 3.1 Scenario shape

**One scenario = one input + one set of expected outputs.** No
`base_input`/`case.input` distinction; no `cases:` array. Each scenario
file is a single fire-and-assert unit. Variants are separate files.

```yaml
scenario: alice-creates-room-federates-to-site-b
source: room-service/handler.go:296-374 + inbox-worker/handler.go:80-150
status: draft
tag: positive                        # ─── positive | negative (confusion-matrix axis)

sites:                               # ─── per-site data (Mongo-backed)
  site-a:
    seed:
      users:
        alice: { verified: true }
      rooms:
        - { id: r-eng, type: channel, name: Engineering }
      memberships:
        alice:
          - r-eng
  site-b:
    seed:
      users:
        bob: { verified: true }

cassandra_data: []                   # ─── scenario-level (Cassandra is shared)

input:                               # ─── ONE input block (no base_input, no cases)
  site: site-a                       # required: fire FROM this site's NATS
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Engineering Chat
    users: [ "${alice.account}" ]
  credential: ${alice.credential}

expected:                            # ─── all assertions for this scenario
  - location: reply
    # no site: — reply is intrinsic to the fire
    match:
      body_json: { status: accepted }

  - location: mongo_find
    site: site-a                     # required for site-scoped pollers
    args: { collection: rooms, filter: { name: "Engineering Chat" } }
    match:
      name: Engineering Chat
      createdBy: ${alice.id}

  - location: mongo_find
    site: site-b                     # federated tail
    args: { collection: rooms, filter: { name: "Engineering Chat" } }
    match: { name: Engineering Chat }
    timeout: 10s                     # federation has a few-hundred-ms tail
```

**The `site:` field is the only YAML signal of which site to use.**
No subject-parsing, no credential-site lookup, no payload tagging,
no fan-out. The author writes which site each block targets.

**If a scenario needs different inputs to be tested, write a separate
scenario file.** The simplicity of "one scenario = one fire + one
envelope" is the whole point.

### 3.2 Invariants

- `sites:` block declares **per-site (Mongo-backed) data**. Cases are
  cross-site by nature.
- `cassandra_data` lives at **scenario top level**, not under any site
  — Cassandra is a single shared cluster across both sites
  (production multi-DC model). One truncate, one set of inserts, one
  query path.
- Aliases (`alice`, `bob`) are **scenario-global** — each alias is unique
  across all sites. `${alice.account}` is unambiguous because the loader
  builds one `alias → user` map after walking both `sites:` entries.
- **`site:` field is REQUIRED on `input` and on every site-scoped
  `expected[]` block.** Site-scoped pollers: `mongo_find`,
  `jetstream_consume`, `nats_subscribe`, `logs_tail`. Loader rejects
  `site:` on `reply` (intrinsic to the fire) and on `cassandra_select`
  (shared cluster — no choice to make).
- Loader rejects `site:` values other than `site-a` or `site-b`.
- A scenario that needs only one site simply omits the other site
  key from `sites:` and never names it on any block.
- **One `input:` block per scenario, one `expected:` list per
  scenario.** No multi-fire-per-scenario. To test a different fire,
  write a different scenario file.

### 3.3 Substitution tokens (delta from today)

Routing uses the YAML `site:` field directly (§3.2); substitution
tokens are unchanged except for the removals below.

| Token | Status | Resolves to |
|---|---|---|
| `${<alias>.account}` `.id` `.jwt` `.nkey` `.credential` | unchanged | Looks up the alias across both sites' seed.users |
| `${site}` | **removed** | No longer needed — authors write the literal site name in subjects (e.g. `chat.user.${alice.account}.request.room.site-a.create`). Loader rejects `${site}`. |
| `${service.<name>.credential}` | **removed** | Service creds are dropped from YAML entirely (see §3.4). Loader rejects with a clear error. |
| `${now}`, `${now ± d}`, `${input.*}`, `${bucket(<col>)}`, `$auto` | unchanged | — |

No `${siteA}`/`${siteB}`/`${alias.site}` tokens. Site is a structural
YAML field, not a substitution. Authors write `site-a` and `site-b`
literally where they appear in subjects.

### 3.4 Service credentials dropped from YAML

Today the suite exposes `${service.backend.credential}` so scenarios
can fire as the operator (used by `room-worker-persists-canonical-create.yaml`
and `room-worker-rejects-missing-valkey-key.yaml` to publish directly
to canonical streams).

**Under multi-site this token is removed.** Rationale:

- The 2-site setup tests **user-driven** behavior — alice fires from
  her home site, the system internally federates, we observe both
  sides. Admin-scoped operations are not the test surface.
- The two existing scenarios above test room-worker in isolation by
  bypassing room-service. Under the new model they're rewritten to
  fire through room-service via alice's user credential (more
  black-box, less internal-pipeline-specific).
- The single existing `docker-local/backend.creds` is mounted into
  BOTH NATS containers (per §6.1.1's single-trust-chain model).
  It's consumed by NATS server config when needed; never referenced
  from YAML.

**Code impact:**
- Delete `buildServiceCreds` (`runner.go:421`).
- Drop `Services map[string]Credential` field from `Sandbox`
  (`sandbox.go:127`) and `Context` (`runner_scenario.go:50`).
- Remove the `${service.<name>.credential}` substitution path; loader
  rejects with a migration hint.
- `pickCredential` (in what's now scenario-runner code, post case
  removal) simplifies — only handles user-cred lookup.

---

## 4. Sandbox.Setup

The setup loop becomes per-site for every data-touching step.
Cross-site state (Placeholders) builds once after both sites finish.
One new step adds the federation bridge.

```
Step          | Single-site today           | Multi-site
──────────────┼─────────────────────────────┼─────────────────────────────────────
1  validate user-effect flags     same      | iterate over both sites' seed.users
2  validate room-subscription     same      | iterate over both sites' seed.rooms
3  validate cassandra_data        same      | unchanged — scenario-top-level field,
                                              one validation pass against shared
                                              keyspace
4  capture sb.StartTime           same      | single — both sites share T_open
                                              (federation latency measured against it)
5  Chaos.Reset                    same      | DEFERRED — chaos disabled for this
                                              work. Step is a no-op / skipped until
                                              mishaps are re-enabled.
6  Drop Mongo collections         same      | for each site: drop site.Mongo collections
7  Truncate Cassandra tables      same      | unchanged — one TRUNCATE per
                                              sandbox-owned table against the
                                              shared cluster
8  Materialize SeedUsers          same      | for each site: mint JWT via site.AuthURL
                                              alias→user map built across BOTH sites
9  Insert profile docs            same      | for each site: insert into site.Mongo
10 Build Placeholders             same      | (single) one combined map;
                                              ${<alias>.site} resolved from alias→site map
11 Insert seeded rooms            same      | for each site: insert into site.Mongo
12 Insert seeded Cassandra rows   same      | unchanged — one set of INSERTs
                                              against the shared cluster
13 Build poller registry          same      | site-scoped pollers see deps.Sites map;
                                              cassandra_select sees the shared
                                              deps.Cassandra handle
```

**Federation Sources are infra's concern, not Sandbox's.** They're
attached by `infra.Up` (step 6 in §6.1) under `USE_INFRA=true`.
Sandbox.Setup makes no precondition check. If federation isn't
attached, scenarios fail at assertion time with "no events on
site-b" in the report — clear enough signal.

### 4.1 Cross-site reference validation

Memberships and `addedUser` references in payloads can name aliases on
either site. The Sandbox's validator now checks:

- Every alias mentioned anywhere resolves to **exactly one** site's
  `seed.users`.
- `site:` field is present where required (`input`, every
  site-scoped `expected[]` block) and absent where forbidden
  (`reply`, `cassandra_select`); uses one of the two known site
  names.
- (`mishap_site:` validation is deferred with the rest of the mishap
  subsystem.)

All fire at scenario-load time before any I/O, matching today's
single-site validators.

### 4.2 Teardown

- Chaos.Reset on both sites.
- Pollers' Close iterates per-site cleanups.
- Federation Sources stay attached across scenarios (stream-level
  config, not per-test state).

### 4.3 Cost estimate

Setup time roughly doubles for I/O-bound steps. Today: ~150-250ms.
Multi-site: ~300-500ms. Federation Sources attach is a one-time ~50ms
hit at first scenario.

---

## 5. Runtime: dispatcher, verb executors, pollers, mishaps

### 5.1 Verb executors hold per-site URL maps

Per-site routing lives in the **verb executor** (not the dispatcher;
today's dispatcher doesn't hold a NATS conn at all). The site is
read from `input.Site` (the YAML `site:` field), NOT from the
credential — the YAML's `site:` is authoritative.

```go
// internal/verbs/types.go — Input carries Site
type Input struct {
    Site        string   // NEW — from YAML input.site
    Subject     string
    Payload     []byte
    Credential  Credential
    Traceparent string
}

// internal/verbs/nats_request.go — per-site URL map
type NATSRequest struct {
    SiteURLs map[string]string   // {"site-a": "...", "site-b": "..."}
    Timeout  time.Duration
}

func (n *NATSRequest) Execute(ctx context.Context, in *Input) Outcome {
    url := n.SiteURLs[in.Site]
    if url == "" {
        return Outcome{Err: fmt.Errorf(
            "nats_request: no NATS URL for site %q", in.Site)}
    }
    conn, _ := nats.Connect(url, authOpt, ...)
    // ... rest unchanged
}
```

`JetStreamPublish` mirrors the same shape.

**Dispatcher unchanged** — it still does substitute-subject +
substitute-payload + executor lookup + Inject into ReplyReader. It
just passes `in.Site` through to the executor.

**Credential is NOT site-tagged.** The shared trust chain (§6.1.1)
means alice's JWT validates on either NATS. Site routing is
code-driven via `in.Site`, set from YAML. No `Credential.Site`
field needed.

**Reply is still one logical location:** the dispatcher injects
exactly one reply per fire; the `reply` poller reads it without
caring about site.

**Admin NATS connection (for `jetstream_consume`, `nats_subscribe`,
and `federation.Apply`):** ONE conn at runner startup. JetStream
domains (configured per site, see §6.1.1) let one conn drive admin
on either site by using `jetstream.New(admin, jetstream.WithDomain("site-X"))`.
Pollers pick the domain via the `site:` field from their YAML
`expected[]` block.

### 5.2 Pollers — `site:` arg, no fan-out

**Site-scoped pollers** (`mongo_find`, `jetstream_consume`,
`nats_subscribe`, `logs_tail`) take their target site from the YAML
`expected[].site` field. No fan-out, no `_site` payload tagging, no
matcher changes. Each poll hits ONE backend.

**Cassandra is shared, so `cassandra_select` ignores `site:` —**
loader rejects `site:` on `cassandra_select` blocks.

Concrete shape for `mongo_find`:

```go
type MongoFindPoller struct {
    Sites     map[string]*mongo.Database  // "site-a" → mongo-site-a's chat DB
    StartTime time.Time
}

// PollFn signature gains `site string` — passed in from the YAML
// expected[].site field by the case runner.
func (p *MongoFindPoller) PollFn(site string, args map[string]any, _ string) func() []Event {
    db := p.Sites[site]
    return func() []Event {
        rows, _ := db.Collection(args["collection"].(string)).Find(...)
        return rowsToEvents(rows, "mongo_find")
    }
}
```

Same pattern for `jetstream_consume`, `nats_subscribe`, `logs_tail`:
each takes a `site string` parameter and picks the corresponding
backend from its `Sites` map. Cost: 1× backend query per poll, same
as today's single-site. **No goroutine fan-out, no errgroup, no
parallel queries.**

**Stateful poller cache keys** become `(site, …)`:
- `jetstream_consume`: `(site, stream, filter_subject)`
- `nats_subscribe`: `(site, subject)`
- `logs_tail`: container name already site-suffixed; cache key
  unchanged.

**`nats_subscribe` Warmer:** Core NATS has no replay, so the Warmer
hook opens the subscription BEFORE the verb fires. The Warmer reads
`site:` from the same `expected[]` block and opens only that site's
subscription — no fan-out.

**Matcher unchanged.** `MatchShape` does subset deep-match on event
payloads. No `_site` injection, no new filtering logic.

### 5.3 Mishaps (`mishap_site:` resolution) — ⛔ DEFERRED

> **This entire section is out of scope for the multi-site work.**
> Mishaps/chaos are disabled until the 2-site system runs and asserts
> federation without them. The design below is kept as the plan for
> the follow-up that re-enables chaos — do not implement it now.
> Scenarios ship with no `mishap:` blocks during multi-site bring-up.

```yaml
cases:
  - name: room-creation-survives-site-a-mongo-partition
    mishap: mongo-partition-500ms
    mishap_site: site-a              # explicit; defaults to firing site
```

**One Toxiproxy, one Docker daemon — `FactoryContext` is unchanged
from today except for `TargetSite`:**

```go
// before
type FactoryContext struct {
    ChaosEngine ChaosEngine
    DockerCLI   DockerCLI
    Pod         string
    Duration    time.Duration
}

// after
type FactoryContext struct {
    ChaosEngine ChaosEngine   // single — one Toxiproxy admin URL
    DockerCLI   DockerCLI     // single — one Docker daemon
    TargetSite  string        // from mishap_site; suffixes proxy + container names
    Pod         string
    Duration    time.Duration
}
```

There is **one Toxiproxy container** hosting site-named proxies
(`MongoProxy-site-a`, `MongoProxy-site-b`, `CassandraProxy-site-a`,
`CassandraProxy-site-b`, `NATSProxy-site-a`, `NATSProxy-site-b`). Site
scoping is a **proxy-name suffix**, not a separate container or admin
URL — so `FactoryContext` needs no per-site map. A factory does:

```go
ctx.ChaosEngine.Disable("CassandraProxy-" + ctx.TargetSite)
ctx.DockerCLI.Restart("room-service-" + ctx.TargetSite)
```

Default `mishap_site:` is the case's firing site (from credential) —
covers today's single-site mental model.

**Cassandra mishap nuance:** the Cassandra cluster is shared, but each
site routes to it through its own proxy (`CassandraProxy-site-a`,
`CassandraProxy-site-b`, same upstream). So
`cassandra-partition-500ms` with `mishap_site: site-a` disables
site-a's proxy — cutting site-a's services off from Cassandra while
site-b keeps reading. This is the chaos signal you want for "site-a
loses its Cassandra link mid-flow."

**New federation-bridge mishap kind:** `outbox-source-pause-500ms`
disables the JetStream Source on the target INBOX for 500ms, then
re-enables. Tests the federation-lag path explicitly. Factory lives
in `internal/mishap/federation_bridge.go`.

---

## 6. Infrastructure

### 6.1 USE_INFRA=true (testcontainers)

`internal/infra/stack.go` becomes 2× the boot scope. Shared Docker
network; per-site aliases.

```
Shared Docker network: itc-net-<runID>     —  26 containers total

site-a aliases                    site-b aliases               shared
─────────────────                 ─────────────────            ──────────
nats-site-a                       nats-site-b                  cassandra
mongo-site-a                      mongo-site-b                 toxiproxy
valkey-site-a                     valkey-site-b                  (one container,
auth-service-site-a               auth-service-site-b            site-named proxies)
broadcast-worker-site-a           broadcast-worker-site-b
history-service-site-a            history-service-site-b
inbox-worker-site-a               inbox-worker-site-b
message-gatekeeper-site-a         message-gatekeeper-site-b
message-worker-site-a             message-worker-site-b
notification-worker-site-a        notification-worker-site-b
room-service-site-a               room-service-site-b
room-worker-site-a                room-worker-site-b

                ──── supercluster gateway ────►
            nats-site-a:7222 ◄═══════════► nats-site-b:7222

The single `toxiproxy` container hosts site-named proxies:
  MongoProxy-site-a → mongo-site-a       MongoProxy-site-b → mongo-site-b
  CassandraProxy-site-a → cassandra      CassandraProxy-site-b → cassandra
  NATSProxy-site-a → nats-site-a         NATSProxy-site-b → nats-site-b
Both sites reach the shared Cassandra through their own proxy, so
chaos can cut one site's link without touching the other.

Deps: 8 (nats×2, mongo×2, valkey×2, cassandra×1, toxiproxy×1)
Services: 18 (9 × 2 sites)
```

**Boot order:**
1. Network + Toxiproxy ×1 (boots empty; proxies created in step 2.5).
2. Deps in parallel — NATS ×2 (with gateway + JetStream domain config
   — see §6.1.1), Mongo ×2, Valkey ×2, **Cassandra ×1 (shared)**.
2.5. **NEW** `internal/infra/toxiproxy.go` programmatically creates
     all 6 site-named proxies via Toxiproxy admin API (HTTP POST to
     `:8474/proxies`). Boots empty in step 1 so the API is available
     here; no static JSON config file.
3. Wait for supercluster — both NATS report the gateway peer connected
   (`/varz` poll, ~5s timeout).
4. Cassandra init ×1 (single DDL, one keyspace).
5. Services ×2 sites × 9 services = 18 containers (in parallel within
   each site). Each service's `CASSANDRA_HOSTS` points at its site's
   `CassandraProxy-<site>` on the shared `toxiproxy` container.
5.5. **NEW** wait for inbox-worker on each site to have created its
     INBOX stream. Poll `js.Stream(INBOX_<site>)` (with the
     appropriate JetStream domain) until non-error, ~30s timeout.
     Without this wait, step 6's `UpdateStream` races inbox-worker's
     bootstrap and fails with "stream not found."
6. **NEW** federation Sources attach (`internal/infra/federation.go`)
   — driven by `catalogs/federation.yaml` (§6.1.2 below). Loader reads
   the catalog, then for each entry runs `js.UpdateStream(<stream>,
   AddSource{stream: <from_stream>, filter: <filter>})` against the
   target NATS admin connection.
   No verification step. If the supercluster is broken, federation
   scenarios fail at assertion time with the standard "no events on
   site-b" report — that's signal enough.

**Backend handle plumbing** — `Stack` accessors expand to take a
site ID:

```go
// before
func (s *Stack) NATSURL() string
func (s *Stack) MongoURI() string
func (s *Stack) CassandraHostPort() string

// after
func (s *Stack) NATSURL(siteID string) string
func (s *Stack) MongoURI(siteID string) string
func (s *Stack) CassandraHostPort() string         // unchanged — shared cluster
func (s *Stack) Sites() []string                   // returns ["site-a", "site-b"]
```

`cmd/runner/main.go` builds the Config from those accessors:

```go
type Config struct {
    SiteA SiteConfig
    SiteB SiteConfig
    // Shared across both sites:
    CassandraHosts    string  // localhost:9042 or stack.CassandraHostPort()
    CassandraKeyspace string  // "chat"
    MongoDB           string  // database name "chat" (same on both Mongos)
    NATSCredsFile     string  // single backend.creds (per §6.1.1 single trust chain)
    // ... rest unchanged (OutputPath, PerformancePath, RepoRoot, etc.)
}

type SiteConfig struct {
    NATSURL  string  // nats://nats-site-a:4222
    MongoURI string  // mongodb://chat-local-toxiproxy:27017 (via Toxiproxy MongoProxy-site-X)
    AuthURL  string  // http://auth-service-site-a:8080
}
```

Explicit `SiteA`/`SiteB` fields (not a map) — matches the
non-goal "N=2 fixed; never more." Code that iterates picks both
fields by name.

**Env vars under manual operation** (not USE_INFRA):
`SITE_A_NATS_URL`, `SITE_A_MONGO_URI`, `SITE_A_AUTH_SERVICE_URL`,
`SITE_B_NATS_URL`, `SITE_B_MONGO_URI`, `SITE_B_AUTH_SERVICE_URL`,
plus the shared `CASSANDRA_HOSTS`, `CASSANDRA_KEYSPACE`,
`MONGO_DB`, `NATS_CREDS_FILE`. But per non-goal §0,
multi-site requires `USE_INFRA=true`; manual env vars are
listed for completeness, never the primary path.

**Cost** (measured from the per-container caps, not guessed):

| | Today | Multi-site |
|---|---|---|
| Containers | 14 | 26 |
| Cassandra (the only heavy one, `mem_limit 800m`) | ×1 | ×1 (shared) |
| Working-set RAM | ~1.3 GB | **~1.8 GB** |
| Cold boot | Cassandra-bound (~90s) | Cassandra-bound (~90s) + ~5s gateway handshake |

The doubling costs **~500 MB** — because Cassandra (the fat
container) is shared and Go service binaries are ~30 MB RSS each. The
extra 12 containers boot in parallel under Cassandra's cold-start
shadow, so wall-clock barely moves. A dev box with 4 GB free runs it;
8 GB is comfortable. (Earlier 12-16 GB figures were over-cautious
guesses — disregard.)

#### 6.1.1 NATS supercluster config — single trust chain, per-site gateway

**Both NATS containers mount the EXISTING `docker-local/nats.conf`**
(the same operator JWT + account JWT + JetStream block that
single-site already uses). The only per-site difference is a layered
gateway addendum.

Site-A loads `docker-local/nats.conf` plus
`docker-local/nats.gateway.site-a.conf`:

```conf
# nats.gateway.site-a.conf — appended via `include` in the
# multisite-tool's startup
server_name: nats-site-a
cluster: { name: site-a, listen: 0.0.0.0:6222 }
gateway: {
    name: site-a
    port: 7222
    gateways: [
        { name: site-b, url: nats://nats-site-b:7222 }
    ]
}

# JetStream domain — lets one admin conn drive remote JetStream admin
# (UpdateStream, AddSource, etc.) by addressing $JS.site-X.API. The
# stock docker-local nats.conf has jetstream {} unconfigured; we
# layer the domain in per site here.
jetstream: {
    domain: site-a
    store_dir: /data/jetstream
}
```

Site-B mirrors with `site-b` substituted and the gateway pointing the
other direction.

**No JWT minting code in the suite.** The single existing operator +
account JWT from `docker-local/setup.sh` covers both NATS containers.
Per the user's framing: *"2 sites combine to make a single system.
Trust extends."* — the trust chain is one logical chain even though
the supercluster has two nodes.

**Auth boundary is code-driven, not trust-driven.** Alice's JWT (signed
by the shared operator) would technically connect to either NATS. The
suite enforces site routing via the verb executor's
`SiteURLs[in.Site]` lookup (§5.1) — the YAML `site:` field decides
which NATS the executor connects to. If a bug ever caused mis-routing,
NATS itself wouldn't reject; the assertion would surface the wrong-site
outcome via missing/extra events.

**What's not in this config** (deliberately):
- No per-site operator or account JWTs.
- No leaf-node config, TLS, monitoring exporters.
- No production-specific gateway permissions. The shared trust + the
  gateway block are enough for JetStream Sources to cross-validate.

**How startup wires it:** `internal/infra/deps.go` startNATS becomes
parameterized by site — it mounts `docker-local/nats.conf` plus the
per-site `nats.gateway.site-X.conf` and starts two containers with
distinct network aliases.

#### 6.1.2 Federation Sources catalog — `catalogs/federation.yaml`

Two-direction Source spec for the supercluster:

```yaml
# tools/integration-suite-multisite/catalogs/federation.yaml
sources:
  - on: site-b                                     # which NATS to apply to
    stream: INBOX_site-b                           # the stream getting a Source
    from_stream: OUTBOX_site-a                     # the source stream (on peer site)
    filter: outbox.site-a.to.site-b.>              # subject filter on the source
  - on: site-a
    stream: INBOX_site-a
    from_stream: OUTBOX_site-b
    filter: outbox.site-b.to.site-a.>
```

`internal/infra/federation.go`:

```go
type SourceSpec struct {
    On         string `yaml:"on"`           // "site-a" or "site-b"
    Stream     string `yaml:"stream"`       // target stream getting a Source
    FromStream string `yaml:"from_stream"`  // peer site's source stream
    Filter     string `yaml:"filter"`       // subject filter on the source
}

func LoadFederationSources(path string) ([]SourceSpec, error) { ... }

// Apply runs UpdateStream on each spec using a single admin conn.
// JetStream domains route the admin call to the right NATS.
func Apply(ctx context.Context, specs []SourceSpec, admin *nats.Conn) error {
    for _, s := range specs {
        js, err := jetstream.New(admin, jetstream.WithDomain(s.On))  // target domain
        if err != nil { return fmt.Errorf("federation js: %w", err) }
        _, err = js.UpdateStream(ctx, jetstream.StreamConfig{
            Name: s.Stream,
            Sources: []*jetstream.StreamSource{{
                Name:          s.FromStream,
                FilterSubject: s.Filter,
                Domain:        peerDomain(s.On),  // source stream's domain
            }},
        })
        if err != nil { return fmt.Errorf("federation Apply %s: %w", s.Stream, err) }
    }
    return nil
}
```

Cross-domain Sources work because gateway connection lets one NATS
fetch messages from the peer's JetStream by domain-qualified subject.
The `Domain` field on `StreamSource` tells the source stream it's a
remote domain; the gateway transports the messages.

**Why catalog YAML, not hardcoded Go:** ops-editable (when production
grows a third federated stream type, edit one YAML, no Go change).
Same vocabulary pattern the suite uses elsewhere (`catalogs/verbs/`,
`catalogs/readers/`, etc.).

**Why YAML, not pulling from `pkg/stream`:** CLAUDE.md §6 explicitly
keeps federation Sources OUT of `pkg/stream` / service `bootstrap.go`.
The suite owns the federation truth here; not the production code.

### 6.2 Manual stack — stays single-site

The `docker-local/` compose files are not extended for multi-site.
Operators wanting multi-site testing must use `USE_INFRA=true`.

Rationale: the manual stack is for fast local iteration against
single-site behavior; multi-site doubles the container count, adds
the federation config step, and is fundamentally a CI / cold-boot
workflow. Keeping the manual stack single-site avoids 5 new compose
files, trust-chain script extensions, and a separate
federation-apply binary — none of which are testing-tool code.

### 6.3 Federation Sources — one path only

`internal/infra/federation.Apply(siteAConn, siteBConn)` is called
directly inside `infra.Up` (boot step 6 above). No standalone binary,
no compose layer. Sandbox.Setup makes no precondition check — if the
supercluster is misconfigured or federation Sources aren't attached,
scenarios fail with "no events on site-b" at assertion time.

CLAUDE.md §6 still rules: production services don't touch federation
config. The suite owns it because the suite owns its infra; that
isolation doesn't leak into service code.

---

## 7. Reporter changes

**None.** Site is a YAML field, not a runtime concern, and the matcher
isn't changed. The per-case header gains a passive `site: site-X`
echo when the assertion fails (taken from the same block the matcher
was using), but nothing else changes.

`performance.json` schema unchanged. Confusion matrix unchanged.

---

## 8. Scenarios — write fresh, don't migrate

Under the fork model, the 11 single-site scenarios stay in
`tools/integration-suite/` (untouched, frozen). The multi-site tool
starts with an **empty** `scenarios/drafts/` after CP0's copy strips
them.

New scenarios are written from scratch in the multi-site shape:

```yaml
scenario: alice-creates-room-federates-to-site-b
status: draft

sites:
  site-a:
    seed:
      users: { alice: { verified: true } }
  site-b:
    seed:
      users: { bob: { verified: true } }

cassandra_data: []   # shared cluster; populate as needed

tag: positive

input:
  site: site-a                                                # fire from site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Engineering
    users: ["${alice.account}", "${bob.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match: { body_json: { status: accepted } }
  - location: mongo_find
    site: site-a                                              # local write
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering, createdBy: ${alice.id} }
  - location: mongo_find
    site: site-b                                              # federation tail
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering }
    timeout: 10s                                              # few-hundred-ms tail
```

**Minimum viable scenario set for the multi-site tool's first ship:**
one scenario like the above. Proves the pipeline end-to-end. Anything
beyond is follow-up. The 11 single-site scenarios stay in the
single-site tool, exercising single-site behavior.

---

## 9. Implementation order — one go, no checkpoints

The user wants this delivered as one round of work, then tested as a
minimal version, then extended. **No incremental checkpoints, no
progressive PRs.** The sequence below is execution order, not landing
order; everything ships together.

### Step 0 — Fork the tool

```sh
cp -r tools/integration-suite tools/integration-suite-multisite
```
- Rewrite all import paths inside the copy:
  `github.com/hmchangw/chat/tools/integration-suite/...` →
  `github.com/hmchangw/chat/tools/integration-suite-multisite/...`
- Update the Makefile targets so `make -C tools/integration-suite-multisite local` works.
- Delete all scenarios inside the copy
  (`tools/integration-suite-multisite/scenarios/drafts/*.yaml`) —
  they're single-site grammar and don't apply.
- Confirm `go test -race ./tools/integration-suite-multisite/...`
  passes (it's a verbatim copy).
- Confirm `go test -race ./tools/integration-suite/...` still passes
  untouched.

### Step 1 — YAML grammar + loader

- `Scenario.Sites` (map under `sites:` block); `cassandra_data`
  promoted to scenario top level.
- Scenario struct collapsed: no `BaseInput`/`Cases`/`CaseInputOverride`;
  a single `Input` field and a single `Expected []Expected` field at
  scenario top level. `Tag` (positive/negative) moves to scenario.
- `site:` field added to `Input` and `Expected` structs.
- Substitution: reject `${site}`, `${siteA}`, `${siteB}`,
  `${<alias>.site}`, and `${service.*.credential}` — site is
  structural now, not a substitution.
- Alias-uniqueness validator across both sites.

### Step 2 — Runtime: per-site Sandbox + executor

- `Input.Site` added (from YAML `input.site`). Credential is NOT
  site-tagged. No case-input shallow-merge logic — one Input per
  scenario, period.
- Drop `Case`, `CaseInputOverride`, `BaseInput` types entirely; replace
  with a single top-level `Input` field on `Scenario`. `Expected` list
  moves to scenario top level. Tag (positive/negative) moves to
  scenario.
- Drop the case loop in `case_runner.go` / `runner_scenario.go`. One
  scenario = Setup → Fire → Assert all expected → Teardown.
- `Sandbox.Setup` 13 steps per §4 (per-site loops for Mongo+user-mint,
  single for Cassandra+placeholders).
- `verbs.NATSRequest.SiteURLs` + `verbs.JetStreamPublish.SiteURLs`
  per §5.1; executor picks URL by `in.Site` (from YAML `site:` field).
- `cmd/runner/main.go`: drop `buildServiceCreds`, build new Config
  shape per §6.1.
- Loader validates `site:` is required on `input` and every
  site-scoped `expected[]` block; rejects on `reply` and
  `cassandra_select`.

### Step 3 — Pollers take `site:` arg

- `mongo_find`, `jetstream_consume`, `nats_subscribe`, `logs_tail`
  take `site string` directly. Each holds a `Sites map[string]<backend>`
  and picks the one for the named site. **One backend hit per poll, no
  fan-out.**
- `jetstream_consume` + `nats_subscribe` cache keys become `(site, …)`.
- `nats_subscribe.Warm` opens the sub for the site named on the
  expected[] block.
- `cassandra_select` unchanged (single shared cluster).
- Matcher unchanged. No `_site` payload field.

### Step 4 — Infrastructure (USE_INFRA=true)

- `internal/infra/stack.go`: 2× NATS (with gateway + JS domain),
  2× Mongo, 2× Valkey, 1× Cassandra, 1× Toxiproxy.
- `internal/infra/services.go`: 2 × 9 services; each gets distinct
  `SITE_ID`, distinct backend URLs.
- `internal/infra/toxiproxy.go` (new): programmatically create 6
  site-named proxies via Toxiproxy admin API after the container is
  up (boot step 2.5).
- `internal/infra/federation.go` (new): load
  `catalogs/federation.yaml` per §6.1.2; apply per spec.
- Wait-for-stream poll loop before `federation.Apply` (boot step 5.5).

### Step 5 — First scenario

Write `scenarios/drafts/room-creates-federates-to-site-b.yaml` per
the §8 sketch. Run it under `USE_INFRA=true`. Iterate until green.

### Step 6 — Verify single-site untouched

`go test -race ./tools/integration-suite/...` still green;
`make -C tools/integration-suite local` still runs the 11 single-site
scenarios.

**Done — multi-site tool ships.** Further scenarios are
follow-up work, not part of this delivery.

---

## 10. Open items / known risks

- **Cold-boot time** ~4-6 min under USE_INFRA. Painful for local
  iteration. Mitigation: use INTERACTIVE=true against the
  single-site manual stack while authoring; switch to USE_INFRA only
  when adding a federation scenario or shipping.
- **Memory** ~1.8 GB working set (4 GB free runs it; 8 GB
  comfortable). Document in `RUNBOOK.md`.
- **Trust chain stays single** — both NATS containers mount the
  existing `docker-local/nats.conf`; no JWT minting in the suite. See
  §6.1.1. (Earlier drafts said "mint two operator/account pairs" —
  reversed; the user clarified that 2 sites form one logical system,
  trust extends.)
- **Alias uniqueness across sites** — explicit design choice (§3.2).
  Worth surfacing in `AUTHORING.md` so authors don't try `alice` on
  both sites.

---

## 11. Implementation decisions log

Captured 2026-06 in the developer's clarification round before
Checkpoint 1 started. These resolved gaps the architecture-level
design session didn't drill into.

### Round 1 — architecture-level gaps from initial design pass

| # | Question | Decision |
|---|---|---|
| 1 | Site tag on `${service.backend.credential}` | Drop service creds from YAML entirely (§3.4); one `backend.creds` file used by both NATS containers under the shared trust chain |
| 2 | Federation Sources precondition check cache scope | Drop the check; infra-only ownership (§4 narrative paragraph) |
| 3 | `nats_subscribe` Warmer behavior in multi-site | Warm opens subs on both sites; cache keyed `(siteID, subject)` (§5.2) |
| 4 | `jetstream_consume` cache key | Per-site: `(siteID, stream, filter_subject)` (§5.2) |
| 5 | Chaos struct shape | ⛔ DEFERRED — chaos disabled for this work (§5.3) |
| 6 | NATS supercluster config block | Minimal viable: per-site gateway block layered on shared `docker-local/nats.conf` (§6.1.1) |
| 7 | Mishaps/chaos during multi-site bring-up | ⛔ DISABLED |

### Round 2 — minimal-happy-path clarifications (2026-06 latest)

| # | Question | Decision |
|---|---|---|
| 8 | Pollers: fan-out vs explicit `site:` arg | **Fan-out + `_site` payload tag** (locked from senior). 4 site-scoped pollers query both sites in parallel; matcher's existing subset-deep-match filters by `_site:` if author needs to disambiguate (§5.2). |
| 9 | Toxiproxy: passive vs drop | **Passive in path.** Single Toxiproxy container with site-named proxies; `serviceEnv` keeps today's `chat-local-toxiproxy` hostnames; no chaos injected. Zero rework when chaos returns. |
| 10 | NATS trust chain separation level | **Single trust chain extends to both sites** (§6.1.1). Both NATS containers mount `docker-local/nats.conf`; per-site gateway block layered on. No JWT minting in the suite. User framing: "2 sites combine to make a single system. Trust extends." |
| 11 | Federation Sources config source | **Catalog YAML** at `catalogs/federation.yaml` (§6.1.2). Ops-editable; suite reads at boot and applies via `js.UpdateStream`. |
| 12 | Verb executor per-site routing shape | **One executor with `SiteURLs map[string]string`** (§5.1). `Credential` gains a `Site` field; executor picks URL by `cred.Site` per call. Dispatcher unchanged (it didn't hold NATS conns anyway). |
| 13 | Fork or extend the existing tool? | **Fork.** New tree at `tools/integration-suite-multisite/`. Single-site tool at `tools/integration-suite/` is frozen — no changes. Both tools coexist; team accepts duplication cost. See banner at top of doc + CP0. |

### Round 3 — pre-coding implementation gaps (2026-06)

| # | Question | Decision |
|---|---|---|
| 14 | When does `federation.Apply` run relative to `inbox-worker` boot? | Poll for `js.Stream(INBOX_<site>)` existence (with the appropriate JS domain) after services-up, ~30s timeout, then Apply. Boot step 5.5 added (§6.1). |
| 15 | Config struct shape | Explicit `SiteA`/`SiteB` fields, no map. Shared fields (`CassandraHosts`, `MongoDB`, `NATSCredsFile`, etc.) at top level. Concrete shape in §6.1. |
| 16 | Admin NATS connection count | **One admin conn + JetStream domains.** `nats.gateway.site-X.conf` adds `jetstream { domain: site-X, store_dir: /data/jetstream }`. Admin code uses `jetstream.New(admin, jetstream.WithDomain(site))` to target a domain. Pollers fan-out by domain, not by physical connection. |
| 17 | Site-named Toxiproxy proxies — config file or programmatic? | **Programmatic.** `internal/infra/toxiproxy.go` (new) POSTs to Toxiproxy admin API at boot step 2.5 to create all 6 proxies. No static JSON config file. |
| 18 | Valkey | Per-site (2 containers), as already in the spec. `room-keys.json` seeding can be skipped for the minimal scenario — rooms are created via room-service which mints its own keys. |

### Round 4 — explicit `site:` field, supersedes fan-out (2026-06)

Major simplification. The author writes `site:` as a structural YAML
field on `input` and every site-scoped `expected[]` block. Routing is
**structural** — zero fan-out, zero `_site` tagging, no matcher
changes.

| # | Question | Decision |
|---|---|---|
| 19 | How does the poller know which site? | **`site:` field on input + expected[] blocks** (§3.2). Required for site-scoped pollers (`mongo_find`, `jetstream_consume`, `nats_subscribe`, `logs_tail`). Rejected for `reply` (intrinsic) and `cassandra_select` (shared). Loader rejects unknown values. |

**Supersedes:**
- **Round 2 Q8** (fan-out + `_site` tag). NOT IMPLEMENTED. Pollers
  take `site:` directly; no fan-out, no `_site` payload tagging, no
  matcher filtering by `_site:`.
- **Round 2 Q12** (`Credential.Site`). Credential no longer carries
  Site. Site lives in YAML structure; passed to the executor as
  `in.Site` (Input field, not Credential field). §5.1 updated.
- Substitution tokens `${siteA}` / `${siteB}` / `${<alias>.site}`
  no longer exist. Authors write site names literally in subjects.
  §3.3 updated.
- §7 reporter changes from "per-site event count formatter" to
  "nothing changes" (no fan-out to report on).

**What this buys:** pollers stay 1× backend hit per poll, matcher
stays untouched, the failure formatter stays as-is. Author cost is
one extra field per block — clearly named.

### Round 5 — one input, one expected, no cases (2026-06)

Drop the case-loop model entirely. A scenario YAML has:
- `input:` — one block (no `base_input`, no `cases[]`, no `case.input`)
- `expected:` — one list (assertions for this scenario's single fire)
- `tag:` — at scenario top level (positive/negative for the matrix)

| # | Question | Decision |
|---|---|---|
| 20 | Scenario shape: one input + one expected, no cases | **One scenario = one fire + one envelope.** Variants are separate scenario files. State inheritance between cases is structurally impossible because there are no cases. |

**Supersedes:**
- `BaseInput` / `Case` / `CaseInputOverride` / `Cases []Case` Go
  types removed; replaced with a single `Input` and a top-level
  `Expected []Expected` slice on `Scenario`.
- `Tag` moves from `Case.Tag` to `Scenario.Tag`.
- The case loop in `runner_scenario.go` becomes:
  `Setup → Fire(input) → assert each expected → recordScenario → Teardown`.
- `recordCase` becomes `recordScenario`; performance.json keys go
  from `<scenario>/<case-name>` to just `<scenario>`; confusion
  matrix axis is scenario tag, not case tag.

**What this buys:**
- No state inheritance footgun (state-leakage between cases was the
  primary correctness risk in the single-site tool).
- Reporter logic simplifies.
- YAML is shorter and reads "what does this scenario test?" without
  scrolling past base/case structure.

**Cost:** N test variants of "same fire, different chaos" or "same
fire, different seed" become N YAML files instead of N cases in 1
file. Acceptable — file proliferation is cheap; conceptual clarity
is not.
