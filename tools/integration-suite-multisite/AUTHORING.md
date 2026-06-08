# Authoring scenarios — multi-site

This is the hand-edit guide for `tools/integration-suite-multisite/`
scenarios. The multi-site scenario shape is fundamentally different
from the single-site shape — read this doc before editing any YAML.
For the YAML grammar field-by-field reference see
[SCENARIO-REFERENCE.md](SCENARIO-REFERENCE.md).

---

## Five hard rules

1. **Expected behavior comes from a cited design doc — not from
   training data.** Every scenario has a `source:` line.

2. **If the design is silent, STOP.** Do not invent expectations.

3. **Catalog vocabulary is closed.** Verbs, readers, and seed-effect
   flags must already exist in `catalogs/`. If a needed primitive is
   absent, surface the gap rather than working around it.

4. **One scenario = one fire + one expected list.** There is no
   `cases:` array and no `base_input:`. Each scenario fires exactly
   once and asserts one set of outcomes. If you need two independent
   experiments, write two scenario files.

5. **Scenarios always land in `drafts/`.** Promotion to `approved/`
   is a separate, human-reviewed PR.

---

## Mental model

A multi-site scenario is one hypothesis about the assembled two-site
system. You declare the seed state you need on each site, fire one
verb from one site, and assert what you expect to observe — possibly
on both sites.

```
scenario
  sites:              per-site seed
    site-a / site-b
  input:              one fire (site required)
  expected:           one assertion list
    []expected[i]     each with site: where required
```

The Sandbox materializes users on each site by calling the per-site
auth-service. It drops and re-creates collections (per site), then
seeds rooms and Cassandra rows before firing.

---

## Required fields in order

```
scenario:   <name>
source:     <doc-or-file-citation>
tag:        positive | negative
sites:      <map>   (at least one site)
input:      <fire>
expected:   <list>
```

`status:` is optional (default `draft`). Include it only when promoting
to `approved` via a reviewed PR.

---

## Per-site seed

Declare each actor under the site where they are registered:

```yaml
sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
      rooms:
        - id: r-eng
          type: channel
          name: Engineering
      memberships:
        - room: r-eng
          user: alice
          role: owner
  site-b:
    seed:
      users:
        bob: { verified: true }
```

- Users on site-a can only be used in `input` and `expected` entries
  whose `site:` is `site-a`. The `${alice.account}` token is global
  across the scenario, but alice's JWT was minted against site-a's
  auth-service.
- Rooms and memberships follow the same closed enums as single-site
  (channel/dm for type; owner/member for role).
- If a site needs no seed, omit it from `sites:` entirely.

---

## `cassandra_data:` at scenario top level

Cassandra is a shared cluster. Seed rows go at the top level (not
inside any `sites.<site>.seed`):

```yaml
cassandra_data:
  - table: messages_by_room
    rows:
      - room_id: r-eng
        created_at: ${now - 2m}
        bucket: ${bucket(created_at)}
        message_id: m-1
        body_text: "hello"
```

`${now - 2m}` resolves to a Unix millisecond timestamp two minutes
before `Sandbox.StartTime`. `${bucket(created_at)}` auto-computes the
message-bucket partition key from the resolved `created_at` column.

---

## Authoring discipline — assert at every observable layer

A scenario fires one verb and observes its effects. When the verb
exercises a multi-step pipeline (request → handler → stream → worker
→ database → cross-site), the natural temptation is to assert only
the final observable — the database row, the federated event — and
treat everything in between as "if the end is right, the middle was
right." That leaves the suite blind in two important ways:

1. **Failure localisation.** When the final observable doesn't
   arrive, "which layer was broken?" requires reading container logs.
   The suite should pinpoint it.

2. **Silent correctness drift.** If a refactor changes an
   intermediate event shape or skips it entirely, the final
   observable may still arrive (via some other path) and the
   scenario stays green while a real regression sits underneath.

**Discipline:** for each fire, enumerate every layer the pipeline
can be observed at, and assert at each. The cost is a few extra
`expected[]` blocks; the benefit is that failures name the broken
layer themselves.

Observable layers, in typical order:

| Layer | Reader primitive | What it proves |
|-------|------------------|----------------|
| Synchronous reply | `reply` | The handler accepted the request and replied. |
| Local Mongo / Cassandra | `mongo_find` / `cassandra_select` | The state mutation persisted. |
| Local JetStream canonical | `jetstream_consume` | The originating service published the event. |
| Local OUTBOX (cross-site cases) | `jetstream_consume` | The worker emitted the federation event locally — before the gateway gets involved. |
| Peer-site INBOX | `jetstream_consume` (with `site:` set) | The federation Source delivered. |
| Peer-site persistence | `mongo_find` / `cassandra_select` | The peer's worker consumed and applied. |
| Service logs | `logs_tail` | Last-resort diagnostic when events skip layers. |

If a layer is **skipped** from `expected[]`, the suite either trusts
it implicitly (fine for layers that are uninteresting to the
scenario's theme) or the author has chosen to be ambiguous about
where a failure originated (avoid).

For cross-site federation tests in particular, the OUTBOX-on-the-
firing-site assertion is the single most diagnostic block: it
distinguishes "the local worker published" from "the federation
Source didn't deliver." Always include it.

---

## Infra-sanity scenarios — the harness's first line of defence

Scenarios prefixed `infra-sanity-` are reserved for tests that
verify the harness itself + the multi-site stack are healthy enough
for any downstream finding to be meaningful. Conventions:

- File name: `infra-sanity-<what>-site-<a|b>.yaml`
- `status: approved` (gates CI). A failing infra-sanity scenario
  means downstream failures are not informative; fix the infra
  first.
- The fire is a minimal, well-known production code path (room
  create, message send, auth mint) — nothing exotic.
- The `expected[]` list covers every observable layer for the chosen
  fire, so a sanity failure already localises the broken layer.

Currently shipped:

| File | Proves |
|------|--------|
| `infra-sanity-rooms-pipeline-site-a.yaml` | site-a NATS req/reply, MESSAGES + ROOMS streams, per-site Mongo, room-service + room-worker chain. |
| `infra-sanity-rooms-pipeline-site-b.yaml` | Same shape on site-b — proves the symmetric site is functional. |

NATS supercluster gateway delivery is implicitly tested by the
`cross-site-room-rename-federation.yaml` scenario; we do not ship a
gateway-isolated sanity test because user-account credentials don't
have permission to publish on backend subjects, and the federation
scenario surfaces gateway-or-Source failures clearly enough.

---

## `pre_fire_scripts:` — escape hatch for state the seed grammar can't express

Some pre-conditions are operationally-scaffolded (a JetStream stream
ops/IaC normally creates, a Vault secret a sidecar normally provides).
The seed grammar declares scenario state, not infra state — see
`ARCHITECTURE.md` §0 for why.

If your scenario needs that kind of prep, declare it as a script:

```yaml
pre_fire_scripts:
  - prep-outbox.sh
```

The script sits next to the YAML file. The harness runs it after
`Sandbox.Setup` and before the fire, passing the live stack's
host-mapped URLs + creds path as `ISM_*` env vars. Non-zero exit
fails the scenario with the script's output captured into the
report. Full reference: `SCENARIO-REFERENCE.md` §4.5.

This is an escape hatch, not a default. Anything the seed grammar
can express belongs in `seed:` — declarative, author-visible, no
external dependency. Reach for `pre_fire_scripts` only when there's
genuinely no seed-grammar way to set up the precondition.

---

## Substitution token vocabulary

Available in `subject`, `payload`, `credential`, `match`, and `args`
fields.

| Token | Resolves to |
|-------|-------------|
| `${<alias>.account}` | seed user's account (== alias) |
| `${<alias>.id}` | `u-` + account |
| `${<alias>.jwt}` | minted NATS JWT |
| `${<alias>.nkey}` | nkey seed |
| `${<alias>.credential}` | user-level credential shorthand |
| `${now}` | `time.Now().UTC().UnixMilli()` |
| `${now - 2m}` | relative offset (Cassandra seed rows) |
| `${now + 1h}` | relative offset (positive direction) |
| `${bucket(<col>)}` | auto-computed message-bucket value |
| `$auto` | runtime-unique random string |

---

## Forbidden tokens

The loader rejects these with an explicit error:

| Token | Why forbidden |
|-------|---------------|
| `${site}` | Ambiguous in a two-site scenario. Write the literal `site-a` or `site-b`. |
| `${siteA}`, `${siteB}` | Same — not supported in multi-site loader. |
| `${<alias>.site}` | Not a recognized field on seed users. |
| `${service.*.credential}` | Service credentials are not exposed in the multi-site runner. |

---

## Site-routing rules

`site:` controls which site's connections the runner uses for the fire
and for each assertion.

| Field | `site:` required? |
|-------|-------------------|
| `input` | Yes — must be `site-a` or `site-b` |
| `expected[i]` where location is `reply` | Forbidden — intrinsic to the fire site |
| `expected[i]` where location is `cassandra_select` | Forbidden — shared cluster |
| `expected[i]` where location is `mongo_find` | Required |
| `expected[i]` where location is `jetstream_consume` | Required |
| `expected[i]` where location is `nats_subscribe` | Required |
| `expected[i]` where location is `logs_tail` | Required |

Violations are caught by the loader before any container is booted.

---

## Worked example 1 — single-site happy path on multi-site infra

File: `scenarios/drafts/room-creates-federates-to-site-b.yaml`

```yaml
scenario: room-creates-federates-to-site-b
source: spec docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md §8
status: draft
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
    name: Engineering
    users: ["${alice.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match:
      body_json:
        status: accepted
  - location: mongo_find
    site: site-a
    args:
      collection: rooms
      filter:
        name: Engineering
    match:
      name: Engineering
      createdBy: ${alice.id}
```

What this tests: room creation succeeds on site-a and lands in site-a's
Mongo. No cross-site assertion. This is the baseline — if this fails,
something is wrong with the single-site stack, not with federation.

---

## Worked example 2 — federation tail

File: `scenarios/drafts/room-create-federates-cross-site.yaml`

```yaml
scenario: room-create-federates-cross-site
source: spec docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md §8
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
  site-b:
    seed:
      users:
        bob: { verified: true }

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: EngineeringFederated
    users: ["${alice.account}", "${bob.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match:
      body_json:
        status: accepted
  - location: mongo_find
    site: site-a
    args:
      collection: rooms
      filter:
        name: EngineeringFederated
    match:
      name: EngineeringFederated
  - location: mongo_find
    site: site-b
    args:
      collection: rooms
      filter:
        name: EngineeringFederated
    match:
      name: EngineeringFederated
    timeout: 10s
```

What this tests: a room created on site-a with a site-b member
federates to site-b's Mongo within 10 seconds. The extended timeout
accommodates OUTBOX → INBOX propagation latency. If this assertion
times out, see the "Federation may not fire on room create" open
concern in `README.md`.

---

## Tips

- **Scenarios are drafts by default.** Do not set `status: approved`
  unless the scenario is going through a reviewed PR for CI promotion.
- **One scenario = one assertion theme.** Do not bundle a happy path
  and a negative case in the same scenario — put them in separate files.
  The multi-site shape has no case loop; bundling would require awkward
  setup or unsafe state sharing between scenarios.
- **Use `$auto` for room names** that must not collide across parallel
  or repeated runs.
- **`timeout: 10s`** (or longer) is appropriate for cross-site
  assertions because federation adds OUTBOX → INBOX propagation time
  on top of normal async processing.
- **Run validation before booting infra:**
  `make -C tools/integration-suite-multisite validate`

---

## Common pitfalls — async-reply assertions

The message-send pipeline (and any other path that uses
`jetstream_publish` + an async response on a reply subject) has a
shape that doesn't map onto the obvious `reply` location. Two
specific traps:

1. **`reply` location only fires for `nats_request`.** If your
   `input.verb` is `jetstream_publish`, the ReplyReader buffer stays
   empty. Use `nats_subscribe` on the service's response subject
   (e.g. `chat.user.${alice.account}.response.<requestId>`) instead.

2. **`nats_subscribe` is a Warmer with no replay.** The subscription
   opens before the verb fires, but Core NATS subjects aren't
   buffered — if the publish raced ahead of the subscribe, the
   response is lost forever. Two rules to defang this:
   - Hardcode the `requestId` in the input payload (don't use `$auto`)
     so the subscribe subject is known at Warm time. Phase B's
     `${input.payload.requestId}` token resolves AFTER fire, too late
     for the Warmer.
   - Declare the `nats_subscribe` entry BEFORE any other entries in
     `expected[]`. The runner walks Warmers in declaration order
     before firing; the entry's position is what guarantees the open
     wins the race.

Worked example:
`scenarios/drafts/message-pipeline-send-and-persist.yaml` Surface 1.

---

## Architecture

`tools/integration-suite-multisite/ARCHITECTURE.md` explains the
26-container stack, NATS supercluster, federation Sources, Sandbox
lifecycle, and the verb/reader primitive catalog. Read it once before
authoring your first scenario.
