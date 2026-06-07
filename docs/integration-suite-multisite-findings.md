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

## F-001 — multi-site JetStream federation needs JS API exports on the chatapp account

**Surfaced:** Runs 36-54 of the multi-site smoke loop; confirmed by
standalone NATS supercluster probe alongside Run 46.

**Layer:** chat-app **local-dev tooling** (`docker-local/setup.sh`).
The chat-app code itself is correct.

**Status:** Mitigated in test tool via
`tools/integration-suite-multisite/setup-jwt-supercluster.sh`
(best-effort; see "Recommendation" below).

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

Surfaces 1-4 pass cleanly. Surface 5 times out at 15s with zero events.

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
       → the federation Source was configured with Domain="site-a"
         (via UpdateStream in internal/infra/federation.go), but NATS
         silently nulled it because the chatapp account JWT does not
         declare any cross-domain JS API subjects as routable.
```

Additionally, the probe confirmed the supercluster gateway itself
works fine for Core NATS subjects:

```
   Test                                                     Result
   ────────────────────────────────────────────────────     ──────
   Core NATS pub/sub across the supercluster gateway        PASS
   Core NATS request/reply across the gateway               PASS
   Cross-domain JS API (admin-on-A queries B's domain)      FAIL
       error: "nats: no responders available for request"
```

So the failure is specifically: **NATS will not forward
`$JS.<peer-domain>.API.*` requests across the supercluster gateway
without account-level scope that declares those subjects as
exports.**

### What this tells the chat-app team

Your code is correct.

- `pkg/stream/stream.go` Outbox subjects: correct.
- `pkg/subject.Outbox(site, dest, type)`: correct.
- `room-worker/handler.go` cross-site member detection and
  `outbox.*.to.*.*` publish: correct.
- `room-service/handler.go` rename handler: correct.
- `federation.yaml`: declares Sources correctly.

Production deployed against an SRE-provisioned trust chain with the
right exports would work as designed. The gap is between your
production-deployment assumption and your local-dev mirror's trust
chain.

### Where the fix belongs in your project

**`docker-local/setup.sh`** is the local-dev mirror of your trust
chain. It currently generates a single-site-shaped operator JWT —
appropriate when the chat app was single-site, no longer sufficient
now that the project supports multi-site federation.

Extending `docker-local/setup.sh` to optionally generate a
multi-site-capable trust chain would resolve F-001 and let any
developer iterating on multi-site federation features verify their
work locally. Shape:

```
   docker-local/setup.sh                        (existing — single-site)
   docker-local/setup.sh --multi-site           (proposed)
       does what setup.sh does today, plus:
       adds a service export on the chatapp account permitting
       cross-cluster $JS.> subjects to be routed through the
       gateway. uses nsc:
       
         nsc add export --account chatapp --service \
             --subject "\$JS.>" \
             --name "CrossDomainJSAPI" \
             --response-type stream
       
       (exact export shape may need iteration — the test tool's
        setup-jwt-supercluster.sh is the best-effort starting point.)
```

### What we did in the test tool meanwhile

`tools/integration-suite-multisite/setup-jwt-supercluster.sh` is the
test-tool-side stop-gap (sanctioned by the test tool's
`ARCHITECTURE.md` §0 "developer-audience setup-time prep" exception,
which has four criteria; all four are satisfied here).

```
   make -C tools/integration-suite-multisite setup-jwt
```

The script regenerates the trust chain with a `$JS.>` service export
on the chatapp account. It is:

- idempotent (no-op on already-extended trust chain)
- self-backing-up (`nats.conf.bak-<timestamp>`,
  `backend.creds.bak-<timestamp>`)
- self-verifying (the JWT-mutation half — restores backups if the
  export doesn't land in the JWT)
- empirically validated only at the JWT-content level (see below)

### Empirical result — Run 58: export is necessary but NOT sufficient

After running `make setup-jwt` and restarting the stack, the
federation Source state probe shows **no change**:

```
                       before make setup-jwt    after make setup-jwt
   ─────────────────   ──────────────────────   ──────────────────────
   cfg.sources[0].domain     ""                       ""
   state.sources[0].active  -1ns                     -1ns
   INBOX_site-b msgs         0                        0
   OUTBOX_site-a msgs        1 (publish works)        1 (publish works)
```

The `$JS.>` service export landed in the chatapp account JWT
correctly. NATS still strips `Source.Domain` at runtime. The
account-level service export alone does not unlock cross-domain JS
API delivery in this trust-chain topology.

This is a precise, useful empirical result: the export is in the
class of changes the trust chain needs, but it isn't the complete
shape. There are at least two directions the chat-app team's
SRE/platform people could investigate next:

**Possibility A — `$JS.*` responders live on the system account.**

JetStream's API subjects are conventionally serviced by the system
account (`$SYS`), not the per-application account. Even with the
chatapp account permitted to publish to `$JS.>`, the responder side
of the request/reply may live on `$SYS` and the gateway may not be
advertising the route to it. Worth checking:

- whether the sys account needs a cross-cluster export/import
- whether the gateway block in `nats.conf` needs an explicit
  `system_account` directive or `jetstream` permissions
- whether `nats server check jetstream` reports cross-domain
  reachability after the chatapp export lands

**Possibility B — topology choice.**

The operator/JWT + supercluster-gateway + single-account-spanning-
both-clusters shape may simply not support cross-domain JetStream
Sources cleanly. Some NATS deployments use:

- **Leafnodes** instead of gateway peers, with explicit JetStream
  account import on the leaf side
- **Single JS domain** spanning both clusters (drop the
  `domain: site-a`/`site-b` distinction in the nats.gateway.*.conf
  files), so there's no "cross-domain" call to fail in the first
  place

Both are larger changes than another JWT tweak. They're production
shape decisions, not local-dev tooling tweaks.

### Where the chat-app team picks up

The test tool has reached the end of what a shell script can answer.
The next iteration belongs to whoever owns the chat-app project's
multi-site production deployment plan:

1. Decide which of Possibilities A / B is the production topology
   you're committing to.
2. Update `docker-local/setup.sh` (or its sibling) to match.
3. When the local-dev trust chain delivers the federation
   end-to-end, this entry's status flips to "resolved" and the
   test-tool-side `setup-jwt-supercluster.sh` becomes dead code
   (delete it, the `setup-jwt` make target, and the
   `ARCHITECTURE.md` §0 exception).

---
