# Spec: Generic Cassandra Seeding Engine

**Status:** Draft for review (no code yet)
**Phase:** 4.3 — History-Read Test Prerequisites
**Spec date:** 2026-06

## 1. Motivation

Phase 4.2 closed the messaging *write-side* gap — `sandbox_rooms.go` now
inserts `rooms` / `subscriptions` / `room_members` Mongo docs so
`message-gatekeeper` accepts a `msg.send` and the canonical event lands
on `MESSAGES_CANONICAL_<site>`.

The *read-side* gap is the symmetric problem: services like
`history-service` only read Cassandra, never write it. A scenario that
asserts pagination needs **pre-existing rows** in `messages_by_room` /
`messages_by_id` / `thread_messages_by_room` *before* the case fires.
With no seeding primitive, the only way to populate history today is
to fire N `msg.send` requests in sequence — multiplying every history
test by the round-trip cost of the full
gatekeeper→canonical→message-worker→Cassandra pipeline, and tangling
the "history pagination is broken" verdict with "did all 50 inbound
sends succeed?"

This phase introduces a **generic Cassandra-row seed primitive** that:

- Lets scenario authors declare arbitrary CQL rows in `seed:` —
  table-name driven, no per-table schema in Go.
- Resolves relative-time tokens (`${now - 2m}`) at substitution time
  so seeded `created_at` values land inside the scenario's
  `[T_open - δ, T_open + δ]` window.
- Auto-computes `bucket` for the production partitioning scheme via
  the same `pkg/msgbucket.Sizer` the cassandra_select primitive uses,
  reusing the configured `MESSAGE_BUCKET_HOURS`.
- Plays cleanly with the existing nil-tolerant
  `if sb.Deps.Cassandra != nil` gate so pure-Go scenarios run untouched.

This unblocks the three drafted history scenarios from the Phase 4.1
gap analysis:

