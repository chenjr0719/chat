# Integration-Suite ↔ Application Sync Register

**Status:** Living document
**Owner:** Whoever last touched the suite or the production source-of-truth
**First published:** 2026-06
**Last updated:** 2026-06

## 1. Purpose

The Phase 4.0 architecture made the integration suite a **universal-primitive
black box**: every domain-specific fact (table names, subject patterns,
bucket math, error wording, container names) lives in scenario YAML or in
small constants at the suite layer, never compiled in as Go strings the
application can change without anyone noticing.

But "black box" does not mean "decoupled" — the suite still has to KNOW
the values to assert against them. Those values are mirrored from
upstream sources: CLAUDE.md, the Cassandra DDL files, `pkg/subject`,
`pkg/stream`, `pkg/idgen`, service `main.go` envDefaults, and the
production slog/error wording.

When upstream shifts, the suite goes silently wrong:

- Rows land in a partition the service never queries (Finding 20 —
  bucket window mismatch).
- Logs no longer match the asserted text (Finding 18 — exact-string
  drift).
- A new Mongo collection gets stale rows across scenarios because the
  drop list never learned about it.

This register catalogs **every place the suite holds a value that
mirrors an upstream source**, so when upstream changes, maintainers
have one document to consult and update.

## 2. How to use this doc

**When changing the suite:** if you're adding a new hardcoded constant
that mirrors a production value (a new owned collection, a new default,
a new subject pattern), add it here in the relevant section.

**When changing the application:** before merging a PR that touches
any of these upstream sources, grep this doc for the surface you're
touching and update the suite-side hardcode in the same PR. The doc
exists so this can be a checklist item, not a guessing game.

**When debugging a suite failure that smells like drift:** scan §4
for the surface the failing assertion touches. Compare the suite-side
hardcode against the upstream source-of-truth. The mismatch is the
root cause more often than you'd expect (Finding 20 was three turns
of misdirection before the bucket-window mismatch surfaced).

**When the doc itself is out of date:** that's the worst-case
failure mode. Counter-pressure: any new suite hardcode that doesn't
appear here is a latent foot-gun. Code review for suite changes
should include "is this in the sync register?" as a checklist line.

## 3. Known active drift

### 3.1 `MESSAGE_BUCKET_HOURS` (Finding 20 — resolved)

| Surface | Value | Source |
|---|---|---|
| Suite `defaultBucketWindow` | **24h** | `tools/integration-suite/internal/runtime/sandbox_cassandra.go:40` |
| CLAUDE.md §6 | **24h** ("default 24") | `CLAUDE.md` |
| `history-service` runtime default | **24h** | `history-service/internal/config/config.go:39` (`envDefault:"24"`) |
| `message-worker` runtime default | **24h** | `message-worker/main.go:37` (`envDefault:"24"`) |
| Suite runner default | **24** | `tools/integration-suite/cmd/runner/main.go` (`envInt("MESSAGE_BUCKET_HOURS", 24)`) |
| `USE_INFRA=true` env injection | **threaded** | `cfg.MessageBucketHours` → `infra.Config.MessageBucketHours` → `startService` → `serviceEnv` sets `MESSAGE_BUCKET_HOURS` on history-service + message-worker |

**Status:** drift reconciled (2026-06). Service envDefaults were
moved from `72` → `24` so CLAUDE.md §6, the suite's seed window, and
the production code defaults all agree. The suite additionally plumbs
its own `cfg.MessageBucketHours` into the spawned containers via
`serviceEnv`, so an explicit `MESSAGE_BUCKET_HOURS=42` on a suite run
flows to the services that read/write `messages_by_room` and stays
self-consistent.

**`history_service_paginates_messages.yaml`** is no longer drift-gated
— it passes against the reconciled defaults.

## 4. The register

Organized by axis of drift. Each entry: where the suite hardcodes the
value, where the upstream source-of-truth lives, what breaks if they
diverge.

