# Integration suite multi-site — findings

Findings from the multi-site integration suite, addressed to the
**chat-app project team**. Each finding is a report — what we
observed, where it lives in the system, what the chat-app team
needs to decide. Not a TODO for the suite team and not a changelog
of how the test tool reached its current shape.

Per-run reports under `docs/integration-suite-multisite/last-run.md`
are overwritten every run. Findings here are durable.

Format per finding:

```
F-NNN  <one-line title>
       Layer:  <chat-app code | chat-app local-dev tooling | ops/IaC>
       Status: <observed — chat-app team action pending>
```

---

## F-001 — `OUTBOX_<site>` has no production owner

**Layer:** ops/IaC (or chat-app code, if the team decides a service
should own it).

**Status:** observed — chat-app team action pending.

No chat-app service bootstraps `OUTBOX_<site>` in production. When
a producer (e.g. `room-worker`) tries to publish a cross-site
metadata event to `outbox.<site>.>` without the stream present,
NATS returns `no response from stream`. The chat-app code is
correct — the bootstrap responsibility is genuinely outside the
service's scope per `CLAUDE.md` §"Stream bootstrap ownership"
("streams are owned by ops/IaC").

The decision the chat-app team owns: **who creates
`OUTBOX_<site>` in production?**

- IaC at deploy time (matches the pattern used for other shared
  streams)
- An ops-owned bootstrap container, run once per cluster
- A designated chat-app service whose responsibility is OUTBOX
  schema ownership

Until designated, every cross-site producer fails on first publish
in any fresh environment. The integration suite works around this
via a per-scenario `pre_fire_scripts` hook that stands up OUTBOX
before the fire — operator-owned and explicit, not invented inside
the harness.

---

## F-002 — production federation topology shape

**Layer:** ops/IaC.

**Status:** observed — chat-app team action pending.

Cross-site JetStream federation requires two pieces that no chat-app
service ships and no current IaC reference declares:

1. **Transport that carries `$JS.<peer>.API.*` across sites.**
   NATS supercluster gateways do not. NATS leafnodes do. The
   integration suite uses leafnodes
   (`tools/integration-suite-multisite/internal/infra/nats.gateway.*.conf`)
   as a working reference.

2. **`SubjectTransform` on each federation `Source`** that rewrites
   `outbox.<remote>.to.<site>.>` → `chat.inbox.<site>.aggregate.>`
   on the way into the destination `INBOX_<site>`. Without the
   transform, federated messages arrive under the `outbox.*`
   namespace, get rejected by `INBOX_<site>`'s declared subjects,
   and even if they landed they'd be invisible to `inbox-worker`'s
   consumer (which binds to `chat.inbox.<site>.aggregate.>`).
   The chat-app's own `pkg/stream/stream.go` `Inbox()` docstring
   lines 64-69 already document the transform shape; the
   integration suite's `internal/infra/federation.go` `Apply` is
   an executable reference for what production `Sources` need to
   look like.

Decisions the chat-app team owns:

- Does production federate over leafnodes (or an equivalent that
  carries `$JS.<peer>.API.*` cross-cluster)?
- Is the SubjectTransform shipped at the federation IaC layer, or
  somewhere else?
- Once decided, mirror the shape in `docker-local/setup.sh` (or a
  sibling) so multi-site federation features can be verified
  locally without standing up the integration-suite-multisite
  stack.

---

## F-003 — `message-worker/README.md` describes a stream layout that no longer exists

**Layer:** chat-app code (doc only).

**Status:** observed — chat-app team action pending.

`message-worker/README.md` describes the service as consuming the
`MESSAGES` stream and publishing to a `FANOUT` stream. The actual
service (verified against `message-worker/main.go` +
`store_cassandra.go`) consumes from `MESSAGES_CANONICAL_<site>` —
the canonical stream split that landed when `message-gatekeeper`
was introduced as the validation gate ahead of message-worker — and
writes to Cassandra (`messages_by_id` + `messages_by_room` via
UnloggedBatch). No publishes to any `FANOUT` stream; that name
isn't declared anywhere in `pkg/stream/stream.go`.

The doc drift made authoring the
`message-pipeline-send-and-persist` scenario harder — an author
reading the README first would build the wrong subject/stream
graph in their head and either fire on a non-existent stream or
look for non-existent canonical events.

The chat-app team owns the doc. The fix: update
`message-worker/README.md` to describe the real consume
(`MESSAGES_CANONICAL_<site>` → `chat.msg.canonical.<site>.created`)
and write (`messages_by_id` + `messages_by_room`) shape, matching
what `message-gatekeeper/handler.go:167-330` (publishes the
canonical) and `message-worker/handler.go` (consumes + persists)
actually do.

---

## F-004 — gatekeeper accepts thread replies to non-existent parents (orphaned threads)

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

