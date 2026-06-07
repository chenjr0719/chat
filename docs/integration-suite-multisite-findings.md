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
- self-verifying (restores backups if post-mutation check fails)
- best-effort (the exact nsc export shape needed for NATS to retain
  the `Source.Domain` field is the only piece that isn't proven;
  the rest of the script is mechanical)

### Recommendation

1. Treat `setup-jwt-supercluster.sh` as a test-tool stop-gap that
   exists because we can't ship updates to `docker-local/setup.sh`
   without the chat-app project's review.

2. Move the equivalent logic into `docker-local/setup.sh` (or a
   sibling, like `docker-local/setup-multisite.sh`) at your project's
   convenience. When you do, the test tool's script becomes dead
   code — delete it and the `setup-jwt` make target, and remove the
   "developer-audience setup-time prep" exception from the test
   tool's `ARCHITECTURE.md` §0.

3. The exact `nsc add export` invocation we wrote is the best-effort
   from a test-tool author without hands-on nsc expertise. If the
   first run of the federation scenario after `make setup-jwt` still
   shows `INBOX_site-b.cfg.sources[0].domain = ""`, the export shape
   needs refinement — that's worth one or two of your SRE/platform
   team members' time to verify directly.

---
