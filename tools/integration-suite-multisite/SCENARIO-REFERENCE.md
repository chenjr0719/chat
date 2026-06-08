# Test scenario YAML — reference (multi-site)

How a multi-site scenario YAML is shaped, what each field means, what
tokens you can use inside it, and what the loader accepts or rejects.

This is a reference, not a workflow guide. For the workflow see
[AUTHORING.md](AUTHORING.md). For the infrastructure that scenarios
reference (verbs, readers, matchers, seed-effects), see the YAMLs
under `catalogs/` — each file is heavily commented.

**Authoritative schema:** `internal/scenario/types.go`. When this doc
disagrees with that file, the Go type wins — that is a doc bug.

The multi-site shape is **fundamentally different** from the single-site
shape. The key differences are:

- No `cases:` array.
- No `base_input:`.
- No top-level `seed:` — seed is nested under `sites.<site>.seed`.
- `tag:` is at scenario level, not per-case.
- `site:` is required on `input` and on every site-scoped
  `expected[i]`.
- `site:` is forbidden on `reply` and `cassandra_select` entries.

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

## 2. Top-level scenario shape

Every field the loader recognizes:

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `scenario` | string | yes | — | Human-readable name; appears in reports and perf keys. |
| `source` | string | yes | — | Design doc or file:line citation. Authors do not invent expectations. |
| `status` | string | no | `draft` | `draft` or `approved`. `approved` gates CI via the approved-only report. |
| `tag` | string | yes | — | `positive` or `negative`. Drives the confusion matrix. |
| `sites` | map | yes | — | Map of `site-a`/`site-b` → site spec. At least one site required. |
| `cassandra_data` | list | no | — | Top-level Cassandra seed rows. Cassandra is shared; do not put this under `sites`. |
| `pre_fire_scripts` | list of strings | no | — | Author-owned scripts that run after `Sandbox.Setup` and before `Dispatcher.Fire`. See §4.5. |
| `input` | object | yes | — | The single verb fire. |
| `expected` | list | yes | — | Non-empty list of assertions. |

A minimal valid scenario:

```yaml
scenario: Room Creates on Site A
source: room-service/handler.go:300
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Lobby
    users: ["${alice.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match:
      body_json:
        status: accepted
```

---

## 3. `sites` map shape

```yaml
sites:
  site-a:            # must be "site-a" or "site-b" — no other values
    seed:
      users: ...
      rooms: ...         # optional
      memberships: ...   # optional
  site-b:
    seed:
      users: ...
```

Exactly two site keys are supported: `site-a` and `site-b`. Any other
key is a loader error. A site may be omitted entirely if no seed is
needed for it (e.g. a single-site baseline).

### 3.1 Seed — users

```yaml
seed:
  users:
    <alias>:
      <effect-flag>: <bool>
```

- `<alias>` is a scenario-local handle. Substitution tokens
  `${<alias>.account}`, `${<alias>.id}`, `${<alias>.jwt}`,
  `${<alias>.nkey}`, `${<alias>.credential}` resolve to the
  materialized user's fields.
- `<effect-flag>` is a key from `catalogs/seed-effects/` (closed
  catalog). Unknown flags fail validation before any I/O.
- Effect flags with value `false` are explicit no-ops.
- The user's ID is derived: `id = "u-" + account`.
- Users are registered against the specified site's auth-service; their
  credentials are only valid on that site.

**Seed-effects catalog (today):**

| Flag | Effect | When |
|------|--------|------|
| `verified: true` | Mint nkey + JWT via auth-service /auth | Any actor that needs to make a NATS request |

`verified: false` (default; also explicit) leaves the user with empty
JWT/NkeySeed — for negative scenarios that assert NATS auth rejection.

### 3.2 Seed — rooms

```yaml
seed:
  rooms:
    - id: r-eng            # required; must be unique within scenario
      type: channel        # "channel" or "dm"
      name: Engineering    # required for channel; omit for dm
```

Rooms are inserted into the site's Mongo `rooms` collection before the
fire. Closed enum for `type`. DM rooms are limited to two members (see
`docs/spec-room-subscription-seed.md`).

### 3.3 Seed — memberships

```yaml
seed:
  memberships:
    alice:                  # user alias (must be declared in seed.users)
      - room: r-eng         # must reference a room in seed.rooms
        roles: [owner]      # list of "owner" / "admin" / "member"; defaults to [member]
    bob: [r-eng]            # shorthand: list of room IDs → all default to [member]
```