`message-gatekeeper`'s `processMessage` validates a thread reply's
`threadParentMessageId` for FORMAT only (`idgen.IsValidMessageID`,
handler.go:191-193) and that `threadParentMessageCreatedAt` is paired
with it (handler.go:209-211). It never verifies the parent message
actually exists before publishing the canonical event. A client can
send a thread reply whose `threadParentMessageId` is any syntactically
valid 20-char base62 string pointing at nothing.

`message-worker` then builds an **orphaned thread**: `CreateThreadRoom`
succeeds, the reply is persisted, but `handleFirstThreadReply`'s
`GetMessageSender(parent)` returns `errMessageNotFound` and
early-returns (handler.go:155-162) — skipping BOTH the parent-author
and the replier `thread_subscriptions`. The reply is Ack'd; nothing
errors.

Demonstrated by
`scenarios/drafts/thread-reply-to-nonexistent-parent-creates-orphan.yaml`
(green): canonical published, orphan `thread_rooms` doc created, reply
persisted with `thread_parent_id` set, ZERO `thread_subscriptions`.

Decision the team owns: should a thread reply whose parent does not
exist be rejected (gatekeeper verifies existence, or worker refuses to
create a room for a missing parent), or is silently building the orphan
acceptable? Consequence today: a client can manufacture unbounded
orphaned `thread_rooms` (pollution/abuse), replies unreachable via a
non-existent parent, and the orphan thread has no subscribers.

---

## F-005 — malformed (present-but-non-UUID) requestId is silently dropped

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

When a `msg.send` payload carries a `requestId` that is present but not
a valid hyphenated UUID, `processMessage` rejects it with
`errcode.BadRequest` (handler.go:178-180) — but `sendReply` then
**no-ops**, because its guard requires `req.RequestID` to pass
`idgen.IsValidUUID` (handler.go:143-145), the very predicate that just
failed. The reply subject `chat.user.{account}.response.{requestId}`
would be unroutable, so nothing is published. The client receives
NOTHING — no success, no error — and the send is dropped.

Contrast: an EMPTY-content rejection with a *valid* requestId IS
delivered (`gatekeeper-empty-content-rejected.yaml`). The differentiator
is solely requestId routability, not the rejection class.

Demonstrated by
`scenarios/drafts/gatekeeper-malformed-requestid-silent-drop.yaml`
(green): no reply reaches the client, gatekeeper logged the bad_request
rejection, no canonical event published.

Decision the team owns: is a silent drop on a malformed requestId
acceptable (client must time out), or should the client be told?

---

## F-006 — a missing quoted-parent drops the entire message, not just the quote

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

When a `msg.send` quotes a parent (`quotedParentMessageId`) that does
not exist, `resolveQuoteSnapshot` propagates history-service's typed
`NotFound` verbatim (handler.go:312-326), and `processMessage` returns
it before publishing — the WHOLE message is dropped (client gets a
`not_found` reply, no canonical event / Cassandra row).

This **contradicts the `ParentMessageFetcher` interface doc**
(store.go:33-34): "the handler soft-fails on every error and ships the
message without the quote." The implementation hard-fails the entire
send on NotFound. Code and stated contract diverge — one side is wrong.

Demonstrated by
`scenarios/drafts/gatekeeper-quote-nonexistent-parent-drops-message.yaml`
(green): reply `not_found`, no canonical, no `messages_by_id` row.
(Positive counterpart `gatekeeper-quote-happy-path-embeds-snapshot.yaml`
confirms the success path embeds + persists the snapshot.)

Decision the team owns: should a bad quote target drop the whole message
(current behavior) or soft-fail as the doc describes? This is a
code-vs-contract mismatch.

---

## F-007 — whitespace-only message content is accepted (no trim)

**Layer:** chat-app code.

**Status:** observed — chat-app team action pending.

`message-gatekeeper`'s non-empty content gate is an exact empty-string
check — `if req.Content == ""` (handler.go:196-198) — with no trimming.
A whitespace-only body ("   ", "\n", a tab) passes validation, is
published, and persisted verbatim. The only other content gate is the
20KB size cap. A user can post blank-looking messages at will.

Demonstrated by
`scenarios/drafts/gatekeeper-whitespace-only-content-accepted.yaml`
(green): a "   " send produces a canonical event and a persisted row
with msg="   ".

Decision the team owns: trim before the non-empty check (reject
whitespace-only), or is it intentionally allowed?
---

## F-008 — `publishThreadSubOutboxIfRemote` has three observationally-indistinguishable exit paths

**Layer:** chat-app code (observability).

**Status:** observed — chat-app team action pending.

In `message-worker/handler.go`, `publishThreadSubOutboxIfRemote`
has three exit paths that all look identical from an operator's
log:

- `ownerSiteID == ""` → `slog.WarnContext("owner siteID empty, skipping outbox publish")`, return nil
- `ownerSiteID == h.siteID` → silent return nil (same-site skip)
- successful cross-site publish → silent return nil

