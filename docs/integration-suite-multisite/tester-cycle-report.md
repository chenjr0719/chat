# Tester → Suite Developer: cycle verification report

Verification of this cycle's suite changes (commits `5ee1a746` → `de020b1b`)
against live `USE_INFRA` runs, what remains unverified, and one new
still-open tool gap. Authored by the scenario-author/tester role; the suite
code is the developer's to own — this is a handoff, not a changelog.

---

## 1. Your fixes — verified live ✅

| Commit | Change | Verification | Result |
|--------|--------|-------------|--------|
| `5ee1a746` | `logs_tail` under `USE_INFRA` — container `Name` + fail-loudly | Run **c9f3** (regression guard failed *for the right reason* — polled the real line, not 0 events); reconfirmed by 4 scenarios that assert `logs_tail` and pass (`empty-content`, `not-subscribed`, `malformed-requestid`, `large-room-member-blocked`) | **Confirmed** |
| `9a1ed260` | `cassandra_data` — `coerceColumnTypes` (Gap A: int→`time.Time` for timestamp cols) + `normalizeNamedMaps` (Gap B: `SeedCassandraRow`→`map[string]interface{}` for UDTs) | Run **d5f9** — `thread-first-reply-happy-path`, all 6 surfaces green: literal `created_at` bound, `sender` UDT seeded → `GetMessageSender` resolved the author → parent-author subscription, `tcount: 1` proved PK coordination | **Confirmed** — unblocked 4 of 5 thread behaviors as predicted |
| `1a94f64f` | `SeedRoom.user_count` override (T1) | Run **1982** — large-room cap set green: member-blocked + owner/bot/thread bypasses. First all-legitimate `user_count` use (real membership + overridden count, no fabrication) | **Confirmed** |
| `de020b1b` | `mongo_data` caveat (pre-condition, not fire-fabrication) | Reviewed — captures the anti-pattern verbatim and keeps subsequent-reply on T2 | **Addresses the flagged framing risk** |

`da06e864` (§2.8) and `1f331f9b` (§2.9) are docs — no runtime surface to verify.

## 2. Landed but NOT yet tester-verified ⚠️

| Commit | Change | Status |
|--------|--------|--------|
| `9e7c979f` | `mongo_data:` block (T3) | 12 unit tests + validator clean, but **not yet driven through a live scenario.** The legitimate-use class (seeding a *pre-existing* doc a fire updates — not fabricating a skipped fire) is on the backlog. "Landed" ≠ tester-verified end-to-end. |

## 3. NEW tool gap — OPEN (highest priority)

**Cross-scenario contamination via in-process service caches** — surfaced while testing `user_count`.

- **Symptom:** `gatekeeper-large-room-owner-bypass` failed (owner wrongly capped, no canonical) in run **649f**, then **passed** in run **1982** with the *only* change being a unique room id.
- **Root cause:** the sandbox truncates **Mongo/Cassandra** per scenario, but service containers (gatekeeper) stay up for the whole run and keep their **in-process caches** — sub-cache keyed `(roomID, account)`, room-meta-cache keyed `roomID`, user-cache — each ~2m TTL. Scenarios ran alphabetically, so `member-blocked` (alice@`r-busy`, roles `[member]`) cached that projection; `owner-bypass` (alice@`r-busy`, roles `[owner]`) then got the **stale `[member]`** → `canBypassLargeRoomCap` saw no owner role → capped.
- **Why it's the worst class found:** **silent, order-dependent false verdicts** — not a loud setup error. A reordering could even make a *negative* scenario falsely pass. It quietly undermines trust in every multi-scenario run.
- **Severity:** high (soundness). The suite's "byte-identical state per scenario" guarantee is **DB-level only**.
- **Mitigation options (developer's call):**
  1. Flush/reset service caches between scenarios (or set cache TTLs to 0 in the test stack).
  2. A sandbox cache-bust hook (NATS admin nudge / targeted restart between scenarios).
  3. At minimum, document the constraint: scenarios in a run must not reuse `(account, roomID)` with differing per-key data. (We adopted unique-key discipline defensively, but it's a footgun for the next author.)

## 4. Feature requests surfaced (not bugs)

1. **Per-scenario service-env override.** For limit/threshold gates (large-room cap, future rate/size caps), the faithful pattern is "lower the limit, use a few real members" rather than inflate the data. That needs per-scenario env (`LARGE_ROOM_THRESHOLD`), which the infra bakes in at boot. A general per-scenario env-override knob would make every threshold gate testable with non-fabricated state. (`user_count` is sound for *this* gate because the gatekeeper reads only `userCount`, never re-derives — but the pattern doesn't generalize.)
2. **base64-payload decode matcher.** Cross-site assertions can only match the OUTBOX/INBOX *envelope* (`type/siteId/destSiteId`) because `OutboxEvent.Payload` is `[]byte` → base64 (already noted in `cross-site-room-rename-federation.yaml` §4.4). Caps cross-site tests at routing-correctness, not payload-content correctness.
3. **T2 — multi-fire / DAG (§2.3).** Still the last blocker for subsequent-reply / dedup / redelivery / tcount-concurrency. `mongo_data` is correctly *not* offered as a substitute.

## 5. Coverage delta from your fixes

Newly testable and green this cycle:
- **Threads first-reply** (room + both subscriptions + stamp + `tcount`) — via Gap A/B fixes.
- **Cross-site author-driven federation** (`OutboxThreadSubscriptionUpserted` to a remote parent author's home site) — via UDT seed.
- **Large-room cap** (block + owner/admin-role/bot/thread bypasses) — via `user_count`.

Still blocked, on a specific tool feature:
- Subsequent thread reply / dedup / redelivery / `tcount` concurrency → **T2**.
- Config-driven limit tests (general) → **per-scenario env override** (§4.1).
- Cross-site payload-content assertions → **base64 matcher** (§4.2).
- Worker-isolation events (system msgs, edits/deletes, nil-createdAt) → architectural boundary (user creds can't publish internal subjects — correct, out of scope).

## 6. Scenario drafts state (FYI)

Added **17** chat-app scenarios for the message-gatekeeper / message-worker
flow (committed `dcdfef4a`). Removed 2 tool-verification scenarios
(`logs-tail-positive-*`, `logs-tail-regression-guard-*`) — they tested the
`logs_tail` primitive, not the chat app; that protection belongs in
`internal/readers/container_logs_test.go`. Separately, 4 chat-app behavioral
findings (F-004–F-007) live in `docs/integration-suite-multisite-findings.md`
— those are for the **chat-app team**, not tool-side.

---

**Bottom line:** all four runtime fixes this cycle are verified working;
`mongo_data` is unit-tested but not yet tester-exercised end-to-end; and
there is **one new high-severity, still-open gap** — in-process cache
contamination across scenarios — to prioritize, because it yields silent
wrong results, not loud failures.