- `history-service-paginates-messages.yaml` (Draft #3) — N=50 rows
  pre-seeded, asserts page-size + ordering.
- `history-service-respects-since-cursor.yaml` — three time-banded
  rows, asserts the `since` cursor filters correctly.
- `messages-by-id-direct-lookup.yaml` — one row, asserts the
  direct-ID lookup path.

## 2. YAML authoring surface

### 2.1 The cassandra_data block

```yaml
seed:
  # EXISTING blocks (unchanged):
  users: {}
  rooms: []
  memberships: {}

  # NEW Phase 4.3 block. List of {table, rows} pairs. One INSERT per
  # row; same row order on the wire as in the YAML.
  cassandra_data:
    - table: messages_by_room
      rows:
        - room_id: r-history-test
          message_id: mPastOne00000000000000
          msg: msg one
          site_id: ${site}
          created_at: ${now - 2m}
          # bucket auto-computed (§3.2) or written explicitly:
          # bucket: "${bucket(created_at)}"
        - room_id: r-history-test
          message_id: mPastTwo00000000000000
          msg: msg two
          site_id: ${site}
          created_at: ${now - 1m}

    - table: messages_by_id
      rows:
        - message_id: mPastOne00000000000000
          room_id: r-history-test
          msg: msg one
          site_id: ${site}
          created_at: ${now - 2m}
```

**Design choices:**

| Choice | Picked | Rejected | Rationale |
|---|---|---|---|
| List-of-tables vs map keyed by table name | **List** | Map | Lists preserve YAML order, which is the simplest deterministic INSERT order. Maps would require sorting by table name for stability and obscure the "physically write these first, then those" mental model. |
| Per-row `{column: value}` map vs positional `[v1, v2, …]` rows | **Map** | Positional | Maps survive column reordering, are self-documenting, and let validation surface the offending column when binding fails. Positional rows would require a parallel `columns:` declaration per table, doubling the YAML surface. |
| Inline schema declaration vs cluster introspection | **Cluster introspection** | Per-row schema | Authors should not have to repeat `cql_type: text` for every column — the gocql cluster metadata already knows the column types. Engine introspects on first insert per (table) tuple, caches for the rest of the scenario. |
| Bucket: auto vs explicit token | **Auto with explicit override** | Required token | The vast majority of seeded rows will want the obvious "bucket from this row's created_at" computation. Defaulting it makes the common case one line shorter; the `${bucket(<col>)}` token stays available for the rare case (asserting bucket-boundary edge behavior) that wants an explicit value. |

### 2.2 Validation rules (enforced at Sandbox.Setup Step 1c)

A new `scenario.ValidateCassandraData(seed)` runs in the same Step-1
pass that today validates user flags + the room-subscription block.
Pure-Go, no Cassandra connection required.

| Rule | Failure mode |
|---|---|
| Every `cassandra_data[].table` is a non-empty identifier matching `^[a-z][a-z0-9_]*$` | `sandbox.Setup: cassandra_data[0]: invalid table name "Foo Bar"` |
| Every entry has at least one row | `sandbox.Setup: cassandra_data[messages_by_room]: rows: must have at least one entry` |
| Every row is a non-empty column map | `sandbox.Setup: cassandra_data[messages_by_room][2]: empty row` |
| Every column name matches `^[a-z][a-z0-9_]*$` (CQL identifier) | `sandbox.Setup: cassandra_data[messages_by_room][0]: invalid column "RoomID" (CQL identifiers must be lower-snake-case)` |
| No row references a `${bucket(<col>)}` whose `<col>` is absent from the same row | `sandbox.Setup: cassandra_data[messages_by_room][0]: bucket(created_at): row has no created_at column` |
| `${now ± <duration>}` parses cleanly via `time.ParseDuration` | `sandbox.Setup: cassandra_data[messages_by_room][0].created_at: invalid duration "2 minutes": time: missing unit` |

Cluster-side rules (table exists, columns exist, types compatible) fire
at `INSERT` time — they require a live session, so we let gocql's error
flow through, wrapped with the failing (table, row index, column) coordinate.

## 3. Substitution: relative time + bucket math

### 3.1 `${now ± <duration>}` tokens

The existing substitute.go path resolves `${now}` to
`time.Now().UTC().UnixMilli()` for jetstream_publish payloads. The
cassandra-seed pipeline needs a richer form:

| Surface | Resolves to | Bound to |
|---|---|---|
| `${now}` | `sb.StartTime.UTC()` (NOT `time.Now()` — T_open is the deterministic anchor) | `time.Time` for `timestamp` columns, `int64` for `bigint` columns |
| `${now - 2m}` | `sb.StartTime.UTC().Add(-2 * time.Minute)` | same |
| `${now + 1h}` | `sb.StartTime.UTC().Add(+1 * time.Hour)` | same |
| `${now - 500ms}` | `sb.StartTime.UTC().Add(-500 * time.Millisecond)` | same |

**Why T_open, not wall-clock at substitution time:** every cassandra
column resolved against `now` in the same Setup pass agrees on a
single anchor. With `time.Now()`, two rows seeded a microsecond apart
would have drifting timestamps — fine in isolation, but a future
`bucket(...)` token that closes over the resolved value could land
either side of a window boundary depending on scheduling jitter.

**Grammar:** A new dedicated parser (NOT extending the substitute.go
catchall) accepts the closed grammar `${now}` | `${now <space>* [+-]
<space>* <duration>}` where `<duration>` is anything `time.ParseDuration`
accepts. Whitespace tolerant. Anything else under `${now ...}` is an
error — fail loud, never silently fall through to "literal string".

**Where it lives:** `internal/scenario/cassandra_subst.go` — new file,
the regex + parser. Kept separate from the runtime's substitute.go so
the pure-Go test surface stays clean and so the existing
`${now}`→int64 semantic for verb payloads is unaffected (different
context, different binding).

### 3.2 `${bucket(<col>)}` tokens — explicit form

When a row has a `bucket BIGINT` column and the author wants explicit
control:

```yaml
- table: messages_by_room
  rows:
    - room_id: r-history-test
      created_at: ${now - 2h}
      bucket: ${bucket(created_at)}   # ← explicit; resolves to floor(created_at_ms / windowMs) * windowMs
      message_id: mEdgeCase00000000000
```

The token's argument is a **column name in the same row**. The engine
walks the row map twice:

1. **Pass 1** — resolve every non-`bucket(...)` token. After this pass
   `created_at` holds a concrete `time.Time`.
2. **Pass 2** — resolve every `${bucket(<col>)}` against the
   already-resolved row. `<col>` MUST resolve to a `time.Time`;
   anything else is an error naming the row coordinate and the actual
   type.

The bucket math reuses `pkg/msgbucket.Sizer.Of(t)` so the seed engine
and `cassandra_select` poller agree on the partition key — the same
`Sandbox.Deps.MessageBucketWindow` field gates both.

### 3.3 Auto-bucket (default behavior)

Most seeded rows don't need to think about partitioning. When:

- The table has both a `bucket BIGINT` column AND a `created_at TIMESTAMP`
  column (introspected from cluster metadata on first INSERT per
  table), AND
- The row author did NOT supply `bucket` explicitly,

…the engine auto-injects `bucket = sizer.Of(row["created_at"].(time.Time))`.

Auto-bucket fires AFTER Pass 1 (so `created_at` is already a
`time.Time`) and BEFORE binding. The cluster metadata cache means it's
free after the first row in the same table.

**The introspection is the only piece of cluster-side schema knowledge
the engine carries.** Column lists, types, partition keys all come
from `gocql.Session.KeyspaceMetadata(<keyspace>)`. Nothing is
hardcoded by table name in Go — auto-bucket is generic at the schema
level (any `(bucket, created_at)` table works) even though it has a
single production target today.

### 3.4 Cross-references to user + room placeholders

A seeded row's value can reference any placeholder already in the
sandbox: `${alice.id}`, `${alice.account}`, `${site}`. These flow
through the SAME runtime substitution path used by `base_input.payload`
(internal/runtime/substitute.go), so syntax matches what authors
already know.

For values that flow through gocql as text/varchar (most user-facing
fields), the resolved placeholder is a string. No further coercion.

## 4. Sandbox.Setup lifecycle placement

### 4.1 Current ordering vs Phase 4.3 ordering

Today's Setup (per sandbox.go after Phase 4.2):

1. Validate user flags (Step 1)
1b. Validate seed room block
2. Capture `sb.StartTime`
3. Chaos reset
4. Drop sandbox-owned Mongo collections
5. Materialize SeedUsers + apply effects
6. Insert user-profile docs
7. Insert seeded rooms + subscriptions + room_members
8. Build Placeholders
9. Build PollerReg

Phase 4.3 adds two changes, **plus moves Placeholders earlier**:

1. Validate user flags
1b. Validate seed room block
1c. **NEW**: Validate `cassandra_data` block
2. Capture `sb.StartTime`
3. Chaos reset
4. Drop sandbox-owned Mongo collections
4b. **NEW**: TRUNCATE sandbox-owned Cassandra tables
5. Materialize SeedUsers + apply effects
6. Insert user-profile docs
7. **MOVED**: Build Placeholders (was step 8)
8. Insert seeded rooms + subscriptions + room_members
9. **NEW**: Insert `cassandra_data` rows
10. Build PollerReg

**Why move Placeholders to Step 7:** values inside `cassandra_data`
rows reference `${alice.id}` etc. The seeding engine needs the
populated Placeholders map. Today's late-Placeholders position is
historical — nothing reads them between materialization and Step 8
under Phase 4.2's ordering. Moving them up is a pure refactor; the
room-insertion step (Step 8 in the new ordering) gains the same
capability as a bonus (a future scenario could declare
`name: "${alice.account}'s room"`).

### 4.2 TRUNCATE strategy

Cassandra's only efficient bulk-clear is `TRUNCATE`. The harness owns
a `sandboxOwnedCassandraTables` slice (mirroring
`sandboxOwnedCollections`), initially populated with the production
write-targets:

```go
var sandboxOwnedCassandraTables = []string{
    "messages_by_room",
    "messages_by_id",
    "thread_messages_by_room",
    "pinned_messages_by_room",
}
```

Step 4b truncates each table in sequence. Failure is a hard error
(can't honor the byte-identical-state invariant if a TRUNCATE silently
fails). The same nil-tolerant `if sb.Deps.Cassandra != nil` gate
short-circuits when running unit tests without infra.

**Why TRUNCATE all four, not just tables referenced by seed.cassandra_data:**
the read-side scenarios assert on what the SERVICE returns from
Cassandra, not the suite's view. A stale row left from a prior
scenario would leak into the SERVICE's response even if THIS scenario
doesn't pre-seed that table.

**Why not per-test keyspaces:** the production-DDL chat keyspace is
shared infra (initialized by `infra/cassandra_init.go` from
`docker-local/cassandra/init/*.cql`). Standing up a fresh keyspace
per scenario would multiply Setup latency by O(DDL files) and break
the assumption that the production schema is the test schema.

### 4.3 Nil-tolerance gates

Every Cassandra touch sits behind a single early-return guard:

```go
// Step 4b: truncate sandbox-owned Cassandra tables.
if sb.Deps.Cassandra != nil {
    if err := truncateSandboxCassandraTables(ctx, sb); err != nil {
        return fmt.Errorf("sandbox.Setup: truncate cassandra: %w", err)
    }
}

// Step 9: insert cassandra_data rows.
if sb.Deps.Cassandra != nil && len(sb.Scenario.Seed.CassandraData) > 0 {
    if err := insertSeededCassandraRows(ctx, sb); err != nil {
        return fmt.Errorf("sandbox.Setup: seed cassandra: %w", err)
    }
}
```

Mirrors today's `if sb.Deps.Mongo != nil` discipline so unit-test
sandboxes (deps zero-valued) stay green.

## 5. Engine architecture

### 5.1 Where the code lives

| File | Role |
|---|---|
| `internal/scenario/types.go` | Add `SeedBlock.CassandraData` + `SeedCassandraTable` + `SeedCassandraRow` types. |
| `internal/scenario/validate_cassandra_seed.go` (new) | `ValidateCassandraData(seed)` — the six rules from §2.2. |
| `internal/scenario/cassandra_subst.go` (new) | `${now ± duration}` + `${bucket(<col>)}` parsers + resolvers. Pure-Go, no gocql import. |
| `internal/runtime/sandbox_cassandra.go` (new) | `truncateSandboxCassandraTables` + `insertSeededCassandraRows`. Pulls the substitution pipeline from `internal/scenario`, hands it the sandbox's StartTime + Placeholders + Sizer. |
| `internal/runtime/sandbox.go` (edit) | Wire Step 1c, Step 4b, Step 9; move Placeholders to Step 7; add `sandboxOwnedCassandraTables`. |

### 5.2 Substitution pipeline (per row)

```
   YAML map[string]any
        │
        ▼
   Pass 1: walk values, apply
     • ${now}, ${now ± dur}    → time.Time
     • ${<alias>.<field>}      → string (existing runtime substitute.go)
     • ${site}                 → string
        │
        ▼
   Pass 2: walk values, apply
     • ${bucket(<col>)}        → int64 (reads pass-1 result, calls msgbucket.Of)
        │
        ▼
   Pass 3 (cluster-schema-aware):
     • Auto-inject bucket if column exists in table AND row didn't set it
        │
        ▼
   Sorted column list + parallel []any binds
        │
        ▼
   INSERT INTO <table> (col1, col2, …) VALUES (?, ?, …)
        │
        ▼
   gocql.Session.Query(stmt, binds...).Exec()
```

### 5.3 CQL assembly

Column order is **lexically sorted** so the parallel `[]any` binds
slice has a deterministic, easy-to-reason-about order. The same row
materialized twice produces byte-identical SQL — useful when comparing
generated CQL in unit tests, and when failure messages name specific
positions.

Statement template:

```cql
INSERT INTO <keyspace>.<table> (<col1>, <col2>, …, <colN>) VALUES (?, ?, …, ?)
```

The keyspace prefix is the sandbox's configured keyspace (already
known via the gocql Session). Single statement per row — no
UnloggedBatch yet (introduce per-table batching in Phase 4.3.1 if a
scenario seeds >100 rows and Setup latency becomes a concern;
50-row history scenarios are well below that threshold).