The grammar is `map[<user-alias>] → list of memberships`. Each
membership entry is either:
- An object `{room: <id>, roles: [<role>...]}` for explicit roles, or
- A bare room-ID string (no role list — defaults to `[member]`).

Both forms can coexist in one scenario (see
`scenarios/drafts/cross-site-room-rename-federation.yaml` for a
worked example using both shapes).

Memberships insert subscription rows into the site's Mongo
`subscriptions` collection.

---

## 4. `cassandra_data:` shape

```yaml
cassandra_data:
  - table: messages_by_room
    rows:
      - room_id: r-eng
        created_at: ${now - 2m}
        bucket: ${bucket(created_at)}
        message_id: m-1
        msg: "hello"
        site_id: site-a
```

This block is at scenario top level, not under any site. Cassandra is
a shared single-cluster deployment.

- `table` — CQL table name (must exist in the keyspace).
- `rows` — list of column→value maps. Column names must match the CQL
  schema exactly — for `messages_by_room` and `messages_by_id` the
  text body column is `msg` (NOT `body_text`); see
  `docs/cassandra_message_model.md` and
  `pkg/model/cassandra/message.go` for the full schema.
- `${now ± d}` — relative timestamp. Supported units: `ms`, `s`, `m`,
  `h`. Resolves to Unix milliseconds relative to `Sandbox.StartTime`.
- `${bucket(<col>)}` — auto-computes the message-bucket partition key
  from the resolved value of the named column (`created_at` in the
  example). Uses the same `MESSAGE_BUCKET_HOURS` window the services
  use.

---

## 4.5 `pre_fire_scripts` — author-owned setup hooks

Some scenarios need pre-conditions that the seed grammar cannot
express — typically operationally-scaffolded state that lives outside
any service's bootstrap responsibility (per `ARCHITECTURE.md` §0,
"the tool's limits"). The `pre_fire_scripts` field lets the author
declare a list of executable scripts that the harness runs **after
`Sandbox.Setup` completes and before `Dispatcher.Fire`**.

```yaml
pre_fire_scripts:
  - prep-outbox-streams.sh
  - prep-vault-secret.sh
```

The grammar is intentionally minimal — a list of relative paths
(strings only; no `args:`, no nested config). Each script is the
author's bespoke tool for one job; if it needs values, it reads
env vars or just hard-codes.

| Property | Rule |
|----------|------|
| Path | Relative to the directory of the scenario YAML file. |
| Working directory | The directory of the scenario YAML file. |
| Execution order | List order. Single-threaded; the next script does not start until the previous one exits 0. |
| Exit code | Non-zero fails the scenario; remaining scripts do not run. |
| Failure reason | `pre_fire_scripts[i] "name.sh": exit status N; output: <stderr/stdout snippet, truncated at 4 KB>`. Surfaces in `last-run.md`. |
| Permissions | Script must be executable (`chmod +x`). The harness exec's it directly — shebang line picks the interpreter. |

### Environment variables exposed to the script

```
   ISM_SITE_A_NATS_URL      host-mapped NATS URL for site-a
   ISM_SITE_B_NATS_URL      host-mapped NATS URL for site-b
   ISM_NATS_CREDS_FILE      absolute path to backend.creds
   ISM_SITE_A_MONGO_URI     host-mapped Mongo URI for site-a
   ISM_SITE_B_MONGO_URI     host-mapped Mongo URI for site-b
   ISM_CASSANDRA_HOSTS      shared Cassandra host:port (single)
   ISM_RUN_ID               sandbox run identifier
   ISM_SCENARIO_NAME        the `scenario:` field of this YAML
```

The script also inherits the caller's `PATH`, `HOME`, and other
environment so CLI tools (`nats`, `mongosh`, `cqlsh`, etc.) the
operator already installed are discoverable.

### Worked example — pre-creating `OUTBOX_<site>`

`OUTBOX_<site>` is owned by ops/IaC in production and is not
bootstrapped by any chat service. If a scenario fires production code
that publishes to `outbox.<site>.>`, the publish returns `nats: no
response from stream`. That's a real finding; the harness will not
auto-fill the gap (per the limits doc). A scenario that wants to
test downstream effects past the gap can declare the prep as a
script the operator's own infrastructure runs.

```yaml
pre_fire_scripts:
  - prep-outbox-streams.sh
```