### 4.1 Time & bucket constants

| Constant | Suite | Upstream | Drift symptom |
|---|---|---|---|
| `MESSAGE_BUCKET_HOURS` window | `internal/runtime/sandbox_cassandra.go:40` (`defaultBucketWindow = 24 * time.Hour`) | CLAUDE.md §6 + per-service `MESSAGE_BUCKET_HOURS` envDefault | Silent partition mismatch on bucketed reads (Finding 20). |
| `historyFloorDays` budget | scenarios use `created_at: ${now - 1h}` defensively | `history-service/internal/config/config.go` `HistoryFloorDays` envDefault `365` | If the production floor shrinks below 1h, room-aging tokens stop dropping the floor low enough. |
| `clockSkewTolerance` for meta-override hints | (no suite use today; scenarios that override `meta.lastMsgAt` use `${now}` so they stay within tolerance) | `history-service/internal/service/room_times.go:41` (`1 * time.Hour`) | If shrunk, `${now}`-based meta overrides may be rejected. |

### 4.2 Cassandra schema — tables & owned set

| Surface | Suite | Upstream | Drift symptom |
|---|---|---|---|
| Table allow-list for truncate-at-Setup | `internal/runtime/sandbox_cassandra.go:23-28` (`sandboxOwnedCassandraTables = [messages_by_room, messages_by_id, thread_messages_by_room, pinned_messages_by_room]`) | `docker-local/cassandra/init/*.cql` + `docs/cassandra_message_model.md` | New production table → not truncated → stale rows leak between scenarios. |
| Auto-bucket column detection | `internal/runtime/sandbox_cassandra.go:applyAutoBucket` (looks for `bucket` + `created_at` columns by name) | `docs/cassandra_message_model.md` UDT / table column names | Rename `bucket` → `partition_bucket` (or similar) silently disables auto-bucket. Scenarios that omit the explicit `${bucket(...)}` token start writing to the default partition (likely 0). |
| Default keyspace | `internal/runtime/sandbox_cassandra.go:34` (`defaultCassandraKeyspace = "chat"`) | `docker-local/cassandra/init/01-keyspace.cql` + service `CASSANDRA_KEYSPACE` envDefault | Wrong keyspace → cluster metadata lookups fail → auto-bucket silently disables, scenarios degrade. |

### 4.3 MongoDB schema — collections & owned set

| Surface | Suite | Upstream | Drift symptom |
|---|---|---|---|
| Collection allow-list for drop-at-Setup | `internal/runtime/sandbox.go:27` (`sandboxOwnedCollections = [users, rooms, subscriptions, room_members]`) | CLAUDE.md §6 + per-service `store_mongo.go` | New collection in production code → suite doesn't drop it → cross-scenario state leak. |
| Default Mongo database name | `internal/runtime/runner.go:189` (`mongoClient.Database(cfg.MongoDB)`) — cfg defaults to `"chat"` via env | per-service `MONGO_DB` envDefault `"chat"` | Suite + services pick different DBs → suite seeds against db A, service reads from db B → empty everywhere. |
| `bson:"_id"` mapping convention | `internal/runtime/sandbox_rooms.go` bson.M docs (e.g. `"_id": r.ID`) | `pkg/model.Room`/`Subscription`/`RoomMember` bson tags | If `_id` is renamed in the model, suite-written rows are present but un-findable. |

### 4.4 NATS subjects