### 5.4 gocql binding (scalar columns + nil)

The MVP supports the column types that the four production message
tables actually use as scalars: `text`, `bigint`, `int`, `boolean`,
`timestamp`, `blob`. Mapping from resolved Go value → gocql bind:

| Resolved Go type | CQL column | Bind |
|---|---|---|
| `string` | text / varchar | direct |
| `int64` / `int` | bigint / int | direct |
| `bool` | boolean | direct |
| `time.Time` | timestamp | direct |
| `[]byte` | blob | direct |
| `nil` / absent | (any) | column omitted from INSERT; CQL null |

**UDT support (sender, mentions, etc.) is explicitly deferred to
Phase 4.3.1.** History-pagination tests assert on row count + order +
scalar fields; a null `sender` is valid Cassandra state and the
history-service reply still echoes the row. Concrete UDT support
arrives when a scenario actually asserts on `sender.account` in the
service reply, and lands as a separate spec (the cluster-metadata path
to introspect UDT field types is the same shape; just more code).

### 5.5 Two pure-Go layers + one runtime layer

A deliberate split:

| Layer | Package | Tests run without |
|---|---|---|
| Types + validation | `scenario` | …anything (pure-Go map manipulation) |
| Substitution (now/bucket) | `scenario` | …anything; takes a clock-injectable `time.Time` for T_open |
| Cassandra session + INSERT | `runtime` | …gocql + a real or fake `cassandraExecutor` interface |

