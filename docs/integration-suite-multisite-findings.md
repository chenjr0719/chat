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