When a cross-site federation scenario fails to deliver an event
to `OUTBOX_<site>`, the operator has no log trace to distinguish
"the publish silently succeeded but didn't land on the stream"
from "the publish was correctly skipped because the remote-user
data looked local."

Surfaced during authoring of
`thread-first-reply-remote-parent-federates-subscription` (Run
sequence ending in the 18/19 cycle). Surfaces 2 and 3 of the
scenario prove the handler reached `InsertThreadSubscription` for
both the parent author and the replier (Mongo rows present).
Surface 4 (`jetstream_consume` on `OUTBOX_site-a` filtered by
`outbox.site-a.to.site-b.thread_subscription_upserted`) times out
with zero events. The full message-worker log across the entire
run contains zero log lines mentioning the test's message IDs at
all — no "owner user not found" warn, no "owner siteID empty"
warn, no publish-error error. The publish, if it happened, left no
trace.

**Recommended fix (one line):**
Add a `slog.InfoContext` log immediately before the `h.publish(...)`
call in `publishThreadSubOutboxIfRemote`:

```go
slog.InfoContext(ctx, "publishing thread subscription outbox",
    "ownerSiteID", ownerSiteID,
    "threadRoomID", sub.ThreadRoomID,
    "user_id", sub.UserID,
    "msgID", msgID,
    "subject", subj,
    "request_id", natsutil.RequestIDFromContext(ctx))
```

After this lands, re-run the failing scenario:
- Log fires with `ownerSiteID="site-b"` and the cross-site subject
  → publish was attempted; the gap is downstream (subject not
  captured by the stream, JetStream dedup window swallowing it,
  etc.). Cheap to localize from there with a stream inspect at the
  right moment.
- Log doesn't fire → the handler isn't actually reaching the publish
  branch for this scenario despite Surfaces 2+3 proving it ran past
  the upsert. Most likely cause to look at: the subsequent-reply
  branch being taken instead of first-reply due to state leakage
  from a prior scenario in the same run (see F-009).

**Adjacent (broader audit):**

- Same observability gap applies to the replier publish a few lines
  below in `handleFirstThreadReply` —
  `publishThreadSubOutboxIfRemote(ctx, replierSub, replier.SiteID,
  msg.ID)`. One log line covers both call sites.
- The same silent-success pattern likely exists in other
  `publish*OutboxIfRemote` helpers across `room-worker` and
  `room-service`. Worth a sweep with the same instrumentation
  discipline. Closes the parallel of `plan-ahead §2.9` at the
  production-code layer.

---

## F-009 — Service in-process caches violate per-scenario isolation

**Layer:** chat-app code (cache lifecycle / test-environment configurability).

**Status:** observed — chat-app team action pending. High severity (soundness).

The integration suite's `Sandbox.Setup` drops Mongo collections and
truncates Cassandra tables between scenarios, guaranteeing
byte-identical store state at scenario start. But the service
containers (gatekeeper, room-service, others) stay up for the whole
run and keep their **in-process caches** — sub-cache keyed
`(roomID, account)`, room-meta-cache keyed `roomID`, user-cache,
each with ~2-minute TTLs.

When scenario N populates a cache key, scenario N+1 — even with
clean Mongo state — can see the stale cached projection if it
references the same key within the TTL window.

**Concrete failure** (Run 649f → 1982 in the latest cycle):
- `gatekeeper-large-room-member-blocked` ran first, caching
  `(alice@r-busy, roles=[member])`.
- `gatekeeper-large-room-owner-bypass` ran second, expected
  `(alice@r-busy, roles=[owner])`. The cached `[member]` projection
  won → `canBypassLargeRoomCap` saw no owner role → capped → wrong
  verdict.
- Run 1982 fixed it by giving the second scenario a unique room ID.
  Only difference. Same code, same env.

**Why this is the worst class of bug:** silent, order-dependent
false verdicts — not a loud setup error. A scenario reordering
could falsely-green a negative scenario without anyone noticing.
The suite's "byte-identical state per scenario" guarantee turns
out to be DB-level only.

**Mitigation options for the chat-app team:**

1. **Env-driven cache TTL override.** Services accept e.g.
   `*_CACHE_TTL` env vars; the test stack sets them to `0`
   (disabling the cache in the test environment). Smallest
   architectural shape; preserves production caching behavior
   unchanged.
2. **Admin cache-flush endpoint.** Each cache-holding service
   exposes a NATS or HTTP admin verb to invalidate its caches.
   The test sandbox calls it between scenarios. More plumbing;
   useful operationally too (cache flush on demand without restart).

The test tool can mitigate at the author-discipline layer (use
unique `(account, roomID)` per scenario — see plan-ahead §2.10) but
the discipline is a footgun, not a fix. The structural fix is
in chat-app code.

---
