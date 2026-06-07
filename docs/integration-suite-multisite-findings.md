# Integration suite multi-site — findings

Findings from the multi-site integration suite, addressed to the
**chat-app project team**. Each finding is a report — what we tested,
what we observed, where it lives in the system. Not a TODO for the
suite team.

Per-run reports under `docs/integration-suite-multisite/last-run.md`
are overwritten every run. Findings here are durable.

Format per finding:

```
F-NNN  <one-line title>
       Surfaced: <run / commit / probe>
       Layer:    <chat-app code | chat-app local-dev tooling | ops/IaC>
       Status:   <observed / mitigated in test tool / resolved>
```

---

## F-001 — multi-site JetStream federation needs leafnode transport and a SubjectTransform on the Source

**Surfaced:** Runs 36-58 of the multi-site smoke loop; standalone NATS
supercluster probes alongside Runs 46 and 58; cross-checked against
`pkg/stream/stream.go` `Inbox()` docstring and
`docs/superpowers/specs/2026-04-27-inbox-stream-ownership-design.md`.

**Layer:** Mixed — see the two gaps below. One is chat-app
local-dev tooling (`docker-local/setup.sh` + the test tool's
infra config). One was the integration suite's own federation
applier.

**Status:** **RESOLVED in the test tool (Run 71).** Leafnode
transport + SubjectTransform on the Source + Source-fetcher retry
tolerance (10059 "stream not found" responses are transient until
OUTBOX_<site> is created by `pre_fire_scripts`) carries the
federation tail end-to-end. Scenario
`cross-site-room-rename-federation.yaml` passes all 5 surfaces in
12.1s. The "design-space exhausted" framing under "Runs 68-70" was
wrong about the boot-clean state — it works; the previously
captured `state.sources[0].active = -1ns` was probed before the
first successful retry, not as a steady state.

Resolution on the chat-app side depends on whether production
federates over the same shape; that's documented under "Where the
chat-app team picks up" below.

### What we ran

Scenario `cross-site-room-rename-federation.yaml`:

```
   alice@site-a fires room.rename on a room whose subscribers include
   bob@site-b. We assert at five layers:

   Surface 1   reply: accepted                       ─┐
   Surface 2   site-a Mongo: rooms.name updated      ─┤  site-a
   Surface 3   ROOMS_site-a canonical event          ─┤  layer
   Surface 4   OUTBOX_site-a publish (envelope)      ─┘
   Surface 5   INBOX_site-b receives via federation  ──  peer-site
                                                          layer
```

### What we observed

Surfaces 1-4 pass cleanly. Surface 5 timed out at 15s with zero
events, regardless of JWT exports on the chatapp account.

A standalone probe of NATS state, taken while the stack was live and
the rename publish had completed:

```
   OUTBOX_site-a   (site-a's local JS, backend.creds):
       msgs       = 1
       firstSeq   = 1, lastSeq = 1
       → room-worker-site-a's publish ran clean.
         the chat-app federation publish path is healthy.

   INBOX_site-b    (site-b's local JS, backend.creds):
       msgs       = 0
       cfg.sources[0]:
           name    = OUTBOX_site-a
           filter  = outbox.site-a.to.site-b.>
           domain  = ""              ← stripped by NATS
       state.sources[0]:
           active  = -1ns            ← Source never activated
           lag     = 0
```

A side probe of the supercluster gateway path:

```
   Test                                                     Result
   ────────────────────────────────────────────────────     ──────
   Core NATS pub/sub across the supercluster gateway        PASS
   Core NATS request/reply across the gateway               PASS
   Cross-domain JS API (admin-on-A queries B's domain)      FAIL
       error: "nats: no responders available for request"
```

So Core NATS traversed the gateway, but `$JS.<peer>.API.*`
requests did not.

### The two gaps

After running through the hypotheses (Runs 46, 58), the failure
decomposed into two distinct gaps:

**Gap 1 — transport.** NATS supercluster **gateways do not carry
`$JS.<peer>.API.*` across clusters.** This is a structural property
of the gateway protocol, not a permissions issue: a JWT-level
`$JS.>` service export on the chatapp account (tested in Run 58) is
necessary-but-not-sufficient, because the gateway protocol itself
doesn't advertise JS-API responders cross-cluster.

The transport that does carry the JS API between clusters is
**NATS leafnodes**. Two NATS servers connected over a leaf link
expose their full account subject space, including the
`$JS.<domain>.API.*` subjects that JetStream Sources call out to
when pulling messages from a remote domain.

**Gap 2 — SubjectTransform missing on the integration suite's
federation Source.**