```sh
# prep-outbox-streams.sh
#!/usr/bin/env bash
set -euo pipefail
for site in site-a site-b; do
  url_var="ISM_$(echo "$site" | tr 'a-z-' 'A-Z_')_NATS_URL"
  nats stream add "OUTBOX_$site" \
       --subjects "outbox.$site.>" \
       --server "${!url_var}" \
       --creds "$ISM_NATS_CREDS_FILE" \
       --defaults --no-progress
done
```

The script is the author's responsibility — its content, its
runtime dependencies, its cleanup, its idempotency. The harness's
role is purely to execute it and surface the result.

### What `pre_fire_scripts` is NOT

- Not a fallback for missing scenario state. Anything the seed grammar
  CAN express (users, rooms, memberships, remote_users, cassandra_data)
  belongs in the seed block — author-visible, declarative, no
  external dependency.
- Not a place to invoke production code on the author's behalf — if
  the scenario wants the app to do something, that's `input:`, not
  `pre_fire_scripts:`.
- Not a workaround for harness bugs — if a scenario shouldn't need
  a script, fix the harness or the scenario, not patch around it.

---

## 5. `input` shape

```yaml
input:
  site: site-a        # required; "site-a" or "site-b"
  verb: nats_request  # required; from catalogs/verbs/
  subject: <template> # required; substitution tokens resolved
  payload: <map>      # required for nats_request; substitution resolved
  credential: <ref>   # required; ${<alias>.credential}
```

| Field | Type | Required | Notes |
|-------|------|----------|-------|
| `site` | string | yes | `site-a` or `site-b`. Routes the fire to that site's NATS connection. |
| `verb` | string | yes | Must be in `catalogs/verbs/`. Today: `nats_request`, `jetstream_publish`. |
| `subject` | string | yes | NATS subject template. Tokens resolved before dispatch. |
| `payload` | map | yes | JSON payload. Tokens resolved. |
| `credential` | string | yes | `${<alias>.credential}`. The alias must be declared in `sites.<input.site>.seed.users`. |

---

## 6. `expected[]` shape

Each element in the `expected` list is one assertion.

| Field | Type | Required | Default | Notes |
|-------|------|----------|---------|-------|
| `location` | string | yes | — | One of the six registered poller locations (see §7). |
| `site` | string | see §7 | — | Required or forbidden depending on `location`. |
| `args` | map | location-specific | — | Per-primitive args (see §7). |
| `match` | map | yes | — | Subset shape against the event payload. |
| `timeout` | duration | no | `5s` | Gomega `Eventually` timeout. Go duration strings. |
| `polling` | duration | no | `100ms` | How often the poller is re-invoked. |
| `not` | bool | no | `false` | `true` uses `Consistently(…).ShouldNot(…)`. |

The runner short-circuits on the first failing assertion.

---

## 7. Universal primitives — `location` + `site` rules

### 7.1 Site presence/absence

| `location` | `site:` |
|------------|---------|
| `reply` | **forbidden** — intrinsic to `input.site` |
| `cassandra_select` | **forbidden** — shared cluster |
| `mongo_find` | **required** |
| `jetstream_consume` | **required** |
| `nats_subscribe` | **required** |
| `logs_tail` | **required** |

Violating either rule is a loader error caught before any container
is booted.

### 7.2 `reply`

```yaml
- location: reply
  match:
    body_json:
      status: accepted
```

The synchronous reply payload from the `nats_request` verb. Injected
by the dispatcher into the `ReplyReader` buffer. `site:` is forbidden.
`args:` is not used.

**Pitfall: `reply` only fires for `nats_request`.** If your scenario's
`input.verb` is `jetstream_publish` (e.g. the message-send pipeline,
which publishes to `MESSAGES_<site>` and gets an async response on
`chat.user.<acct>.response.<requestId>`), the ReplyReader buffer
stays empty and `reply` assertions silently never match. For
async-response paths, use a `nats_subscribe` Warmer on the response
subject instead — see
`scenarios/drafts/message-pipeline-send-and-persist.yaml` Surface 1
for the worked pattern (hardcode the `requestId` in the payload so
the subscribe filter is known pre-fire).

### 7.3 `mongo_find`

```yaml
- location: mongo_find
  site: site-a
  args:
    collection: rooms
    filter:
      name: EngineeringFederated
  match:
    name: EngineeringFederated
    createdBy: ${alice.id}
  timeout: 5s
  polling: 100ms
```