| Subject family | Suite reference (YAML / docs) | Upstream source-of-truth | Drift symptom |
|---|---|---|---|
| `chat.user.{account}.request.room.{roomID}.{siteID}.{verb}` | scenario YAML `base_input.subject` patterns | `pkg/subject/subject.go:159` (UserRoomRequest) | Routing pattern change → gatekeeper / room-service / history-service no longer receive the request → assertion times out. |
| `chat.user.{account}.room.{roomID}.{siteID}.msg.send` | Draft #1 / #2 base_input | `pkg/subject/subject.go:244` (MessageSendPattern) | Same as above. |
| `chat.msg.canonical.{siteID}.created` | Draft #1 cassandra_select context + scenario YAML filter_subject | `pkg/subject/subject.go:152` (MsgCanonicalCreated) | Filter no longer matches → jetstream_consume primitives time out. |
| `chat.room.{roomID}.event` | Phase 4.5 demonstration scenario nats_subscribe args | `pkg/subject/subject.go:164` (RoomEvent) | Broadcast lands on different subject → nats_subscribe sees nothing. |
| `chat.user.{account}.event.room` | (no current scenario; available for notification fan-out scenarios) | `pkg/subject/subject.go:168` (UserRoomEvent) | Same as above. |
| `chat.room.canonical.{siteID}.>` | Draft `room-worker-persists-canonical-create.yaml` filter | `pkg/subject/subject.go` rooms-canonical pattern | Wildcard miss → consumer empty. |

### 4.5 JetStream streams

