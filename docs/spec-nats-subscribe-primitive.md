# Spec: `nats_subscribe` — the 6th Universal Primitive

**Status:** Draft for review (no code yet)
**Phase:** 4.5 — Transient Broadcast Coverage
**Spec date:** 2026-06

## 1. Motivation

Five universal primitives ship today (Phase 4.0 architecture, plus
Phase 4.4's Gomega-delegated array matcher in `matches_shape`):

| Primitive | Backs | Persistence |
|---|---|---|
| `mongo_find` | MongoDB collections | persisted |
| `cassandra_select` | Cassandra tables | persisted |
| `jetstream_consume` | JetStream streams (MESSAGES, MESSAGES_CANONICAL, ROOMS, OUTBOX, INBOX) | persisted; replayable via `DeliverByStartTime` |
| `logs_tail` | Container stdout/stderr | streamed |
| `reply` | NATS request/reply (dispatcher-fed) | synchronous |

The remaining blind spot is **Core NATS pub/sub** — the transient
fan-out path `notification-worker` and `broadcast-worker` use to
deliver per-room and per-user events. `broadcast-worker/handler.go:188`
publishes `chat.room.<roomID>.event`; `handler.go:198` publishes
`chat.user.<account>.event.room`. Neither is persisted: subscribers
that aren't listening at publish time miss the message entirely.

Without a primitive that observes these subjects, scenarios can:

- Assert what the worker WRITES (via `cassandra_select` for message
  rows, `mongo_find` for subscription state).
- Assert what arrives on the canonical JetStream stream (via
  `jetstream_consume`).
- Assert the worker LOGGED a publish (via `logs_tail` matching the
  worker's own slog line).

…but cannot assert that the broadcast actually fanned out to the
right subjects with the right payloads. Three currently-undraftable
scenarios depend on this:

- `broadcast-worker-fans-out-room-event.yaml` — proves
  `RoomCreated` events reach `chat.room.<id>.event`.
- `notification-worker-respects-mute-flag.yaml` — proves muted users
  do NOT receive `chat.user.<account>.event.room` notifications.
- `cross-site-outbox-publishes-room-event.yaml` — proves the
  inbox-worker's downstream broadcast hits the local fan-out subject
  after a cross-site event replay.

Phase 4.5 closes the loop with `nats_subscribe` — the 6th and final
universal primitive.

## 2. YAML authoring surface

### 2.1 Single-subject form

```yaml
- location: nats_subscribe
  args:
    subject: chat.room.r-engineering.event
  match:
    received:
      - subject: chat.room.r-engineering.event
        body_json:
          event: room.created
          roomId: r-engineering
```

The `args.subject` value is a Core NATS subject literal or wildcard
pattern (`*`, `>`). The primitive opens an ephemeral subscription
**before** the case's verb fires, buffers every arriving message in
order, and returns the buffered slice on each `PollFn` call.

### 2.2 Wildcard subject form

```yaml
- location: nats_subscribe
  args:
    subject: chat.room.*.event       # one * → single-token wildcard
  match:
    received:
      - subject: chat.room.r-engineering.event
        body_json: {event: room.created}
      - subject: chat.room.r-design.event
        body_json: {event: room.created}
```

The actual subject of each received message is exposed under
`received[].subject` so the author can disambiguate when subscribing
to a pattern.

### 2.3 ROSM integration — one event, many received messages

`PollFn` returns **exactly one synthetic `readers.Event`** whose
`Payload` carries the full ordered list of arrivals. This is the
crucial design choice that makes the Phase 4.4 Relative Order
Subset Matcher work out of the box: the author asserts on the
`received` array using the same `[]any` syntax `matches_shape` already
dispatches to ROSM.

| Surface | Maps to ROSM as |
|---|---|
| Scenario YAML `match.received: [{…}, {…}]` | `[]any` of `map[string]any` |
| Subscription's buffered arrivals | `Payload.Received []NATSReceivedMessage` → marshalled to `[]any` for matchshape |
| Expected ordering | enforced by ROSM's greedy forward pass |
| Intervening unlisted broadcasts | tolerated (subset semantics) |
| Wrong-order regression | "RELATIVE-ORDER VIOLATION" in `## Failure Details` |
| Missing broadcast | "MISSING" classifier |

The primitive itself is **zero-LOC ROSM logic** — it just delivers
the slice; ROSM does the matching. This is the same "compose
upstream primitives" discipline Phase 4.4 established for Gomega
delegation.

### 2.4 `not: true` for negative assertions

The scenario-level `not: true` flag works unchanged:

```yaml
- location: nats_subscribe
  args:
    subject: chat.user.${alice.account}.event.room
  not: true
  match:
    received:
      - body_json:
          event: notification.muted-user-skipped
```

Case-runner switches to `Consistently().ShouldNot()`, polling for the
default window. If the muted-user-skipped broadcast EVER arrives
during the window, the assertion fails. The case
`notification-worker-respects-mute-flag.yaml` lands trivially on this.

## 3. Ephemeral lifecycle

### 3.1 Subscription must open BEFORE the verb fires

Core NATS has no replay. If `nats.Subscribe` runs after the publish,
the message is lost. JetStream sidesteps this with
`DeliverByStartTime` set to `sb.StartTime`, replaying every message
since T_open even when the consumer is opened lazily. Core NATS
has no equivalent.

The case-runner's current sequence
(`internal/runtime/case_runner.go:104-110`) is:

```
Step 4: dispatcher.Fire(ctx, ..., verb, ...)     ← publish happens here
Step 5: for each expected: evalExpected(...)     ← PollFn called here
```

For `nats_subscribe`, this is too late. Step 4 publishes the broadcast;
Step 5 (lazy `PollFn` call) opens the subscription AFTER. We need a
pre-Fire warmup.

### 3.2 The `Warmer` optional interface

New optional interface in `internal/runtime/pollers/registry.go`:

```go
// Warmer is implemented by pollers that need to initialize state
// BEFORE the case-runner's dispatcher.Fire call. Today only the
// Phase 4.5 nats_subscribe primitive needs it — Core NATS has no
// replay, so subscriptions must be live at publish time.
//
// Pollers without warmup needs (mongo_find, cassandra_select,
// jetstream_consume, logs_tail, reply) do NOT implement Warmer.
// case_runner type-asserts at the call site so the interface is
// purely opt-in.
type Warmer interface {
    Warm(args map[string]any) error
}
```

In `case_runner.go`, inserted between Step 3 (mishap setup) and
Step 4 (Fire):

```go
// Step 3b: warm pollers that need pre-Fire initialization.
// Phase 4.5 — Core NATS subscriptions must be live before the
// verb publishes its first message. JetStream/Mongo/Cassandra/log
// readers don't implement Warmer and are skipped.
for i := range c.Expected {
    poller, err := sb.PollerReg.Get(c.Expected[i].Location)
    if err != nil { continue }                  // unknown location surfaces at Step 5
    if w, ok := poller.(Warmer); ok {
        args, _ := substituteArgs(c.Expected[i].Args, subCtx)
        if err := w.Warm(args); err != nil {
            return VerdictV3{}, fmt.Errorf("warm %s: %w", c.Expected[i].Location, err)
        }
    }
}
```

### 3.3 Per-scenario subscription cache

The `nats_subscribe` primitive maintains a `map[string]*subscription`
keyed by the resolved subject literal. `Warm` opens once per subject;
subsequent calls (same scenario, different cases hitting the same
subject) are no-ops. This mirrors `jetstream_consume`'s per-args
caching at `pollers/jetstream_consume.go:90`.

Each cached entry holds:

- `*nats.Subscription` (for `Unsubscribe` at teardown)
- `chan *nats.Msg` (buffered, size 256)
- `mu sync.Mutex` (guards the drain-and-return list)
- `received []NATSReceivedMessage` (accumulated arrivals)

The drain pattern: `PollFn`'s returned closure pulls every message
the goroutine has pushed since the last drain, appends to `received`,
and returns ONE `Event` whose payload is the full accumulated list.
**Accumulation is monotonic across `PollFn` calls within one case** —
the Eventually loop sees the buffer grow until it satisfies the
matcher.

### 3.4 Teardown — no goroutine leaks

`RegisterBuiltinPollers` returns a cleanup `func()` that
`Sandbox.Teardown` invokes via `pollerCleanup` (already wired today
for `jetstream_consume`'s ephemeral consumers). The
`nats_subscribe` poller's cleanup walks its cache:

```go
for _, sub := range cache {
    _ = sub.subscription.Unsubscribe()  // best-effort; conn may be drained
    close(sub.msgChan)                  // signals the goroutine to exit
}
```

The forwarder goroutine reads from `sub.msgChan`; closing the channel
breaks its loop. No leaked goroutines, no leaked NATS subscriptions.
Connection lifecycle stays with `SandboxDeps.AdminConn` — we own the
subscription, not the connection.

### 3.5 Connection — reuse `SandboxDeps.AdminConn`

The primitive uses `sb.Deps.AdminConn` — the operator-JWT-authenticated
admin connection already shared by `jetstream_consume`. AdminConn nil
→ same warn-and-degrade pattern as today: `Warm` returns nil, `PollFn`
returns no events, the Eventually loop times out, the case fails with
a clear "no admin NATS connection" message.

## 4. Architecture

### 4.1 File layout

| File | Status | Role |
|---|---|---|
| `internal/readers/nats_subscribe.go` | new (~110 LOC) | `NATSSubscribeReader` — opens subscription, buffers, exposes a drain method. |
| `internal/runtime/pollers/nats_subscribe.go` | new (~120 LOC) | `NATSSubscribePoller` — implements `Poller` + `Warmer`; per-subject cache; integrates with `RegisterBuiltinPollers`. |
| `internal/runtime/pollers/registry.go` | edit (+15 LOC) | Add the `Warmer` interface declaration. |
| `internal/runtime/case_runner.go` | edit (+18 LOC) | Step 3b — warm pollers before Fire. |
| `internal/runtime/pollers/builtins.go` | edit (+5 LOC) | Register `nats_subscribe` in `RegisterBuiltinPollers`; wire cleanup. |
| `internal/runtime/pollers/nats_subscribe_test.go` | new (~270 LOC) | Pure-Go unit tests using `nats-server/v2/test` in-process server. |
| `internal/runtime/pollers/nats_subscribe_integration_test.go` | new (~120 LOC) | Docker-tagged end-to-end. |

### 4.2 The reader's surface

```go
// NATSSubscribeReader owns one Core NATS subscription. Buffers
// arrivals in declaration order, exposes a drain method.
type NATSSubscribeReader struct {
    conn    *nats.Conn
    subject string
    sub     *nats.Subscription
    mu      sync.Mutex
    queue   []NATSReceivedMessage
    closed  bool
}

func NewNATSSubscribeReader(conn *nats.Conn, subject string) *NATSSubscribeReader

// Open synchronously calls nats.Subscribe so the subscription is
// guaranteed live when Open returns. Per-message handler appends
// to queue under the mutex.
func (r *NATSSubscribeReader) Open() error

// Drain returns the messages received since the last Drain call,
// appending to a caller-supplied accumulator slice for monotonic
// growth across PollFn loops.
func (r *NATSSubscribeReader) Drain(into []NATSReceivedMessage) []NATSReceivedMessage

// Close unsubscribes + marks the reader closed. Safe to call twice.
func (r *NATSSubscribeReader) Close() error
```

### 4.3 The poller

```go
type NATSSubscribePoller struct {
    conn  *nats.Conn               // nil → degrade, log Warn, return no events
    mu    sync.Mutex
    cache map[string]*subEntry     // key = resolved subject literal
}

type subEntry struct {
    reader   *NATSSubscribeReader
    received []NATSReceivedMessage   // monotonic accumulator
}

func (p *NATSSubscribePoller) Warm(args map[string]any) error {
    subject := requireSubject(args)
    p.mu.Lock(); defer p.mu.Unlock()
    if _, ok := p.cache[subject]; ok { return nil }   // idempotent

    rdr := NewNATSSubscribeReader(p.conn, subject)
    if err := rdr.Open(); err != nil {
        return fmt.Errorf("nats_subscribe: open %q: %w", subject, err)
    }
    p.cache[subject] = &subEntry{reader: rdr}
    return nil
}

func (p *NATSSubscribePoller) PollFn(args map[string]any, _ string) func() []readers.Event {
    subject := requireSubject(args)
    return func() []readers.Event {
        p.mu.Lock(); defer p.mu.Unlock()
        entry, ok := p.cache[subject]
        if !ok {
            // Defensive: Warm should have run first.
            slog.Warn("nats_subscribe: PollFn before Warm", "subject", subject)
            return nil
        }
        entry.received = entry.reader.Drain(entry.received)
        return []readers.Event{{
            Location:  "nats_subscribe",
            Timestamp: time.Now(),
            Type:      readers.EventCascade,
            Payload: NATSSubscribePayload{
                Subject:  subject,
                Received: entry.received,
            },
        }}
    }
}

func (p *NATSSubscribePoller) Close() {
    p.mu.Lock(); defer p.mu.Unlock()
    for _, entry := range p.cache {
        _ = entry.reader.Close()
    }
    p.cache = nil
}
```

### 4.4 Payload shape

```go
type NATSSubscribePayload struct {
    Subject  string                 `json:"subject"`         // the SUBSCRIBED pattern (incl. wildcards)
    Received []NATSReceivedMessage `json:"received"`         // arrivals in delivery order
}

type NATSReceivedMessage struct {
    Subject  string              `json:"subject"`            // ACTUAL subject of this message
    BodyJSON map[string]any      `json:"body_json,omitempty"`
    BodyRaw  string              `json:"body_raw,omitempty"`
    Header   map[string][]string `json:"header,omitempty"`
}
```

The two-tier `subject` field (subscribed pattern vs. actual delivery
subject) lets wildcard subscriptions disambiguate per-message. The
`body_json`/`body_raw` split mirrors `ReplyPayload` and
`JetStreamSubjectPayload` — authors don't have to learn new fields.

### 4.5 Buffer policy

| Bound | Behavior |
|---|---|
| 256 messages in transit (channel buffer) | If exceeded, drop new arrivals with `slog.Warn` (matches `jetstream_consume`'s drop policy at `readers/jetstream_subject.go:101-107`). |
| Accumulator grows unbounded | Per-case scope only — cleared at scenario teardown. A pathological scenario receiving 100k+ broadcasts would consume memory; document as Phase 4.5.1 if it becomes a problem. |
| Goroutine count | Exactly ONE forwarder goroutine per cached subject. Bounded by the number of distinct `args.subject` values across all cases. |

## 5. Diagnostics

### 5.1 Failure mode taxonomy

| Failure | Reason surfaced |
|---|---|
| AdminConn nil (operator forgot `NATS_CREDS_FILE`) | `Warm` logs `slog.Warn`, returns nil. `PollFn` returns empty. Eventually times out → `MatchShape: polled 1 event, none matched expected shape …; closest mismatch: matches_shape: field "received": expected element [0] not found at or after observed[0] (cursor advanced through 0/0 elements)\nMISSING: …` |
| Subscribe error (invalid subject) | `Warm` returns error; case_runner propagates: `RunCaseV3 "<case>": warm nats_subscribe: open "<subject>": nats: invalid subject`. |
| Subscription opens but no messages arrive | Same as AdminConn nil — empty `received`, ROSM `MISSING` classifier fires for every expected element. |
| Messages arrive but in wrong order | ROSM's standard `RELATIVE-ORDER VIOLATION` classifier fires; Gomega's pretty-printed child diff surfaces in `## Failure Details`. |
| Messages arrive with wrong payload fields | ROSM's `closest candidate's diff:` block surfaces the failing field — gstruct's pretty-printer pinpoints the bad key. |
| Buffer overflow during scenario | `slog.Warn("nats_subscribe: buffer full; dropping message", "subject", …)` — visible in runner stdout; matchshape may then fail to find an expected element → `MISSING` with no nearby candidate. |

### 5.2 The `## Failure Details` chain — unchanged

ROSM's `FailureMessage` → `shapeMatcher.bestReason` →
`reporter.renderFailureDetails` → `last-run.md` `## Failure Details`.
The same chain Phase 4.4 just shipped; nats_subscribe inherits it
for free by returning a `received []any` array under a standard
field.

### 5.3 Worked example — broadcast-worker fan-out regression

YAML:

```yaml
- location: nats_subscribe
  args:
    subject: chat.room.r-engineering.event
  match:
    received:
      - body_json: {event: room.created}
      - body_json: {event: member.added}
```

If broadcast-worker regressed to emit `member.added` BEFORE
`room.created`, the `## Failure Details` block would read:

```
matches_shape: field "received": expected element [0] not found at or after observed[2] (cursor advanced through 2/2 elements)
RELATIVE-ORDER VIOLATION: this element exists at observed index 1, but the cursor already advanced past it
closest candidate's diff:
Expected
    <map[string]interface {}>: …event: room.created…
to match keys: {
."body_json": …
}
```

— immediately identifying the inversion.

## 6. Test plan

### 6.1 Pure-Go unit tests (no Docker)

`pollers/nats_subscribe_test.go` uses
`github.com/nats-io/nats-server/v2/test` to spin an in-process
NATS server. The harness pattern is already used elsewhere in
the repo (e.g. `pkg/natsutil/*_test.go`).

| # | Scenario | Assertion |
|---|---|---|
| 1 | Warm + publish + PollFn → 1 event with 1 received | `received[0].subject == published subject` |
| 2 | Warm + 3 publishes → PollFn returns 3 received in publish order | indices 0/1/2 match publish order |
| 3 | Wildcard subscription `chat.room.*.event` + publishes to two different rooms → `received[].subject` distinguishes | per-message subject preserved |
| 4 | Warm is idempotent (called twice → one Subscribe under the hood) | NATS server's per-subject subscriber count == 1 |
| 5 | PollFn before Warm → no events + slog.Warn | bench test for the defensive branch |
| 6 | AdminConn nil → Warm returns nil + PollFn returns empty | nil-tolerant gate verified |
| 7 | Subscribe error (invalid subject `""`) → Warm returns error | error message names the subject |
| 8 | Buffer full (publish 300 fast, buffer=256) → 256 captured + `slog.Warn` | bench test under load |
| 9 | Cleanup unsubscribes + closes goroutine → no goroutine leak | `goleak.VerifyNone(t)` after Close |
| 10 | JSON-decodable payload → `BodyJSON` populated, `BodyRaw` empty | round-trip a `{"event":"x"}` |
| 11 | Non-JSON payload → `BodyRaw` set, `BodyJSON` nil | round-trip a `"plain text"` bytes |
| 12 | Header propagation (`X-Request-ID: <uuid>`) → `Header["X-Request-Id"]` matches | bench the header copy path |
| 13 | Monotonic accumulation across PollFn calls — 2 PollFn calls between which 1 publish lands → second call sees BOTH the first call's events + the new one | proves the Eventually loop sees buffer growth |
| 14 | Cross-case isolation — same subject across two cases reuses subscription, both see accumulated buffer (documented behavior; author's responsibility for case-scope reset) | bench the cache shape |

### 6.2 Docker-tagged integration tests

`pollers/nats_subscribe_integration_test.go` (build tag
`integration`):

- `TestNATSSubscribePoller_EndToEndAgainstLiveStack` — runs against
  the real Docker NATS, fires a publish from a separate `nats.Conn`,
  asserts the poller's PollFn returns the message.
- `TestNATSSubscribePoller_WildcardEndToEnd` — wildcard subscription
  across two distinct subjects.

### 6.3 Scenario-level end-to-end demonstration (Phase 4.5.1 PR)

After the primitive lands, author
`scenarios/drafts/broadcast-worker-fans-out-room-event.yaml`:

```yaml
scenario: broadcast_worker_fans_out_room_event
source: broadcast-worker/handler.go:188 + docs/spec-nats-subscribe-primitive.md
seed:
  users:
    alice: {verified: true}
  rooms:
    - id: r-engineering
      type: channel
  memberships:
    alice: [r-engineering]
base_input:
  verb: jetstream_publish
  subject: chat.msg.canonical.${site}.created
  payload: {…canonical event…}
  credential: ${service.backend}
cases:
  - name: room-event-fans-out-to-core-nats
    tag: positive
    expected:
      - location: nats_subscribe
        args:
          subject: chat.room.r-engineering.event
        match:
          received:
            - body_json:
                event: room.created
                roomId: r-engineering
```

This validates the primitive end-to-end against the live broadcast
worker. Ships in the demonstration PR after the matcher.

### 6.4 Coverage target

95%+ on `pollers/nats_subscribe.go` and `readers/nats_subscribe.go`
(every branch in `Warm`/`PollFn`/`Open`/`Drain`/`Close` exercised);
package overall ≥80%. Goroutine leakage pinned via `goleak`.

## 7. Open questions for review

| # | Question | Default if no input |
|---|---|---|
| Q1 | Should `Warmer` be a public interface in the `pollers` package or kept internal? | **Public** — future primitives (e.g. a hypothetical `websocket_subscribe`) may need the same hook. |
| Q2 | Should `Warm` errors hard-fail the case OR log + degrade like nil AdminConn? | **Hard-fail** — Warm errors mean operator misconfig (bad subject, no creds). Degrading would silently turn the case into a guaranteed assertion failure with a misleading "no events" reason. |
| Q3 | Should `Warm` accept a context for cancellation? | **No (Phase 4.5)** — Subscribe is non-blocking, sub-millisecond. Add when a real cancellation use case surfaces. |
| Q4 | Buffer size — 256 fixed vs. configurable? | **Fixed at 256.** Configurable would require args.buffer_size and per-call sanity validation. Defer. |
| Q5 | Should we surface the subscription subject as a substitution token (e.g. `${input.subject}`) so the assertion can reference it dynamically? | **No (Phase 4.5)** — the YAML already declares it explicitly under `args.subject`. |
| Q6 | Should we deduplicate identical received messages (idempotent delivery)? | **No** — Core NATS already deduplicates at the connection layer; surfacing duplicates is the producer's fault and should fail the test. |
| Q7 | Should the cleanup `func()` returned by `RegisterBuiltinPollers` be guarded against concurrent calls? | **Yes — `sync.Once`** mirroring `Sandbox.Teardown`'s pattern. |
| Q8 | Should we also support queue groups (`args.queue: "X"`)? | **No (Phase 4.5)** — queue groups only matter for parallel scenarios sharing a subject. The harness runs sequentially. |

## 8. Non-goals (explicit)

- **Request/reply via `nats_subscribe`** — the `reply` primitive
  already covers synchronous request/reply.
- **Persistent subscription** — that's `jetstream_consume`'s job.
- **Subject filtering BEYOND Core NATS wildcards** — if the operator
  needs to assert only on a subset of arrivals, the ROSM matcher in
  `match.received` handles it.
- **Per-message ack/nack** — Core NATS has no ack semantics.
- **Cross-scenario subscription sharing** — every scenario's
  `Sandbox.Teardown` closes all subscriptions. Next scenario opens
  fresh.

## 9. Backward compatibility

- Five existing primitives untouched. Pollers without `Warmer`
  bypass the new Step 3b case-runner hook via the type-assertion
  fall-through.
- The `Poller` interface is unchanged. `Warmer` is purely additive
  and optional.
- Existing 10 scenarios don't reference `nats_subscribe` — the new
  registered location is silently available; no scenario migration.
- `SandboxDeps` unchanged. `AdminConn` already exists; the new
  primitive reuses it.
- `## Failure Details` reporter unchanged — receives the standard
  `Result.Reason` payload via ROSM's existing surface.

## 10. Net change estimate

| Layer | Files | Lines (approx) |
|---|---|---|
| Reader | `internal/readers/nats_subscribe.go` (new) | ~110 |
| Poller | `internal/runtime/pollers/nats_subscribe.go` (new) | ~120 |
| Lifecycle hook | `internal/runtime/pollers/registry.go` (edit) | +15 |
| Case-runner Step 3b | `internal/runtime/case_runner.go` (edit) | +18 |
| Builtins registration | `internal/runtime/pollers/builtins.go` (edit) | +5 |
| Unit tests | `internal/runtime/pollers/nats_subscribe_test.go` (new) | ~270 |
| Integration tests | `internal/runtime/pollers/nats_subscribe_integration_test.go` (new, build-tagged) | ~120 |
| Demonstration scenario | `scenarios/drafts/broadcast-worker-fans-out-room-event.yaml` (new, Phase 4.5.1) | ~70 |

Total: ~728 lines added, **zero `pkg/model` changes, zero
`internal/matchers/` changes** (ROSM is reused unchanged).

## 11. Recommended landing path

1. **Land this spec** (commit + push).
2. **PR 2 — primitive**: Add the reader + poller + the case-runner
   Step 3b hook + the `Warmer` interface + the 14 unit tests + the
   integration tests. ~660 LOC.
3. **PR 3 — demonstration scenario**:
   `scenarios/drafts/broadcast-worker-fans-out-room-event.yaml`
   exercising the new primitive against the live broadcast-worker.
   ~70 LOC. Closes the loop with an 11th scenario that proves the
   primitive unblocks a real read-side test.

PR 2 is reviewable in isolation (no scenario changes, no behavioral
change for existing 10 drafts). PR 3 demonstrates the end-to-end win.

---

**Approval needed before any code lands.** Specifically:

- §3.2 the `Warmer` lifecycle hook (vs. eager pre-fire poller init).
- §2.3 the single-event-with-array-payload shape vs. multi-event
  streaming.
- §3.5 reuse `SandboxDeps.AdminConn` (vs. dedicated subscription
  connection).
- §4.5 buffer policy (256, drop on overflow).
- §8 the eight open questions — particularly Q2 (hard-fail on Warm
  errors) and Q4 (fixed buffer size).
- §11 PR sequencing (matcher first, demonstration second).