Queries the named collection on the specified site's Mongo. The
`createdAt >= startTime` filter is auto-merged so assertions only
observe events that occurred after `Sandbox.StartTime`.

| Arg | Required | Notes |
|-----|----------|-------|
| `collection` | yes | Mongo collection name. |
| `filter` | yes | MongoDB query document. Tokens resolved. |

### 7.4 `cassandra_select`

```yaml
- location: cassandra_select
  args:
    query: "SELECT JSON * FROM messages_by_room WHERE room_id = ? AND bucket = ?"
    params: ["r-eng", 0]
  match:
    msg: "hello"
    room_id: "r-eng"
```

Queries the shared Cassandra cluster. `site:` is forbidden.

The poller scans a SINGLE JSON column per row (`SELECT JSON ...`).
Plain `SELECT *` returns multi-column rows that the scanner can't
unpack — always use `SELECT JSON *` (or another JSON-yielding shape).
Match keys are CQL column names in snake_case (`msg`, `room_id`,
`message_id`), NOT the camelCase JSON tags from the Go Message
struct.

| Arg | Required | Notes |
|-----|----------|-------|
| `query` | yes | CQL query string. `?` placeholders bound in order. |
| `params` | no | List of bind values. `StartTime` is bound to the first `?` when `params` is absent. |

### 7.5 `jetstream_consume`

```yaml
- location: jetstream_consume
  site: site-b
  args:
    stream: INBOX_site-b
    filter_subject: outbox.site-a.to.site-b.room.created
  match:
    roomId: ${input.payload.name}
  timeout: 10s
```

Replays messages from the JetStream stream via a `DeliverByStartTime`
ephemeral consumer. Uses `WithDomain(site)` to target the correct JS
domain.

| Arg | Required | Notes |
|-----|----------|-------|
| `stream` | yes | JetStream stream name. |
| `filter_subject` | yes | Subject filter for the ephemeral consumer. |

### 7.6 `nats_subscribe`

```yaml
- location: nats_subscribe
  site: site-a
  args:
    subject: chat.room.r-eng.>
  match:
    eventType: room.created
```

Subscribes to a Core NATS subject. This poller implements `Warmer` —
the subscription opens before the verb fires. The subscription has no
replay; if the fire races ahead of `Warm`, events may be missed.

| Arg | Required | Notes |
|-----|----------|-------|
| `subject` | yes | NATS subject with optional `*`/`>` wildcards. |

### 7.7 `logs_tail`

```yaml
- location: logs_tail
  site: site-a
  args:
    container: room-service
    service: room-service
  match:
    msg: "room created"
    roomId: r-eng
```

Tails container stdout/stderr. The runner resolves the container name
as `<container>-<site>` (e.g. `room-service-site-a`).

| Arg | Required | Notes |
|-----|----------|-------|
| `container` | yes | Base container name (without site suffix). |
| `service` | no | Alias for log-line filtering; defaults to `container`. |

---

## 8. Substitution token grammar

Tokens are resolved in subject, payload, credential, match, and args
fields. Resolution occurs before the fire for `input` fields, and per
assertion for `expected[i]` fields.

```
token ::= "${" expr "}" | "$auto"

expr  ::= alias-field
        | "now"
        | "now" ws op ws duration
        | "bucket(" col ")"
        | "input." input-field

alias-field  ::= alias "." field
alias        ::= [a-zA-Z_][a-zA-Z0-9_]*    (declared in sites.*.seed.users)
field        ::= "account" | "id" | "jwt" | "nkey" | "credential"

op           ::= "+" | "-"
duration     ::= [0-9]+ unit
unit         ::= "ms" | "s" | "m" | "h"
col          ::= [a-zA-Z_][a-zA-Z0-9_]*    (column name in cassandra_data row)

input-field  ::= "subject" | "payload." key | "requestId"
key          ::= [a-zA-Z_][a-zA-Z0-9_.]*
```

`$auto` resolves to a runtime-generated unique string of the form
`it-<runID>-room-auto-<N>`. Useful for room names and IDs that must
not collide across parallel or repeated runs.

---

## 9. Loader errors

The loader rejects scenarios before any container is booted.

