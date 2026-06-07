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

**Status:** Mitigated in the test tool (leafnode transport in
`internal/infra/nats.gateway.*.conf`, SubjectTransform in
`internal/infra/federation.go`). Resolution on the chat-app side
depends on whether production federates over leafnodes or some
equivalent that carries the JS API across sites.

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

### Where the chat-app team picks up

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
