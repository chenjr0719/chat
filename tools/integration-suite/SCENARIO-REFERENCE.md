# Test scenario YAML — reference

How a scenario YAML is shaped, what each field means, what tokens you
can use inside it, and what vocabulary it can reference. Read this
before authoring or hand-editing a scenario.

This is a reference, not a workflow guide. For the workflow ("how do
I author one"), see [AUTHORING.md](AUTHORING.md). For the
infrastructure that scenarios reference (verbs / readers / matchers /
mishaps / seed-effects catalogs), see the YAMLs themselves under
`catalogs/` — each file is heavily commented.

**Authoritative schema:** `internal/scenario/types.go` (the Go struct
YAML decodes into). When this doc disagrees with that file, the Go
type wins — and that's a doc bug.

---

## 1. Where scenarios live

```
scenarios/
├── drafts/<name>.yaml          unreviewed; informational in CI
└── approved/<name>.yaml        promoted via human PR; gates the run
```

Flat layout — no scope subfolders. One scenario per file. The
filename (without `.yaml`) should match the `scenario:` field by
convention.

---

## 2. Anatomy of a scenario

A complete scenario, every section labelled:

```yaml
scenario: Create Room Sandbox                         # ─── 2.1 identity
source: design-doc#room-creation (room-service/...)   # ─── 2.2 citation (required)
status: approved                                      # ─── 2.3 optional; default "draft"

seed:                                                 # ─── 2.4 inline seed users
  users:
    alice:
      verified: true       # closed-catalog effect flag
    eve_unverified:
      verified: false      # no-op (explicit "no effect" — documents intent)

base_input:                                           # ─── 2.5 case-inherited defaults
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.${site}.create
  payload:
    name: Engineering Chat
  credential: ${alice.credential}

cases:                                                # ─── 2.6 explicit experiments
  - name: happy-path-room-created
    tag: positive                                     # confusion-matrix axis
    expected:
      - location: reply
        match:
          body_json:
            status: accepted

  - name: unverified-user-rejected
    tag: negative
    input:                                            # case-local override
      subject: chat.user.${eve_unverified.account}.request.room.${site}.create
      credential: ${eve_unverified.credential}
    expected:
      - location: reply
        match:
          body_json:
            error: "unauthorized"

  - name: withstands-mongo-partition
    tag: positive
    mishap: mongo-partition-500ms                     # closed-catalog kind
    expected:
      - location: mongo_find
        args:
          collection: rooms
          filter:
            name: Engineering Chat
        match:
          name: Engineering Chat
```

### 2.1 `scenario` — identity

Human-readable name. Surfaces in the report's `ScenarioName` column
and in PerformanceStore keys (`<scenario>/<case-name>`).

### 2.2 `source` — citation (required)

A `file:line` or `doc#section` reference back to the design or code
the scenario is asserting against. Every scenario cites a source —
authors don't invent expected behavior.

### 2.3 `status` — `draft` (default) or `approved`

`approved` scenarios gate CI via the approved-only report. Drafts run
informationally.

### 2.4 `seed` — inline seed block

```yaml
seed:
  users:
    <alias>:
      <effect-flag>: <bool>
      ...
  rooms:           # optional — pre-seeded rooms/subscriptions for msg.send scenarios
    - id: r-eng
      type: channel
      name: Engineering
  memberships:     # optional — subscription rows referencing seed.users + seed.rooms
    - room: r-eng
      user: alice
      role: owner
  cassandra_data:  # optional — pre-seeded Cassandra rows for history scenarios
    messages_by_room:
      - room_id: r-eng
        created_at: ${now - 2m}
        bucket: ${bucket(created_at)}
        message_id: m-1
        body_text: "hello"
```

**Users:**
- `<alias>` is a scenario-local handle. Substitution tokens
  `${<alias>.account}`, `${<alias>.id}`, `${<alias>.jwt}`,
  `${<alias>.nkey}`, `${<alias>.credential}` resolve to the
  materialized user's fields.
- `<effect-flag>` is a key from the closed `catalogs/seed-effects/`
  catalog. Each effect is a registered Go function that takes the
  materialized SeedUser and applies one mutation.
- Effect flags with value `false` are explicit no-ops.
- Unknown flags fail validation at sandbox setup.
- The user's id is derived: `id = "u-" + account`.

**Rooms / memberships:** see `docs/spec-room-subscription-seed.md` for
the full grammar (closed enums for room type + member role, DM
arity, unique room ids).

**Cassandra data:** see `docs/spec-cassandra-seeding-engine.md` for
the full grammar (table-name + column-name CQL identifiers,
`${now ± d}` relative timestamps, optional `${bucket(<col>)}`
auto-computed partition keys).

**Catalog of seed-effects** (today):

| Flag | Effect | When you'd use it |
|------|--------|-------------------|
| `verified: true` | Mint nkey + JWT via auth-service /auth | Any scenario where the actor needs to make a NATS request |

`verified: false` (the default, also explicit) leaves the user with
empty JWT/NkeySeed — useful for negative scenarios that assert NATS
auth rejection.

### 2.5 `base_input` — case-inherited defaults

```yaml
base_input:
  verb: <verb-name>           # required
  subject: <subject-template> # post-substitution
  payload: <map>              # post-substitution
  credential: <ref>           # ${alias.credential} or ${service.<name>.credential}
```

Every case inherits this; a case's `input:` block shallow-merges
overrides on top.

### 2.6 `cases` — ordered, explicit experiments

```yaml
cases:
  - name: <unique-within-scenario>
    tag: positive | negative
    input:                # optional; shallow-merge override of base_input
      subject: ...
      payload: ...
      credential: ...
    mishap: <kind-name>   # optional; one of the catalog mishap kinds
    expected:             # required; non-empty list
      - location: <poller-location>
        args: <map>       # per-primitive args (see §4)
        match: <shape>
        timeout: 5s       # optional; default 5s
        polling: 100ms    # optional; default 100ms
        not: false        # optional; default false (positive assertion)
```

- **`tag`**: drives the report's confusion matrix. `positive` cases
  assert the system DOES the thing; `negative` cases assert the
  system correctly REJECTS the thing.
- **`mishap`**: at most one per case. There is no Cartesian
  expansion — if you want crash AND partition, write two cases.
- **`expected[]`**: order matters for the human reader; the runner
  short-circuits on the first failure.

### 2.7 `expected[]` block — per-assertion semantics

| Field | Type | Required | Meaning |
|-------|------|----------|---------|
| `location` | string | ✓ | One of the registered poller locations (see §4). |
| `args` | map | depends on location | Per-primitive args (collection+filter for mongo_find, query for cassandra_select, subject for nats_subscribe, etc.). Tokens resolve before the call. |
| `match` | map | ✓ | Subset shape against the event payload. Substitution tokens (`${alias.*}`, `${site}`, `${input.payload.*}`, `${now}`) resolve before matching. |
| `timeout` | duration | optional, default `5s` | How long Gomega's `Eventually` waits before failing. Accepts Go duration strings (`100ms`, `2s`, `1m`). |
| `polling` | duration | optional, default `100ms` | How often the poller is re-invoked within the timeout. |
| `not` | bool | optional, default `false` | If true: uses `Consistently(poller, timeout, polling).ShouldNot(MatchShape(match))`. Asserts the matching event MUST NOT happen across the timeout window. |

---

## 3. Substitution tokens

Resolve before the dispatcher fires, and again per-assertion before
matching.

| Token | Resolves to | Available where |
|-------|------------|-----------------|
| `${<alias>.account}` | `seed.users.<alias>` account (== alias) | subject, payload, credential, match, args |
| `${<alias>.id}` | `u-` + account | same |
| `${<alias>.jwt}` | minted NATS JWT | same |
| `${<alias>.nkey}` | nkey seed | same |
| `${<alias>.credential}` | shorthand for the alias's user-level cred | credential field |
| `${service.<name>.credential}` | service-level NATS creds file (today: `${service.backend.credential}` from `NATS_CREDS_FILE`) | credential field |
| `${site}` | `cfg.SiteID` (default `site-local`) | subject, payload, match, args |
| `${now}` | `time.Now().UTC().UnixMilli()` | subject, payload — useful for jetstream_publish synthetic events |
| `${now ± d}` | relative offset in Cassandra seed rows (`${now - 2m}`) | seed.cassandra_data |
| `${bucket(<col>)}` | auto-computed message-bucket partition value | seed.cassandra_data |
| `${input.subject}` | post-substitution subject for this case | match (post-fire) |
| `${input.payload.<key>}` | post-substitution payload field | match, args |
| `${input.requestId}` | UUIDv7 X-Request-ID set by the dispatcher | match |
| `$auto` | runtime-generated unique value (`it-<runID>-room-auto-<N>`) | subject, payload — use for room names / IDs that must not collide across runs |

---

## 4. Universal primitives (poller locations)

Each `expected[].location` must be one of the universal primitives
registered by `pollers.RegisterBuiltinPollers`. Every primitive accepts
per-assertion `args` from the YAML so the runtime is application-agnostic.

| Location | Args | Backs | Degrade-on-nil |
|----------|------|-------|----------------|
| `reply` | (none) | Dispatcher-injected sync reply outcome | `ReplyReader` nil — programming error |
| `mongo_find` | `collection`, `filter` | Any Mongo collection (`createdAt >= startTime` auto-merged) | `MongoDB` nil → warn + empty |
| `cassandra_select` | `query`, `params?` | Any Cassandra table (`StartTime` bound to first `?` when params unset) | `Cassandra` nil → warn + empty |
| `jetstream_consume` | `stream`, `filter_subject` | Any JetStream stream (replay via DeliverByStartTime) | `AdminConn` nil → warn + empty |
| `nats_subscribe` | `subject` | Any Core NATS subject (Warmer — subscription opens before the fire) | `AdminConn` nil → warn + empty |
| `logs_tail` | `container`, `service?` | Any container's stdout/stderr (`docker logs -f`) | container unreachable → warn + empty |

Scenarios referencing an unregistered location fail at `expected[]`
evaluation time with the available-locations hint — the operator sees
what they DO have.

---

## 5. Mishap kinds

Each `c.mishap` must be one of the kinds in `catalogs/mishaps/`:

| Kind | What it does | Requires |
|------|-------------|----------|
| `crash` | `docker restart` of the target pod when Apply fires | `DockerCLI` |
| `mongo-partition-500ms` | Toxiproxy disables MongoProxy for 500ms | `ChaosEngine` |
| `cassandra-partition-500ms` | Toxiproxy disables CassandraProxy for 500ms | `ChaosEngine` |

The trigger is pre-closed — the mishap fires immediately as soon as
`Apply` is scheduled, in parallel with the case's assertion loop.
`Cleanup` runs in defer with a fresh 30s context.

---

## 6. Matcher semantics — `matches_shape`

The default (and only) matcher for `expected[].match` is
`matches_shape`:

- **Subset deep match.** The event payload may have extra fields;
  only fields the `match` block mentions must match.
- **Nested maps recurse with the same subset semantics.** Example:
  `match: {body_json: {status: accepted}}` succeeds against a reply
  payload `{body_json: {status: accepted, roomId: r-x, ...}}`.
- **Type normalisation.** JSON-decoded numbers come back as float64;
  the matcher normalizes ints/floats for comparison.
- **Struct payloads** (e.g. `ReplyPayload`, `NATSSubscribePayload`)
  marshal to JSON and unmarshal as a generic map so field assertions
  work transparently.
- **Array match (Relative Order Subset Match — ROSM).** When the
  expected match value is a list, the matcher walks the observed
  slice looking for each expected element in declaration order. Used
  for `nats_subscribe`'s `received[]`, `cassandra_select`'s row list,
  and any other slice-valued field.

For "must NOT happen" assertions, set `not: true` — the loop uses
`Consistently(...).ShouldNot(...)` so any event matching during the
timeout window fails.

---

## 7. A worked example

`scenarios/drafts/create-room-sandbox.yaml`:

```yaml
scenario: Create Room Sandbox
source: design-doc#room-creation (room-service/handler.go:296-374)

seed:
  users:
    alice:
      verified: true
    eve_unverified:
      verified: false

base_input:
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.${site}.create
  payload:
    name: Engineering Chat
    users: ["${alice.account}"]
  credential: ${alice.credential}

cases:
  - name: happy-path-room-created
    tag: positive
    expected:
      - location: reply
        match:
          body_json:
            status: accepted
            roomType: channel
      - location: mongo_find
        args:
          collection: rooms
          filter:
            name: Engineering Chat
        match:
          name: Engineering Chat
          createdBy: ${alice.id}

  - name: unverified-user-cannot-create-room
    tag: negative
    input:
      subject: chat.user.${eve_unverified.account}.request.room.${site}.create
      credential: ${eve_unverified.credential}
    expected:
      - location: reply
        match:
          body_json:
            error: "unauthorized"

  - name: withstands-mongo-partition
    tag: positive
    mishap: mongo-partition-500ms
    expected:
      - location: mongo_find
        args:
          collection: rooms
        match:
          name: Engineering Chat
```

What happens at runtime:

1. **Sandbox.Setup**: drops `users`/`rooms`/`subscriptions`; mints
   alice's NATS JWT via auth-service; builds the PollerReg from the
   live readers.
2. **Case `happy-path-room-created`**: ChaosEngine.Reset → Fire
   nats_request → `Eventually(reply, 5s, 100ms).Should(MatchShape(...))`
   succeeds when the reply payload arrives → `Eventually(mongo_find,
   5s, 100ms)` succeeds when the room lands. Pass.
3. **Case `unverified-user-cannot-create-room`**: Reset → Fire with
   eve's creds → reply carries an "unauthorized" error → match. Pass.
4. **Case `withstands-mongo-partition`**: Reset → spawn the mongo
   partition executor (Toxiproxy disables MongoProxy) → Fire → wait
   500ms for the partition to heal → `Eventually(mongo_find, 5s,
   100ms)` eventually sees the room. Pass.
5. **Teardown**: close poller goroutines, ChaosEngine.Reset.

---

## 8. Common mistakes

| Symptom | Cause | Fix |
|---------|-------|-----|
| `cases[N] tag must be 'positive' or 'negative'` | Empty/missing `tag` | Set `tag: positive` or `tag: negative` |
| `cases[N] expected: must have at least one entry` | Case has no assertions | Add at least one `expected[]` block (or delete the case) |
| `cases[N] duplicate name "X"` | Two cases share a name | Names must be unique within the scenario (drives perf keys) |
| Assertion fails with "polled N events, none matched" | match shape doesn't actually match the payload | Inspect the closest-mismatch reason in the failure message; check substitution resolved correctly |
| Assertion times out at `jetstream_consume` or `nats_subscribe` | `NATS_CREDS_FILE` unset → admin conn nil → poller warns at PollFn | Set `NATS_CREDS_FILE`; runner warns at startup when it's missing |
| Assertion times out at `mongo_find` | Auto-merged `createdAt >= startTime` filter excludes legacy rows; the row may have landed before the case began | Confirm the scenario does write the row; check the scenario's Mongo seeds haven't pre-loaded it; add an explicit `createdAt` clause to override |
| Assertion times out at `cassandra_select` | `MESSAGE_BUCKET_HOURS` mismatch between suite (24h) and production services (default 72h) — reads target a different partition than writes | Set `MESSAGE_BUCKET_HOURS` on both sides; see `docs/integration-suite-sync-register.md` §3.1 |
| `mishap kind "X" has no factory in catalog` | `c.mishap` references a kind not in `catalogs/mishaps/` | Use one of `crash`, `mongo-partition-500ms`, `cassandra-partition-500ms` |
| `${service.backend.credential}` resolves to empty | `NATS_CREDS_FILE` env not set | Set the env to a creds file path; check `buildServiceCreds` in `runner.go` |