| Error | Cause |
|-------|-------|
| `scenario: required` | `scenario:` field missing or empty. |
| `source: required` | `source:` field missing or empty. |
| `tag: must be "positive" or "negative"` | `tag:` is absent or has another value. |
| `sites: required` | `sites:` map absent or empty. |
| `sites key must be "site-a" or "site-b"` | An unknown site key was used. |
| `input: required` | `input:` block missing. |
| `input.site: required` | `site:` field absent from `input`. |
| `input.site: must be "site-a" or "site-b"` | Site value is not one of the two. |
| `input.verb: unknown verb "<v>"` | Verb not in `catalogs/verbs/`. |
| `expected: must have at least one entry` | `expected:` list is empty or absent. |
| `expected[N].location: unknown "<loc>"` | Location not in the registered poller set. |
| `expected[N]: site: forbidden for location "reply"` | `site:` present on a `reply` entry. |
| `expected[N]: site: forbidden for location "cassandra_select"` | `site:` present on a `cassandra_select` entry. |
| `expected[N]: site: required for location "<loc>"` | `site:` absent on a site-scoped entry. |
| `expected[N]: site: must be "site-a" or "site-b"` | Site value is not one of the two. |
| `forbidden token "${site}" in <field>` | The ambiguous `${site}` token was used. Write `site-a` or `site-b` literally. |
| `forbidden token "${siteA}" ...` | Same family of forbidden tokens. |
| `forbidden token "${service.*}" ...` | Service credentials are not exposed. |
| `seed.users.<alias>: unknown flag "<flag>"` | A flag not in `catalogs/seed-effects/` was set to `true`. |
| `cassandra_data: must not appear under sites` | `cassandra_data` was placed inside a site block; move it to top level. |

---

## 10. Validator errors (site-presence)

The static validator (`make validate`) also checks:

| Error | Cause |
|-------|-------|
| `expected[N]: site must be absent for "reply"` | `site:` present when forbidden. |
| `expected[N]: site must be absent for "cassandra_select"` | Same. |
| `expected[N]: site required for "<loc>"` | `site:` absent when required. |
| `expected[N]: site "<s>" unknown` | Not `site-a` or `site-b`. |
| `input.site "<s>" not declared in sites:` | Input fires at a site with no seed block. Add the site to `sites:` even if its seed is empty. |

---

## 11. Matcher semantics — `matches_shape`

The default (and only) matcher for `expected[].match` is
`matches_shape`:

- **Subset deep match.** Extra fields in the observed payload are
  ignored; only fields the `match` block mentions must match.
- **Nested maps recurse** with the same subset semantics:
  `match: {body_json: {status: accepted}}` succeeds against
  `{body_json: {status: accepted, roomId: r-x, ...}}`.
- **Type normalisation.** JSON numbers decoded as float64 are
  normalized for integer comparison.
- **Struct payloads** (e.g. `ReplyPayload`) marshal to JSON and
  unmarshal as a generic map.
- **ROSM (Relative Order Subset Match).** When the expected match
  value is a list, the matcher walks the observed slice looking for
  each expected element in declaration order.

For "must NOT happen" assertions, set `not: true` — the loop uses
`Consistently(...).ShouldNot(...)`.

---

## 12. Common mistakes

| Symptom | Cause | Fix |
|---------|-------|-----|
| `forbidden token "${site}"` | Used `${site}` in subject or payload. | Replace with the literal string `site-a` or `site-b`. |
| `expected[N]: site: required for location "mongo_find"` | `site:` missing on a `mongo_find` entry. | Add `site: site-a` or `site: site-b`. |
| `expected[N]: site: forbidden for location "reply"` | `site:` present on a `reply` entry. | Remove `site:` from that entry. |
| Assertion times out at `mongo_find` (cross-site) | Federation pipeline hasn't delivered the event within the default 5s window. | Extend `timeout:` to `10s` or longer for cross-site assertions. |
| Assertion times out at `mongo_find` (single-site) | `createdAt >= startTime` auto-filter excludes rows written before `Sandbox.StartTime`. | Confirm the scenario writes the row after setup; check filter. |
| Assertion times out at `nats_subscribe` | Subscription opened after the verb fired (race). | Ensure `nats_subscribe` entries are listed before `reply` — the runner processes Warmers in declaration order. |
| `seed.users.<alias>: unknown flag "X"` | Effect flag not in `catalogs/seed-effects/`. | Use `verified: true`; check catalog for new flags. |
| Federation cross-site assertion times out consistently | Production code may not federate on room-metadata events (only on message-send). | See "Open Concerns" in `README.md`; this is a known hypothesis the smoke run tests. |