The `cassandraExecutor` seam lets `sandbox_cassandra_test.go` (pure-Go)
exercise the orchestration path without a Cassandra container by
substituting a recording executor that captures the (stmt, binds)
pair. Integration tests still validate the live gocql round-trip.

## 6. Test plan

### 6.1 Pure-Go unit tests (no Docker)

**`internal/scenario/types_test.go` — YAML decoding**

- `cassandra_data` round-trips through `yaml.Unmarshal`: list of
  tables, each with rows, each with column map. Both leaves are
  raw types (`any`) so token strings survive intact.
- Mixed scalar types in one row (string + int + bool + time-literal)
  decode without coercion drift.

**`internal/scenario/validate_cassandra_seed_test.go` — six rules**

One Red-Green pair per rule from §2.2, asserting exact error text +
the offending coordinate (table name, row index, column name).

**`internal/scenario/cassandra_subst_test.go` — substitution**

- `${now}` resolves exactly to the supplied T_open.
- `${now - 2m}` resolves to T_open − 2 minutes (assert ms-precision).
- `${now + 1h}` resolves to T_open + 1 hour.
- Whitespace permutations: `${now-2m}`, `${ now - 2m }`, `${now   -   2m}`.
- Malformed durations: `${now - 2 minutes}` errors naming the row + col.
- Unknown operator: `${now * 2m}` errors with the closed list `+|-`.
- `${bucket(created_at)}` resolves AFTER the column does — chained
  resolution within one row.
