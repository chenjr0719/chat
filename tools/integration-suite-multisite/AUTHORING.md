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

## Architecture

`tools/integration-suite-multisite/ARCHITECTURE.md` explains the
26-container stack, NATS supercluster, federation Sources, Sandbox
lifecycle, and the verb/reader primitive catalog. Read it once before
authoring your first scenario.