`pkg/stream/stream.go`'s `Inbox()` docstring is explicit (lines
64-69):

```
   chat.inbox.{siteID}.aggregate.>
     Federated events sourced from remote OUTBOX streams. These
     land here via a JetStream SubjectTransform that rewrites
     outbox.{remote}.to.{siteID}.> → chat.inbox.{siteID}.aggregate.>
     on the way into this stream.
```

And the inbox-worker consumer is bound to
`chat.inbox.{site}.aggregate.>`, not to `outbox.>`. The test tool's
`internal/infra/federation.go` `Apply` was configuring the Source
with `Name + FilterSubject + Domain` only, **without** the
`SubjectTransform`. Even with leafnode transport in place, the
inbox-worker consumer would never see the federated message,
because the source-delivered subject would still be `outbox.*`,
which falls outside the consumer's bind filter.

The chat-app design treats `Sources + SubjectTransforms` as
ops/IaC territory (see `docs/superpowers/specs/2026-04-27-
inbox-stream-ownership-design.md` — non-goal "Multi-site
federation in local dev"). The test tool standing in for ops/IaC
in local-dev MUST mirror the production federation shape — Source
**plus** SubjectTransform — not just the Source half.

### What we did in the test tool

`tools/integration-suite-multisite/internal/infra/nats.gateway.
{site-a,site-b}.conf` now use **leafnodes** instead of gateways
for cross-site transport:

- site-a runs a leafnode hub on `:7422`.
- site-b's `remotes:` block dials `nats-leaf://nats-site-a:7422`
  using `docker-local/backend.creds` (chatapp account user). The
  test tool mounts `backend.creds` into both NATS containers via
  `internal/infra/deps.go`.

`tools/integration-suite-multisite/internal/infra/federation.go`
`Apply` now attaches a `SubjectTransform` to each Source it
configures, matching the shape documented in `pkg/stream.Inbox`:

```go
SubjectTransforms: []jetstream.SubjectTransformConfig{{
    Source:      "outbox.site-a.to.site-b.>",     // = s.Filter
    Destination: "chat.inbox.site-b.aggregate.>", // = peer-INBOX subject
}}
```

These changes are infra-layer — the scenario YAML grammar, reader
and verb primitives, sandbox model, and runner flow are
**unchanged**.

### Follow-up — Run 59: leafnode handshake init race

The leafnode topology landed conceptually correct, but the suite
never reached scenarios:

```
   panic: infra.Up: services (phase A): start room-worker-site-b:
          container exited with code 1

   room-worker-{site-a,site-b}, same wall-clock second:
     INFO  connected to MongoDB
     ERROR bootstrap streams failed
           error="create ROOMS stream: nats: API error: code=503
                  err_code=10008 description=JetStream system
                  temporarily unavailable"
     → exits 1
```

NATS prints `Server is ready` BEFORE the leafnode handshake
completes. During that window, JetStream replies 503
`temporarily unavailable` to stream operations that touch
cross-domain routing. The testcontainers wait strategy in
`deps.go` keyed off `Server is ready`, so Phase A services
(`room-worker × 2`, which set `BOOTSTRAP_STREAMS=true` in dev)
booted into that window and exited 1 deterministically.

**Fix in the test tool (this commit):**
`internal/infra/stack.go` now runs `waitForJetStreamReady`
between deps boot (Step 3) and Phase A service start. It opens a
per-site credentialed admin conn and polls `js.AccountInfo` on
each site's local domain until the call stops returning 503,
bounded by a 15s timeout. Same conn pattern as
`waitForRoomsStreams`, just earlier in the lifecycle and checking
503-vs-not-503 rather than stream-exists.

This is tool-soundness territory — sequencing of the boot phases.
Any future scenario that fires a service with
`BOOTSTRAP_STREAMS=true` against the leafnode-connected stack
would have hit the same race; the wait unblocks all of them.

### Follow-up — Runs 60-61: `cluster:` stanza forced clustered JS

The Phase A wait fired correctly but never observed JS reach
ready. Both NATS containers logged the same pattern indefinitely:

```
   [INF] Starting JetStream cluster
   [INF] Creating JetStream metadata controller
   [INF] JetStream cluster bootstrapping
   [INF] JetStream using domains: local "site-a", remote "site-b"
   [WRN] JetStream has not established contact with a meta leader
   [INF] JetStream cluster no metadata leader   ← every ~20s, forever
```

The `JetStream using domains` line confirmed the leaf link was
connecting (each side saw the peer's domain), but the
`metadata leader` election never converged. Single-node clusters
can't elect a meta-leader, so AccountInfo returned 503 forever.

The cause was a leftover from an earlier topology era: each conf
still carried

```
   cluster: {
     name: site-X
     listen: 0.0.0.0:6222
     routes: [nats://nats-site-X:6222]   ← self-route
   }
```

That `cluster:` block was originally added to give the `$SYS`
account a cluster transport when running `gateway:` + `jetstream:`
together (a gateway-era requirement). With the transport switched
to leafnodes, the `$SYS`-via-cluster requirement is gone — but the
side effect (clustered JS demanding leader election) remained.

**Fix in the test tool (this commit):**
Stripped the `cluster: {…}` block from both
`internal/infra/nats.gateway.site-{a,b}.conf`. JetStream now runs
standalone on each node. The leaf link continues to carry inter-
site traffic as designed; no `$SYS` warnings reappeared.

### Follow-up — Run 65: NATS 10137 on the Source consumer

Stack booted in 67s, all streams ready, federation `Apply` ran
clean, no boot races. A standalone probe confirmed cross-domain
`$JS.>` traversal works over the leaf link (`A→B INBOX state` and
`B→A OUTBOX state` queries both returned correct counts). But
INBOX_site-b still received zero messages.

Both NATS containers logged, every Source retry tick (~13s):

```
   [WRN] JetStream error response for stream INBOX_site-b
         create source consumer OUTBOX_site-a:
         consumer with multiple subject filters
         cannot use subject based API (10137)
```

NATS counted `StreamSource.FilterSubject` and
`SubjectTransforms[0].Source` as two filters on the cross-cluster
Source consumer, even though both carried the same pattern
(`outbox.site-a.to.site-b.>`). Cross-cluster Source consumers use
the legacy subject-routed API, which permits only one filter.

The two "options" the tester proposed:
- **(A)** Drop `FilterSubject`, let the SubjectTransform's `Source`
  field act as both filter and rewrite.
- **(B)** Drop the SubjectTransform, keep `FilterSubject` alone.

**Option B was rejected.** `inbox-worker`'s consumer is bound to
`subject.InboxAggregateAll(siteID)` = `chat.inbox.{site}.aggregate.>`
(see `inbox-worker/main.go:394`), and `INBOX_{site}`'s declared
subjects in `pkg/stream.Inbox` are
`[chat.inbox.{site}.*, chat.inbox.{site}.aggregate.>]`. Without
the transform, federated messages would arrive under the
`outbox.>` namespace, get rejected by the stream's subject filter,
and even if they landed they'd be invisible to the
`aggregate.>`-bound consumer. The transform is what bridges
publisher subject space to consumer subject space; dropping it
breaks the chat-app's documented federation contract.

**Fix in the test tool (this commit):**
Option A — `internal/infra/federation.go` `Apply` no longer sets
`StreamSource.FilterSubject`. The `SubjectTransform.Source` field
now both filters (NATS only pulls matching subjects) and rewrites.
Same Source/Destination pair as before; one duplicate filter
removed.

### Follow-up — Runs 68-70: Source 10059, design space exhausted

After every fix landed cleanly through Run 67, Surface 5 still
showed `state.sources[0].active = -1ns` and INBOX_site-b.msgs = 0.
The next two runs explored the remaining NATS-topology design
space; the matrix is now exhausted within the test tool's scope.

#### Run 68 — standalone JS, Source returns 10059

The 10137 cleared. Source state on INBOX_site-b shows the config
landed correctly:

```
   cfg.sources[0]:
     name=OUTBOX_site-a
     external.api="$JS.site-a.API"   ← Domain→External mapping correct
     external.deliver=""
     transform[0]:
       src="outbox.site-a.to.site-b.>"
       dest="chat.inbox.site-b.aggregate.>"
   state.sources[0]:
     active=-1ns                     ← never activated
     lag=0
   INBOX_site-b.msgs = 0
```

But every Source retry tick (~13s), both NATS containers log:

```
   [WRN] JetStream error response for stream INBOX_site-b
         create source consumer OUTBOX_site-a:
         stream not found (10059)
```

Two paths diverge:

- **Client probe via leaf** —
  `NewWithDomain(ncB, "site-a").Stream("OUTBOX_site-a").Info()` →
  `msgs=1`. The `$JS.site-a.API.STREAM.INFO.OUTBOX_site-a` request
  reaches NATS-A and returns correctly.
- **Source-fetcher's internal CONSUMER.CREATE** — same prefix,
  but returns 10059 instead.

The most plausible explanation: standalone JetStream (no `cluster:`
block at all, after Run 60-61's strip) skips JS meta-leader
activation. The meta-leader is what publishes cross-domain
interest in `$JS.<peer>.API.>` over the leafnode link.
Client requests work because they target subjects with active
interest from NATS-A's chatapp JS subscription, propagated via
the leaf user account scope. Source-fetcher's internal
consumer-create uses a server-internal path that doesn't pick up
the same interest unless JS meta is active.

Run 60-61's hang (`no metadata leader` forever) was caused not by
clustering itself but by the **self-route** in the cluster block,
which made NATS wait for a peer that never came. A cluster block
of size 1 with NO routes elects itself instantly (quorum = 1,
votes = 1), giving meta-leader activation without hang.

#### Run 70 — single-node cluster, no routes → boot FTL

Hypothesis for Run 68 was that standalone JS skips JS meta-leader
activation and the meta-leader is what propagates cross-domain
interest over the leaf. Tried: re-add `cluster:` block, size 1,
no routes. Both NATS containers refused to start:

```
   [INF] Starting JetStream cluster
   [FTL] Can't start JetStream:
         JetStream cluster requires configured routes or solicited
         leafnode for the system account
```

NATS in cluster mode requires `$SYS` routing via EITHER configured
`routes:` OR a solicited leafnode connection that serves the
system account. The hub side has no leafnode remote at all. The
spoke side has a remote, but it solicits for the **chatapp**
account (via `backend.creds`), not the system account. Neither
side satisfies the check, both FTL at boot.

#### Design-space matrix (empirically exhausted)

| Topology                              | Boots | Sources deliver |
|---------------------------------------|-------|------------------|
| no `cluster:` block                   | yes   | no — Source returns 10059, server-internal path can't reach peer $JS API |
| `cluster:` with `routes:` self-route  | yes   | no — meta-leader election hangs forever waiting for the "peer" that's itself |
| `cluster:` with no routes             | no    | n/a — FTL on boot, no $SYS leaf for cluster mode |

All three states have been verified empirically across Runs 60-70.
Two boot, neither delivers. None of the three permit Surface 5 to
exercise on the test tool's own configuration alone.

#### What the test tool has done (this commit)

Reverted to the boot-clean state (no `cluster:` block on either
side). Surfaces 1-4 continue to pass cleanly. Surface 5 remains a
known gap and the scenario YAML's Surface 5 assertion stays in
place as the failure marker.

The test tool stops iterating on this. Further movement requires
chat-app-side input on production topology.

#### Where the chat-app team picks up (concrete options)

Three directions surfaced through the iteration. The chat-app
team picks whichever matches the production federation plan:

1. **Bridge `$SYS` across the leaf via a second leafnode
   connection.** The spoke (site-b) adds a second `remotes:`
   entry authenticating with system-account credentials, giving
   the cluster-mode startup the "solicited leafnode for the
   system account" it needs. Requires generating a system-account
   user via `nsc` in `docker-local/setup.sh` (currently only the
   chatapp `backend` user is generated). This is the path that
   most closely matches a production NATS leafnode-with-JS
   topology.

2. **Hub-and-spoke with one site as the JS-meta hub.** Promote
   one site (say site-a) to a clustered JS hub with explicit
   `routes:` pointing at a second hub-cluster member (a third
   NATS container dedicated to forming a 2-node cluster with
   site-a). The spoke (site-b) stays single-node. JetStream
   cross-domain Sources from a clustered hub to a single-node
   leaf is the documented NATS pattern. Significantly more
   container budget (a 27th container) but matches NATS docs
   exactly.

3. **Single NATS, two JS domains.** Drop the per-site NATS
   containers; one NATS server hosts both `site-a` and `site-b`
   as separate JetStream domains on the same server. Eliminates
   the cross-cluster question entirely. Loses the cross-cluster
   test surface (so e.g. network partitions between sites are
   no longer expressible), but Surfaces 1-5 all become testable.

The chat-app team's production federation plan decides which.
Until then, this finding stays at `observed`, the test tool ships
the boot-clean two-site stack, and scenarios assert Surfaces 1-4
cleanly.

#### Run 71 — green end-to-end

The "design space exhausted" conclusion under Runs 68-70 was
itself a measurement artifact. The boot-clean topology (no
`cluster:` block, leafnode transport, SubjectTransform on the
Source, FilterSubject dropped) does deliver Surface 5 — provided
OUTBOX_<site> exists by the time the Source-fetcher's retry tick
comes around.

The catch: OUTBOX_<site> isn't created by any chat-app service
(`RUNBOOK.md` gotcha "OUTBOX_<site> is not created by anyone").
At federation `Apply` time the source-fetcher's first CONSUMER.
CREATE returns 10059 because OUTBOX doesn't yet exist. The
fetcher retries every ~13s. `pre_fire_scripts` (per-scenario,
operator-owned) stand up OUTBOX before the fire; the next retry
after that succeeds and the Source activates.

Earlier-run probes that captured `state.sources[0].active = -1ns`
and `msgs = 0` had snapshotted that pre-OUTBOX window, then
walked away before the retry that would have flipped it. The
matrix in Run 70 was technically correct about the FTL corner
(`cluster:` with no routes) but wrong about the boot-clean
corner's steady state — it works, eventually, deterministically.

Final scenario timings on Run 71:

```
   infra-sanity-rooms-pipeline-site-a       206ms   ✓
   infra-sanity-rooms-pipeline-site-b       207ms   ✓
   cross-site-seed-visibility               122ms   ✓
   cross-site-room-rename-federation        12.1s   ✓   (Surface 5 in 15s budget)
```

All 5 surfaces of `cross-site-room-rename-federation` pass:
reply, site-a Mongo update, ROOMS_site-a canonical event,
OUTBOX_site-a envelope, INBOX_site-b federation tail.

#### What chat-app picks up (revised, narrower)

With Run 71 green, the picks-up list collapses:

1. **Decide whether production federates over the same leafnode
   + SubjectTransform shape.** If yes, mirror the test tool's
   `internal/infra/nats.gateway.*.conf` and federation `Apply`
   logic in `docker-local/setup.sh` (multi-site flag) and in the
   IaC that stands up production NATS clusters. The chat-app's
   own `pkg/stream/stream.go` `Inbox()` docstring already
   documents the SubjectTransform shape — the test tool's
   federation `Apply` is now an executable reference for what
   the production Sources should look like.

2. **Decide where OUTBOX_<site> is created in production.**
   `RUNBOOK.md` records that no chat-app service bootstraps it.
   In the test tool this is operator-owned via `pre_fire_scripts`.
   Production needs a documented owner: IaC at deploy time, an
   ops-owned bootstrap container, or (less likely) a designated
   chat-app service whose responsibility is OUTBOX schema
   ownership. Until OUTBOX exists, every Source pulling from it
   will log 10059 retries indefinitely — fine if transient,
   noise if not.

When both are decided and reflected in `docker-local/setup.sh`,
this entry can flip from "RESOLVED in the test tool" to
"RESOLVED end-to-end".

### Adjacent fix retained — Surface 5 subject

The scenario `cross-site-room-rename-federation.yaml` Surface 5
was filtering on the pre-transform subject
(`outbox.site-a.to.site-b.room_renamed`). Once the
SubjectTransform writes the message into INBOX_site-b as
`chat.inbox.site-b.aggregate.room_renamed`, the original filter
would never match — even if the Source delivered cleanly. The
Surface 5 assertion has been corrected to filter on the
post-transform subject, matching what inbox-worker's consumer
actually binds to. This stays in place across the revert; it's
correct in any of the three resolution paths above.

### Where the chat-app team picks up (summary)

The full picks-up list is enumerated in "Concrete options" under
"Runs 68-70" above. The short version:

1. Decide which production topology direction the chat-app team
   commits to (single-leaf with `$SYS` bridge, hub-and-spoke
   with a 2-node JS hub, or single-NATS-with-multiple-domains).
2. Update `docker-local/setup.sh` (or sibling) and the test
   tool's `internal/infra/nats.gateway.*.conf` to match.
3. When Surface 5 delivers end-to-end against the new topology,
   this finding flips to `resolved`.

This finding is a report on what the test tool needed to mirror
production federation in local-dev. The chat-app team owns the
production federation topology decision; the questions to answer:

1. **Does production federate cross-cluster via leafnodes?** If
   yes, this finding flips to `resolved` once that's confirmed and
   `docker-local/setup.sh` (or a sibling) grows multi-site support
   that mirrors the production topology.

2. **Does production rely on Sources carrying the
   SubjectTransform, or some other mechanism (e.g. an ingress
   subject-mapping at the cluster level)?** The `pkg/stream.Inbox`
   docstring documents the per-Source transform, which is what the
   test tool now mirrors. If production uses a different shape,
   the docstring and the test tool both need to update to match.

3. The earlier `setup-jwt-supercluster.sh` script + `make
   setup-jwt` target + `ARCHITECTURE.md` §0 "developer-audience
   setup-time prep" exception have been removed from the test
   tool. Run 58 proved the JWT-export hypothesis was not the right
   diagnosis; the transport choice was. Leaving deprecated
   scaffolding around would only mislead the next person reading
   the finding.

---