- `${bucket(missing_col)}` errors with row coordinate + the absent column.
- `${bucket(string_col)}` errors naming the non-time type.

**`internal/runtime/sandbox_cassandra_test.go` — orchestration**

Uses a recording `cassandraExecutor`:

- Nil session → no-op (no executor calls).
- Empty `cassandra_data` → no-op.
- One row with `${now - 2m}` → executor sees exactly one
  `INSERT INTO messages_by_room (bucket, created_at, message_id,
  msg, room_id, site_id) VALUES (?, ?, ?, ?, ?, ?)` with binds
  ordered to match the sorted column list.
- Two tables, three rows total → executor sees three INSERTs in
  YAML order, one statement per row.
- Auto-bucket: a row WITHOUT explicit `bucket` and a fake schema map
  containing `bucket BIGINT + created_at TIMESTAMP` injects the
  computed bucket value.
- Auto-bucket OFF: same row with the schema map omitting `bucket`
  does not inject (no surprise columns added).

### 6.2 Docker-tagged integration tests

**`internal/runtime/sandbox_cassandra_integration_test.go`**
(build tag `integration`):

- `TestSandboxSetup_SeedCassandraRowsRoundTrip` — runs against
  `testutil.CassandraKeyspace`. Seeds 3 rows across `messages_by_room`
  + `messages_by_id`, calls `Sandbox.Setup`, then `SELECT JSON *` from
  each table. Asserts:
  - Row count matches per table.
  - `created_at` values land within ±10ms of the expected
    `T_open − N` shift (clock drift tolerance).
  - `bucket` (auto-computed) equals `Sizer.Of(created_at)` for the
    configured `MESSAGE_BUCKET_HOURS`.
  - `msg` + `room_id` + `site_id` round-trip byte-identical.