| Stream | Suite reference | Upstream | Drift symptom |
|---|---|---|---|
| `MESSAGES_<siteID>` | YAML args.stream (e.g. Draft #1) | `pkg/stream/stream.go` + CLAUDE.md §6 | Stream rename → ephemeral consumer fails to open → jetstream_consume warns + times out. |
| `MESSAGES_CANONICAL_<siteID>` | Draft #1, Draft #2, Phase 4.5 demo | same | same |
| `ROOMS_<siteID>` | room-worker-* scenarios | same | same |
| `OUTBOX_<siteID>` | (no current scenario) | same | same |
| `INBOX_<siteID>` | (no current scenario) | same | same |

### 4.6 ID formats

| Format | Suite reference | Upstream | Drift symptom |
|---|---|---|---|
| 20-char base62 message ID | scenario payload literals (e.g. `id: mBroadcastEvent00001`) | `pkg/idgen/idgen.go:87` `IsValidMessageID` | Gatekeeper handler.go:168 rejects → case fails at the validation gate, not at the assertion. |
| Hyphenated UUIDv7 `requestId` | scenario payload literals (e.g. `requestId: 01970000-0000-7000-8000-000000000301`) | `pkg/idgen/idgen.go` `IsValidUUID` + gatekeeper handler.go:163 | Same shape — gatekeeper rejection. |
| Mongo doc `_id` formats — UUIDv7 hex (subscriptions, room_members), 17-char base62 (channel rooms), sorted-concat-of-IDs (DMs), 20-char base62 (messages) | `internal/runtime/sandbox_rooms.go` uses `"sub-" + alias + "-" + roomID`, `"rm-" + alias + "-" + roomID` for predictability | CLAUDE.md §6 "Primary keys" enumeration + `pkg/idgen` | Suite's deterministic IDs don't validate as production formats → production code that reads the `_id` field via `idgen.Is*` checks rejects them. Currently NO scenario triggers this because the validations run on inbound IDs only. |
| Site ID | `internal/runtime/sandbox.go` `defaultSiteID = "site-local"` | docker-local convention; cfg env default | Wrong site → cross-site subjects miss; scenarios using `${site}` get the wrong value. |

### 4.7 Container names (logs_tail)

| Container | Scenarios using it | Upstream | Drift symptom |
|---|---|---|---|
| `message-gatekeeper` | Draft #2 (`message-gatekeeper-rejects-unsubscribed-sender.yaml`) | `tools/integration-suite/internal/infra/config.go:60` + `docker-local/docker-compose.yml` | Container rename → `docker logs` target missing → `logs_tail` warns + times out. |
| `room-worker` | `room-worker-rejects-missing-valkey-key.yaml`, `empty-create-request-rejected.yaml` | same | same |
| `history-service` | Phase 4.3 demo (`history-service-paginates-messages.yaml`) | same | same |
| `room-service`, `message-worker`, `broadcast-worker`, `notification-worker`, `auth-service`, `inbox-worker`, `search-service` | (no current scenario) | same | same |

### 4.8 Log strings & error formats (HIGH-DRIFT SURFACE)

`matches_shape` uses exact-equality for scalar string values — substring
matching is NOT supported. Every log assertion is a tight coupling to
the production slog wording.

| Production string | Suite assertion location | Upstream | Drift symptom |
|---|---|---|---|
| `"process message failed"` (gatekeeper slog.Error msg) | Draft #2 `match.msg` | `message-gatekeeper/handler.go:85` | Slog message rename → exact match fails. |
| `"user %s is not subscribed to room %s"` (wrapped error format) | Draft #2 `match.error` (Finding 18) | `message-gatekeeper/handler.go:195` | Wrapping format change → exact-equality fails. Caught in Finding 18 by a live USE_INFRA run. |
| `"nats request"` (natsrouter Logging middleware) | Draft #3 `match.msg` | `pkg/natsrouter/middleware.go:68` | Middleware message rename → exact match fails. |
| `"process message failed"` (room-worker async-job-failed) + `"room key absent for %s"` | `room-worker-rejects-missing-valkey-key.yaml` | `room-worker/handler.go` slog.Error sites | same |
| `"async room job failed"` + `"operation": "room.create"` | same scenario | `room-worker/handler.go` operation tag | same |

**Anti-drift discipline for new log assertions:** before adding any
log assertion to a scenario, copy the EXACT message text from a real
log line (not the source code's format string — production may wrap
or filter). When adding a new error format string to a service, grep
the suite scenarios for the OLD format string. The above table is
intentionally not exhaustive — keep this section current.

### 4.9 Default deployment values

| Value | Suite default | Upstream convention | Drift symptom |
|---|---|---|---|
| `siteID` | `"site-local"` (sandbox.go) | docker-local single-site naming | Multi-site scenarios that hardcode `site-local` break in cross-site infra. |
| `keyspace` | `"chat"` (sandbox_cassandra.go) | service `CASSANDRA_KEYSPACE` envDefault | If production keyspace renames, suite cluster-metadata calls fail silently → auto-bucket disabled. |
| Mongo DB | `"chat"` (cfg envDefault `MONGO_DB`) | service `MONGO_DB` envDefault | Same drift class as keyspace. |
| NATS admin creds account | (operator JWT — opaque from suite side) | provisioned by auth-service NATS callout | Auth changes invalidate the cred file → admin connection fails at startup → jetstream_consume + nats_subscribe both degrade. |

### 4.10 Runner-only knobs (no upstream mirror)

Knobs that exist purely on the suite side — they don't mirror any
production source-of-truth, so drift doesn't apply. Listed for
completeness so maintainers know which env vars are runner-internal
vs. service-mirrored.

| Knob | Suite location | Purpose |
|---|---|---|
| `INTERACTIVE` | `rt.Config.Interactive` (Phase 4.6) | Opt-in stdin-driven menu loop. Default false = today's batch sweep. See `docs/spec-scenario-dev-mode.md`. |
| `SCENARIOS_DIR` | `rt.Config.ScenariosDir` | Default `"scenarios"`; can repoint at `scenarios/approved/` for CI-gating runs. |
| `CATALOGS_DIR` | `rt.Config.CatalogsDir` | Default `"catalogs"`. |
| `OUTPUT_PATH` / `APPROVED_OUTPUT_PATH` / `PERFORMANCE_PATH` | `rt.Config.*Path` | Report destinations; runner-internal. |

## 5. Drift detection workflow

The register is HUMAN-MAINTAINED. There is no script. The two
mechanisms for catching drift in practice:

### 5.1 Live infrastructure runs

`USE_INFRA=true make local` against the real Docker stack is the
canonical drift detector. A scenario that previously passed and now
fails with a "MISSING" / "no events" / "exact-string mismatch"
diagnostic is almost certainly drift. The Phase 4.4 ROSM diagnostics
("RELATIVE-ORDER VIOLATION", "closest candidate's diff: …") will name
the failing field; cross-reference §4 for the upstream source.

### 5.2 Code-review checklist for upstream-source PRs

When a PR touches any of these production files, the reviewer asks
"is this in `docs/integration-suite-sync-register.md`?":

- `CLAUDE.md` (any section change)
- `docs/cassandra_message_model.md`
- `pkg/subject/subject.go`
- `pkg/stream/stream.go`
- `pkg/idgen/idgen.go`
- `pkg/model/*.go` (struct shape / bson-tag changes)
- any service `main.go` envDefault block
- any service slog.Error / fmt.Errorf format string
- `docker-local/cassandra/init/*.cql`
- `docker-local/docker-compose.yml` (container rename)

If yes → update the corresponding suite hardcode in the same PR, or
flag it explicitly as "drift accepted; will fail scenario X" so the
next live run isn't a surprise.

### 5.3 Adding to this register

Any new suite-side constant that mirrors an upstream source MUST
appear here BEFORE the PR landing it merges. The doc lives at a
known path (`docs/integration-suite-sync-register.md`); reviewers
of suite PRs should check the diff.

## 6. Maintenance discipline

| Concern | Discipline |
|---|---|
| Doc rot | The Section 3 "Known active drift" list is the strongest signal that the doc is current. An empty Section 3 means "we believe everything in §4 is in sync." If a scenario fails in a live run and the failure points at §4, Section 3 gains an entry until the drift is resolved. |
| Discoverability | This doc lives under `docs/` next to the other suite specs (`spec-*.md`). New maintainers find it via the same `docs/` listing they consult for the four `spec-*` docs from Phases 4.2–4.5. |
| Authority | When upstream and suite disagree, **upstream wins** unless the project has explicitly ruled otherwise. The suite is a black-box observer; it doesn't get to override what production does. The `MESSAGE_BUCKET_HOURS` drift (§3.1) was resolved by aligning the production envDefaults with CLAUDE.md's documented 24h, not by changing the suite. |
| Anti-pattern: code-encoded drift | Do NOT add `// TODO drift-XXX` comments scattered through the suite as the drift register. They scatter, they rot, they become invisible. Every drift signal belongs in §3 here. |

---

## Appendix A: Why the suite doesn't read upstream env vars dynamically

A natural alternative to this doc: have the suite read every
production env var (MESSAGE_BUCKET_HOURS, MONGO_DB, CASSANDRA_KEYSPACE,
SITE_ID, …) at startup and use those values everywhere. This was
explicitly rejected in the project ruling (2026-06):

- **Env-var sprawl.** Each new mirrored value adds a new env var to
  thread through the runner config, the sandbox, the pollers, and the
  scenario substitution layer. The cost-per-value is non-trivial.
- **Too-specific-detail.** Values like the bucket window and the
  history floor are deep implementation details that the test
  scenarios already inherit from the production deployment via the
  shared Docker stack. Adding suite-side env vars duplicates that
  surface.
- **The blast radius is small.** The current count of mirrored values
  is bounded — under 50 across the entire suite — and most surfaces
  (subjects, streams, containers) change rarely. A human-maintained
  register is cheaper than the abstraction it would replace.

The escape hatch: if a SPECIFIC value becomes a frequent-drift point
(more than one Finding per quarter), bump it from the register to a
suite env var on a case-by-case basis. `MESSAGE_BUCKET_HOURS` is the
most plausible candidate today; the ruling above is to defer until
the second drift incident.

## Appendix B: This doc is a deliverable, not a contract

The register catalogs CURRENT mirrors. It does NOT promise that every
hardcode will be enumerated forever, nor that every drift will be
caught before it surfaces in a live run. It DOES promise: when a
maintainer finds a drift, the register is updated before the next
PR lands. That discipline is the entire artifact.
