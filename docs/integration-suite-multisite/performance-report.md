# Performance report — message-gatekeeper / message-worker scenarios

Data source: `docs/integration-suite-multisite/performance.json` (accumulated
per-scenario latest/best/worst across runs) + stack-boot `elapsed_ms` from the
13 `USE_INFRA` runs this cycle. Durations are wall-clock per scenario as the
runner measured them; verdicts are the latest recorded.

## Headline

A scenario's runtime is almost entirely **assertion-shape cost**, not chat-app
latency:

```
scenario wall-clock ≈ ~150ms (real pipeline)  +  5000ms × (# of `not:true` / Consistently assertions)
```

The actual gatekeeper → MESSAGES_CANONICAL → worker → persist pipeline resolves
in **~100–215ms**. Everything above that is Gomega `Consistently(...)` holding
for its full 5s window to *prove absence* on a `not:true` assertion. The 26-
container stack boot is a fixed **~66.5s** paid once per run.

## Fixed cost — stack boot (once per run)

| Metric | Value |
|--------|-------|
| Runs sampled | 13 |
| Boot `elapsed_ms` range | 66,263 – 67,008 ms |
| Mean / spread | ~66.5 s, ±0.4 s (very stable) |
| Services | 18 replicas across the 2-site, 26-container stack |

Implication: boot dominates total runtime. Running N scenarios in **one** boot
(filtered `SCENARIOS_DIR`) amortizes the 66.5s; one-scenario-per-boot is
pathological.

## Per-scenario latency (latest recorded)

Bucketed by the dominant cost — the number of absence (`not:true` / `Consistently`)
windows in `expected[]`.

### Bucket A — all-positive assertions (~100–215 ms)
The real pipeline; every assertion is an `Eventually` that resolves on first hit.

| Scenario | Latest | ms |
|----------|--------|----|
| gatekeeper-large-room-bot-bypass | pass | 102 |
| gatekeeper-large-room-thread-reply-exempt | pass | 102 |
| message-worker-at-all-mention-persisted | pass | 104 |
| message-worker-mentions-persisted | pass | 104 |
| gatekeeper-whitespace-only-content-accepted | pass | 104 |
| gatekeeper-large-room-owner-bypass | pass | 108 |
| gatekeeper-quote-happy-path-embeds-snapshot | pass | 108 |
| thread-first-reply-happy-path | pass | 110 |
| message-pipeline-send-and-persist (baseline) | pass | 212 |

### Bucket B — one `not:true` window (~5.1–5.2 s)
One `Consistently(5s).ShouldNot(...)` (the "no canonical" / "no subscription" check).

| Scenario | Latest | ms |
|----------|--------|----|
| thread-reply-to-nonexistent-parent-creates-orphan | pass | 5,105 |
| gatekeeper-quote-cross-context-mismatch-rejected | pass | 5,212 |
| gatekeeper-large-room-member-blocked | pass | 5,213 |
| gatekeeper-thread-reply-missing-parent-createdat-rejected | pass | 5,213 |
| gatekeeper-empty-content-rejected | pass | 5,214 |
| gatekeeper-not-subscribed-rejected | pass | 5,214 |

### Bucket C — two absence windows (~10.1 s)
Two `Consistently` windows (e.g. no-reply + no-canonical, or no-canonical + no-row).

| Scenario | Latest | ms |
|----------|--------|----|
| gatekeeper-quote-nonexistent-parent-drops-message | pass | 10,103 |
| gatekeeper-malformed-requestid-silent-drop | pass | 10,113 |

### Cross-site (OUTBOX/federation-dependent)

| Scenario | Latest | ms | Note |
|----------|--------|----|------|
| thread-first-reply-remote-parent-federates-subscription | fail* | 10,115 | *Passed at **216 ms** in the targeted run (40ca) WITH `prep-outbox.sh` + the `createoutbox` tool present. The recorded fail is a full-drafts run where `OUTBOX_site-a` wasn't stood up → the OUTBOX assertion times out at its 10s ceiling. F-001 dependency. |
| cross-site-room-rename-federation | pass | 12,045 | federation tail (INBOX sourcing) — inherently slow + flaky |
| cross-site-message-federation | fail | 15,104 | 15s timeout on the federated arrival |
| room-create-federates-cross-site | fail | 5,003 | OUTBOX/federation dependency |

## Reading the verdicts (caveat)

`performance.json` is **accumulated across many runs** (some not from this
tester's targeted runs), so `worst: fail` entries are historical memory of
pre-fix states we already diagnosed and fixed:
- `gatekeeper-large-room-owner-bypass` worst=fail → the cache-contamination
  failure (run 649f), now pass after the unique-room fix (run 1982).
- `thread-first-reply-happy-path` worst=fail → the seed-gap setup errors
  (runs 148a/7c0f), now pass after commit 9a1ed260 (run d5f9).
- `gatekeeper-malformed-requestid-silent-drop` worst=fail → the logs_tail
  USE_INFRA gap, now pass after commit 5ee1a746.

All message-gatekeeper / message-worker scenarios are **green at latest**.

## Takeaways for suite runtime

1. **Boot dominates (~66.5s).** Batch scenarios per boot; never one-per-boot.
2. **`not:true` is the per-scenario cost driver**, at 5s each (the default
   `Consistently` window). Negative scenarios are 25–95× slower than positive
   ones purely because of absence-proving, not code. If suite runtime becomes a
   concern, the lever is the `not:true` timeout (a shorter Consistently window
   trades absence-confidence for speed) — a tool-config question, not a code one.
3. **The chat-app send pipeline is fast** — gatekeeper-validate → canonical →
   worker-persist lands in ~100–215 ms end-to-end in this stack, including the
   deep thread first-reply (room + 2 subscriptions + stamp + tcount) at 110 ms.
4. **Cross-site is the slow/flaky tail** (5–15s), gated on `OUTBOX` prep
   (F-001) and federation sourcing — assert the local OUTBOX publish (fast,
   reliable) rather than the INBOX arrival when the goal is the worker's
   behavior, not federation transport.