- `TestSandboxSetup_TruncatesOwnedTablesAtSetup` — prepopulate
  `messages_by_room` with a stale row whose `room_id` is unique to
  this test, run Setup, query: stale row must be gone. Mirrors the
  Phase 4.2 `room_members` drop-discipline pin.

- `TestSandboxSetup_CassandraNilSkipsEngine` — Cassandra-less
  Sandbox with non-empty `cassandra_data` — Setup completes cleanly,
  no panic, the gate short-circuits.

- `TestSandboxSetup_ExplicitBucketTokenWins` — row sets
  `bucket: ${bucket(created_at)}` AND auto-bucket would have run;
  result equals the explicit form (proves the two paths agree).

- `TestSandboxSetup_PlaceholderRefInCassandraValue` — row sets
  `user_id: ${alice.id}` and asserts the inserted row's `user_id`
  column matches `sb.Users["alice"].ID`.

### 6.3 Coverage targets

Per CLAUDE.md's 80% minimum: pure-Go layers should land at 95%+
(every error path is exercised by a Red test); the runtime layer
lands at 85%+ once integration tests cover the gocql round-trip.

## 7. Backward compatibility

- `SeedBlock.CassandraData` is a new optional field; nil/empty for
  every pre-Phase-4.3 scenario.
- `ValidateCassandraData` returns nil cleanly when the block is empty.
- Step 4b's TRUNCATE only runs when `sb.Deps.Cassandra != nil`. The
  current 9 active scenarios that DON'T pre-seed Cassandra
  unaffected, because their assertions filter by `created_at >= T_open`
  via the `cassandra_select` poller (so leftover rows from prior
  scenarios are already hidden under today's pattern).
- Moving Placeholders to Step 7 is internal-only — no caller outside
  Sandbox.Setup reads them mid-setup.

## 8. Open questions for review

| # | Question | Default if no input |
|---|---|---|
| Q1 | Should the engine accept `${now}` resolving to wall-clock `time.Now()` when `sb.StartTime` is the literal zero value, to support unit tests that didn't go through Setup? | **No** — unit tests inject T_open explicitly through the substitution function signature. Allowing zero-fallback would silently desync the cassandra_select poller's window. |
| Q2 | Should auto-bucket fire only on a hardcoded allow-list of tables, even though the introspection is generic? | **No** — generic by design. Any `(bucket, created_at)` table in the keyspace gets the convenience. Cluster metadata IS the source of truth. |
| Q3 | Should we support `${bucket(<col>, -1)}` for "the bucket BEFORE the one containing col"? | **Defer** — useful for bucket-boundary scenarios, but no current draft needs it. Land if Phase 4.3.1 adds a "history paginates across bucket boundary" test. |
| Q4 | Should UDT support (sender/mentions) land in this spec or 4.3.1? | **Defer to 4.3.1** — none of the three drafted history scenarios assert on UDT fields; scoping it out now keeps PR diffs small. |
| Q5 | Should the engine support `{cql_type: ...}` value-tagging to override scalar inference? | **No (Phase 4.3)** — gocql binds map[string]any reflectively against the cluster schema. Defer until a real type-mismatch surfaces. |
| Q6 | Should TRUNCATE happen before or after Mongo Drop in Step 4? | **After** — keep Mongo (the fast one) first so the slower Cassandra wipe doesn't bottleneck the case loop's first scenario. Setup-latency micro-optimization. |
| Q7 | Should the YAML accept `seed.cassandra_data` keyed by table name (map form) AS WELL AS the list form? | **No** — one form. Map form would obscure ordering and complicate validation. |

## 9. Non-goals (explicit)

- **UDT seeding** (sender, mentions, quoted_parent_message,
  card/file/cardAction). Land in Phase 4.3.1 if a scenario needs it.
- **Counter columns, collections (list/set/map of UDT).** Same — wait
  for the first scenario that needs them.
- **CQL3 prepared statements / batching.** Each row is a one-shot
  `Session.Query().Exec()`. Optimization deferred until a scenario
  seeds enough rows that Setup latency is measurable.
- **Mongo + Cassandra atomic seed.** No cross-store transaction. The
  individual nil-tolerant gates mean partial-Setup failure is loud
  (sandbox.Setup returns the error).
- **Read-back seeded rows via cassandra_select with the auto-bind
  heuristic.** Scenarios that assert "seeded row X is present" must
  use explicit `params: [...]` — the auto-`?` binds to T_open which
  filters out anything `created_at < T_open`. Most history
  scenarios assert on the SERVICE reply, so this is rarely an issue;
  the spec calls it out to set author expectations.
- **YAML token form for Cassandra-side INSERT options
  (`USING TTL`, `USING TIMESTAMP`).** No current draft needs them.

## 10. Net change estimate

| Layer | Files | Lines (approx) |
|---|---|---|
| Types | `internal/scenario/types.go` (struct additions) + `internal/scenario/types_test.go` (new tests) | ~80 |
| Validation | `internal/scenario/validate_cassandra_seed.go` (new) + `internal/scenario/validate_cassandra_seed_test.go` (new) | ~200 |
| Substitution | `internal/scenario/cassandra_subst.go` (new) + `internal/scenario/cassandra_subst_test.go` (new) | ~240 |
| Sandbox engine | `internal/runtime/sandbox_cassandra.go` (new) + `internal/runtime/sandbox.go` (edits) + `internal/runtime/sandbox_cassandra_test.go` (recording executor) | ~280 |
| Integration | `internal/runtime/sandbox_cassandra_integration_test.go` (new, build-tagged) | ~220 |

Total: ~1020 lines added, **zero pkg/model changes** (black-box principle).

## 11. Recommended landing path

1. **Land this spec** (commit + push) — gets the design under review
   before any code moves.
2. **Pure-Go PR**: types + validator + substitution parser + their
   tests. No sandbox touch, no Cassandra. Mirrors the Phase 4.2 Step 1
   discipline.
3. **Sandbox wiring PR**: `sandbox_cassandra.go` +
   sandbox.go edits + Placeholders refactor + recording-executor
   unit tests + integration test. Lands the live INSERTs.
4. **First read-side scenario PR**: `history-service-paginates-messages.yaml`
   (Draft #3) — proves the engine unblocks a real read-side test.

PRs 2 + 3 should be sequenced (the wiring PR builds on the types +
parser). PR 4 demonstrates the whole stack end-to-end.

---

**Approval needed before any code lands.** Specifically:

- The §2.1 YAML shape (list-of-tables + per-row map + token strings).
- The §3.3 auto-bucket via cluster introspection (vs. always-explicit
  `${bucket(<col>)}`).
- The §4.1 Placeholders-moves-to-Step-7 refactor (small, but it does
  touch the existing ordering invariant).
- The §4.2 TRUNCATE strategy (the four-table baseline list, and the
  "always TRUNCATE all four" policy vs "only tables in
  cassandra_data").
- The §5.4 scalar-only MVP scope (UDTs deferred to 4.3.1).
- The seven open questions in §8.
