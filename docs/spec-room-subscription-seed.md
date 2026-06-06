# Spec: Room-Subscription Seed Effect

**Status:** Draft for review (no code yet)
**Phase:** 4.2 — Messaging Pipeline Prerequisites
**Author:** Implementation plan for review
**Spec date:** 2026-06

## 1. Motivation

The Phase 4.0 universal-primitive engine can already assert against
Cassandra (`cassandra_select`), the canonical message stream
(`jetstream_consume`), and the message-gatekeeper logs (`logs_tail`).
What's still missing is the **world state** every messaging test needs
to even reach those primitives: a sender's subscription to the target
room.

`message-gatekeeper` looks up `subscriptions.find({roomId, "u.account":
sender})` on every inbound message. With no document, the gatekeeper
rejects synchronously and the message never reaches
`MESSAGES_CANONICAL_<site>` — the test author then can't tell whether
the **send path is broken** or whether **they forgot to seed
membership**.

Phase 4.2 closes that gap with a first-class **room-subscription seed
primitive** that:

- Declares rooms + memberships in the same YAML block where users are
  declared.
- Produces fully-formed `rooms`, `subscriptions`, and `room_members`
  Mongo documents that match the production schema (so the gatekeeper
  + any future handler that reads these collections is satisfied).
- Validates declared rooms vs. declared memberships at Sandbox.Setup,
  failing loudly on typos before the case fires.

This is a **harness prerequisite** for the three drafted messaging
scenarios:

- `message-send-persists-canonical-and-cassandra.yaml` (Draft #1)
- `message-gatekeeper-rejects-unsubscribed-sender.yaml` (Draft #2 —
  uses the **absence** of seed memberships)
- `history-service-paginates-messages.yaml` (Draft #3)

## 2. YAML authoring surface

### 2.1 The two-block design

```yaml
seed:
  # NEW: top-level list of pre-seeded rooms. One entry per distinct
  # room the scenario needs to exist before any case fires. Rooms are
  # referenced by id from seed.users[].member_of.
  rooms:
    - id: r-engineering
      name: Engineering Chat
      type: channel              # optional; default: channel
    - id: r-design
      name: Design Sync
      type: channel
    - id: r-alice-bob-dm
      type: dm                   # name optional for DMs (display is computed at read)

  # EXISTING: per-user effect flags (verified) + NEW: member_of list.
  users:
    alice:
      verified: true
      member_of:
        - room: r-engineering
          roles: [owner]         # optional; default: [member]
        - r-design               # bare-string shorthand → default-role member
    bob:
      verified: true
      member_of:
        - r-engineering          # bob is a default-member of engineering
        - room: r-alice-bob-dm
          roles: [member]
```

**Design choices:**

| Choice | Picked | Rejected | Rationale |
|---|---|---|---|
| Rooms declared once OR duplicated per-user | **Once at top level** | Per-user inline | Eliminates the drift risk where `alice.rooms[0]` and `bob.rooms[0]` disagree about a shared room's name/type. |
| Membership where the user is OR standalone `room_members` block | **Per-user `member_of`** | Standalone block | Test authors think "alice is in these rooms"; matches the mental model. The Mongo `room_members` collection gets generated FROM `member_of`, not declared directly. |
| Bare-string shorthand for default roles | **Yes** | Always-object form | Most memberships are default-member; the shorthand keeps single-room cases readable. |
| Role list vs. single role | **List** | Single | Matches `pkg/model/subscription.go:33` (`Roles []Role`); future scenarios that need a user who is BOTH owner AND admin should compose cleanly. |

### 2.2 Validation rules (enforced at Sandbox.Setup Step 1)

| Rule | Failure mode |
|---|---|
| Every `seed.users.<alias>.member_of[].room` (or bare string) resolves to a declared `seed.rooms[].id` | `sandbox.Setup: member_of references undeclared room "r-foo" for alias "alice"` |
| `seed.rooms[].id` values are unique within the block | `sandbox.Setup: seed.rooms duplicate id "r-engineering"` |
| `seed.rooms[].type` (when set) is one of `channel \| dm \| botDM \| discussion` | `sandbox.Setup: seed.rooms[r-foo] type "channel-extra" must be one of channel|dm|botDM|discussion` |
| Roles (when set) are one of `owner \| admin \| member` | `sandbox.Setup: member_of[r-engineering] role "viewer" must be one of owner|admin|member` |
| For `type: dm`, exactly two distinct aliases reference the room | `sandbox.Setup: dm room "r-x" needs exactly 2 members, got 1` |

Validation happens **before** Mongo writes — same Step-1 pass that
today validates seed-effect flags. The Sandbox.Setup ordering becomes:

1. **Validate** user flags + rooms + member_of refs *(extended)*
2. Chaos.Reset
3. Drop `users` + `rooms` + `subscriptions` + `room_members` Mongo
   collections *(extended — `room_members` added)*
4. Materialize SeedUsers + apply effects + mint NATS creds
5. Insert minimal user profile docs (Phase 3.7)
6. **Insert seeded rooms + subscriptions + room_members** *(NEW Step 6)*
7. Build Placeholders
8. Build PollerReg

## 3. MongoDB document shapes

Every field below is grounded in the production Go model so the
gatekeeper's `subscriptions.find` and any other handler reading these
collections is satisfied without ad-hoc patching.

### 3.1 `rooms` collection (one doc per `seed.rooms[]` entry)

Source struct: `pkg/model/room.go:14-30` → `type Room`

```jsonc
{
  "_id":       "r-engineering",          // from seed.rooms[].id
  "name":      "Engineering Chat",       // from seed.rooms[].name
  "type":      "channel",                // from seed.rooms[].type; default channel
  "siteId":    "site-local",             // from sandbox SiteID
  "userCount": 2,                        // count of member_of refs whose
                                         //   declared user is a non-bot
                                         //   (Phase 4.2: every seeded user is non-bot)
  "appCount":  0,                        // (no app members in Phase 4.2)
  "uids":      ["u-alice", "u-bob"],     // sorted; derived from member refs
  "accounts":  ["alice", "bob"],         // paired with uids[i] (same order)
  "lastMsgId": "",                       // no message yet; empty string
  "createdAt": <T_open>,                 // sandbox StartTime (UTC)
  "updatedAt": <T_open>                  // same
}
```

**Omitted fields** (populated by handlers, not relevant for seeded
state): `lastMsgAt`, `lastMentionAllAt`, `minUserLastSeenAt`,
`restricted`. Mongo accepts the absence cleanly; `omitempty` on the
Go side means the gatekeeper reads them as zero values.

**Optional `restricted: true`** declarable later if we add a flag at
`seed.rooms[].restricted`. Phase 4.2 defers — no current scenario
needs it.

### 3.2 `subscriptions` collection (one doc per `member_of[]` ref)

Source struct: `pkg/model/subscription.go:28-44` → `type Subscription`

Unique-key constraint (`room-service/store_mongo.go:60-65`):
`(roomId, u.account)` — naturally enforced by one-doc-per-membership.

```jsonc
{
  "_id":          "sub-<alias>-<roomId>",   // deterministic; e.g. "sub-alice-r-engineering"
  "u": {
    "_id":     "u-alice",                   // matches SeedUser.ID
    "account": "alice",                     // matches SeedUser.Account
    "isBot":   false                        // every seeded user is non-bot in Phase 4.2
  },
  "roomId":       "r-engineering",          // matches seed.rooms[].id
  "siteId":       "site-local",             // sandbox SiteID
  "roles":        ["owner"],                // from member_of[].roles; default [member]
  "name":         "Engineering Chat",       // copied from the room's name
                                            //   (production handlers do the same)
  "roomType":     "channel",                // copied from the room's type
  "isSubscribed": true,                     // every seeded membership is active
  "joinedAt":     <T_open>,                 // sandbox StartTime (UTC)
  "hasMention":   false,                    // start without mentions
  "alert":        false,                    // start without unread alerts
  "muted":        false                     // start unmuted
}
```

**Omitted fields:** `historySharedSince`, `lastSeenAt`, `threadUnread`
(all optional / `omitempty`).

**Why we copy `name` + `roomType` into the subscription:** production
room-service does the same so the side-bar can render a subscription
without a join. Matching that shape means a future test that reads the
subscription via `mongo_find` doesn't have to know which fields are
denormalised — they're all there.

### 3.3 `room_members` collection (one doc per `member_of[]` ref)

Source struct: `pkg/model/member.go:42-47` → `type RoomMember`

Unique-key constraint (`room-service/store_mongo.go:53-58`):
`(rid, member.type, member.id)`.

```jsonc
{
  "_id":    "rm-<alias>-<roomId>",          // deterministic; e.g. "rm-alice-r-engineering"
  "rid":    "r-engineering",                // matches seed.rooms[].id
  "ts":     <T_open>,                       // sandbox StartTime (UTC)
  "member": {
    "id":      "u-alice",                   // matches SeedUser.ID
    "type":    "individual",                // every seeded membership is type=individual
                                            //   (org memberships deferred)
    "account": "alice"                      // optional in production; we always set it
                                            //   so future enrich=true reads have it
  }
}
```

### 3.4 Deterministic `_id` convention

| Collection | `_id` formula | Why deterministic |
|---|---|---|
| `subscriptions` | `"sub-" + alias + "-" + roomId` | Tests can assert on the `_id` directly via `mongo_find` without first looking up the doc. Mirrors the deleted seed/users.json convention. |
| `room_members` | `"rm-" + alias + "-" + roomId` | Same. |
| `rooms` | `seed.rooms[].id` (author-supplied) | Already explicit. |

## 4. Implementation path

### 4.1 Type extensions

#### `internal/scenario/types.go`

```go
// SeedBlock declares scenario-local seed data. Phase 4.2 adds rooms;
// future expansions (org memberships, app installs) live here.
type SeedBlock struct {
    Users map[string]SeedUserFlags `yaml:"users,omitempty"`
    Rooms []SeedRoom               `yaml:"rooms,omitempty"`   // NEW
}

// SeedUserFlags is now richer than map[string]bool. Today's `verified:
// true` flag stays; Phase 4.2 adds `member_of` for room memberships.
//
// Backward-compat: any flag value other than `member_of` falls through
// to the existing bool-flag → Effect dispatch.
type SeedUserFlags struct {
    Verified bool                   `yaml:"verified,omitempty"`
    MemberOf []SeedMembership       `yaml:"member_of,omitempty"`  // NEW
}

// SeedRoom is one pre-seeded room.
type SeedRoom struct {
    ID   string   `yaml:"id"`
    Name string   `yaml:"name,omitempty"`
    Type RoomType `yaml:"type,omitempty"`   // default RoomTypeChannel
}

// SeedMembership is one (user, room) pair with optional roles.
// Supports the bare-string YAML form ("r-engineering") for the common
// default-member case via a custom UnmarshalYAML.
type SeedMembership struct {
    Room  string   `yaml:"room"`
    Roles []string `yaml:"roles,omitempty"`  // default [member]
}

func (m *SeedMembership) UnmarshalYAML(node *yaml.Node) error { ... }
```

**The flat-vs-struct change to `SeedUserFlags` is the biggest source-
file edit.** Today it's `map[string]bool`. The Phase 4.2 shape needs
real fields. To preserve back-compat for any external loader, the
YAML still parses `verified: true` the same way; only the Go shape
changes.

#### `internal/seedeffect/registry.go`

`SeedUser` gains no new fields. The membership state is local to
Sandbox.Setup and doesn't need to ride on the per-user struct.

### 4.2 Sandbox.Setup changes

#### Step 1 (validation) — extended

`seedeffect.ValidateFlags` only checks **per-user effect flags** (today
just `verified`). Phase 4.2 adds a sibling validator
`scenario.ValidateSeedBlock(s.Seed)` that runs in the same Step-1 pass
and returns the same shape error. Five new checks:

1. Every `seed.rooms[].id` is unique within the block.
2. Every `seed.rooms[].type` (when set) is in the closed enum.
3. Every `seed.users.<alias>.member_of[].room` resolves to a declared
   `seed.rooms[].id`.
4. Every `seed.users.<alias>.member_of[].roles[]` entry is in the
   closed enum.
5. For each `seed.rooms[]` with `type: dm`, exactly two distinct
   aliases reference it.

The function returns `nil` (no rooms declared) cleanly so every
existing scenario keeps working unchanged.

#### Step 3 (drop collections) — extended

`sandboxOwnedCollections` becomes
`{"users", "rooms", "subscriptions", "room_members"}`. Adding
`room_members` is the only change.

#### Step 6 (insert seeded room state) — NEW

Inline in `sandbox.go` (mirrors Phase 3.7's `insertSeedUserProfiles`):

```go
// Step 6: insert seeded rooms + subscriptions + room_members.
// Phase 4.2 — feeds the message-gatekeeper's subscription lookup so
// downstream messaging tests can fire msg.send without the rejection
// path swallowing the request. Shape: pkg/model.Room / .Subscription
// / .RoomMember, written via bson.M (per the Phase 3.8 black-box
// principle).
if sb.Deps.Mongo != nil && len(sb.Scenario.Seed.Rooms) > 0 {
    if err := insertSeededRooms(ctx, sb); err != nil {
        return fmt.Errorf("sandbox.Setup: seed rooms: %w", err)
    }
}
```

`insertSeededRooms` orchestrates three `InsertMany` calls (rooms,
subscriptions, room_members), computing the per-doc fields from the
declared seed block + the materialized `sb.Users` map.

### 4.3 No registered Effect

The Phase 3.x `seedeffect` registry is **per-user attribute mutation**
(verified, banned, premium). Room membership is a **relation**
spanning multiple users + rooms; it doesn't fit the
`Effect.Apply(ctx, u, deps)` signature cleanly.

Phase 4.2 puts room-seeding in `sandbox.go` directly (as a Setup step),
not in `internal/seedeffect/`. This keeps the Effect contract
single-purpose and avoids the awkwardness of "VerifiedEffect runs once
per user, but RoomMemberEffect needs to see all users together."

### 4.4 Catalog

No new seed-effect YAML under `catalogs/seed-effects/`. `verified.yaml`
stays as-is. Room seeding is a built-in step, not a catalog entry.

### 4.5 Substitution tokens

**Phase 4.2 introduces NO new tokens.** Authors hardcode room IDs in
payloads:

```yaml
base_input:
  subject: chat.user.${alice.account}.room.r-engineering.${site}.msg.send
  payload:
    roomId: r-engineering            # ← author hardcoded
    sender: { account: ${alice.account}, id: ${alice.id} }
```

A future `${room.r-engineering.id}` token could land if a refactoring
pattern emerges — defer to Phase 4.3.

## 5. Integration test plan

### 5.1 Extend `sandbox_integration_test.go` (Docker-tagged)

The existing `TestSandboxSetup_DropsCollectionsAndAppliesEffects` is
the natural extension point. Add one new function:

```go
func TestSandboxSetup_SeedRoomsAndMemberships(t *testing.T) {
    ctx := context.Background()
    db := testutil.MongoDB(t, "phase4-sandbox-rooms-seed")
    authURL := fakeAuthForSandbox(t, "test-jwt-token")

    s := &scenario.ScenarioV3{
        Name: "rooms-seed-test",
        Seed: scenario.SeedBlock{
            Rooms: []scenario.SeedRoom{
                {ID: "r-engineering", Name: "Engineering", Type: "channel"},
                {ID: "r-design",      Name: "Design",      Type: "channel"},
            },
            Users: map[string]scenario.SeedUserFlags{
                "alice": {
                    Verified: true,
                    MemberOf: []scenario.SeedMembership{
                        {Room: "r-engineering", Roles: []string{"owner"}},
                        {Room: "r-design"},      // default-member
                    },
                },
                "bob": {
                    Verified: true,
                    MemberOf: []scenario.SeedMembership{
                        {Room: "r-engineering"}, // default-member
                    },
                },
            },
        },
    }
    // ... NewSandbox + Setup + assertions:

    // 2 rooms in `rooms`
    // 3 subscriptions: (alice,r-engineering,[owner]), (alice,r-design,[member]),
    //                  (bob,r-engineering,[member])
    // 3 room_members: same shape
    // userCount on r-engineering == 2; on r-design == 1
    // uids + accounts sorted + paired correctly
    // subscription.name + roomType copied from the room
}
```

### 5.2 Unit tests in `internal/scenario/` for validation

Five Red-Green pairs covering each validation failure mode from §2.2.
These exercise `scenario.ValidateSeedBlock` directly — no Mongo, no
Docker — so they run on every `make test`.

### 5.3 Unit tests in `internal/runtime/` for Setup nil-tolerance

- `TestSandboxSetup_RoomsAndMemberOfEmptyIsNoOp` — `seed.Rooms = nil`
  + `member_of = nil` → Setup completes cleanly, `rooms` collection
  stays empty. Confirms Phase 4.2 is backward-compatible with the 8
  existing scenarios.
- `TestSandboxSetup_MongoNilSkipsRoomSeed` — `Deps.Mongo = nil` →
  Setup skips Step 6 cleanly. Matches today's nil-tolerant pattern at
  Step 3 / Step 5.

### 5.4 YAML loader unit tests

Cover the bare-string-shorthand UnmarshalYAML in
`internal/scenario/types_test.go`:

- `member_of: [r-engineering]` parses to `[{Room: "r-engineering",
  Roles: nil}]`.
- `member_of: [{room: r-engineering, roles: [owner]}]` parses to the
  full shape.
- Mixed list with both forms parses correctly.

### 5.5 End-to-end smoke (manual, after the spec is approved)

After implementation lands, write a temporary throwaway scenario that
declares 1 room + 2 members, runs `USE_INFRA=true make local`, and
manually inspects `last-run.md` for a `pass` row + uses
`mongosh` to confirm the 3 doc shapes. Burn the scenario before
landing the real messaging drafts.

## 6. Backward compatibility

Existing 8 scenarios don't declare `seed.rooms` or `member_of`. All
five new validation rules return `nil` when the relevant block is
empty. Step 6 is gated on `len(sb.Scenario.Seed.Rooms) > 0`. **No
existing scenario needs touching.**

The breaking type change at `SeedUserFlags` (map → struct) is
contained inside the suite — the YAML surface stays compatible because
`yaml.v3` parses `verified: true` into either shape.

## 7. Open questions for review

| # | Question | Default if no input |
|---|---|---|
| Q1 | Should the bare-string shorthand support roles via colon syntax (`"r-engineering:owner"`)? | **No** — the colon form is too clever; authors who need non-default roles take the object form. |
| Q2 | Should we seed a *parent* subscription doc for thread-rooms when seeding a thread room's parent? | **Out of scope** — thread rooms are message-worker-owned and don't exist until a thread starts. Phase 4.2 only covers regular rooms. |
| Q3 | Should `member_of` accept org memberships (`{org: "o-platform"}`)? | **No (Phase 4.2)** — `RoomMember.type` would be `org` and the lookup path is different. Defer to Phase 4.3 if needed. |
| Q4 | Should we expose `${room.<id>.name}` / `${room.<id>.uids}` substitution tokens? | **No (Phase 4.2)** — authors hardcode for now. Revisit if a refactoring pattern emerges. |
| Q5 | Should we drop `subscriptions` from `sandboxOwnedCollections` and instead let Step 6 rebuild it idempotently? | **No** — the drop discipline at Step 3 is a load-bearing invariant ("every scenario starts from byte-identical Mongo"). Step 6's inserts hit an empty collection, which is what we want. |

## 8. Non-goals (explicit)

- **Read-state seeding** (lastSeenAt, hasMention, unreadCount). Phase
  4.2 seeds memberships that look like fresh joins. Unread-state
  scenarios author this later via a separate seed primitive.
- **History-shared subscriptions.** `historySharedSince` stays unset.
- **Per-room `restricted: true` flag.** Add when a scenario needs it.
- **DM HRInfo enrichment.** DM subscriptions get the base shape; the
  `hrInfo` wrapper is a wire concern, not stored.
- **Cassandra seed rows.** Pre-existing messages are a SEPARATE Phase
  4.3 primitive (the `pre-existing-cassandra-rows` effect flagged in
  the gap analysis).

## 9. Net change estimate

| Layer | Files | Lines (approx) |
|---|---|---|
| Types | `internal/scenario/types.go` (struct change) + `internal/scenario/types_test.go` (new tests) | ~120 |
| Validation | `internal/scenario/validate_seed.go` (new) + `internal/scenario/validate_seed_test.go` (new) | ~180 |
| Sandbox | `internal/runtime/sandbox.go` (Step 1 / Step 3 / Step 6 changes) + helper file `internal/runtime/sandbox_rooms.go` (new) | ~150 |
| Tests | `internal/runtime/sandbox_integration_test.go` (one new function) + `internal/runtime/sandbox_test.go` (two new unit tests) | ~120 |

Total: ~570 lines added, with one breaking change to `SeedUserFlags`
contained inside the suite.

## 10. Recommended landing path

1. **Land this spec** (commit + push) — gets the design under
   review before any code moves.
2. **Type extensions + validator + unit tests** in one PR. Pure Go;
   nothing touches Sandbox yet. Reviewable in isolation.
3. **Sandbox.Setup wiring + integration test** in a second PR. Lands
   the actual Mongo writes. Validator from PR 1 is the safety net.
4. **First messaging scenario** (Draft #1 from the gap analysis) in a
   third PR — proves the seed effect unblocks a real test.

PRs 2 and 3 can run in series same-day; PR 4 is the demonstration that
the whole stack works end-to-end.

---

**Approval needed before any code lands.** Specifically:
- The two-block YAML design (§2.1) vs. an alternative.
- The non-Effect implementation choice (§4.3) — i.e., room seeding as
  a direct Sandbox.Setup step rather than a per-user `Effect`.
- The five open questions in §7.
