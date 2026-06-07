# Integration suite multi-site — findings

Persistent log of findings the multi-site suite has surfaced, with
disambiguation: tool bug, app bug, or operational/IaC concern. Each
finding cites the run that revealed it and the layer it lives in.

This doc is the long-lived counterpart to per-run reports under
`docs/integration-suite-multisite/last-run.md`, which are overwritten
on every run. Findings here are durable — they outlive any single
run and document what the suite has learned about the system.

> **What goes here:** durable diagnoses produced by the suite or by
> targeted operator probes alongside a run. **What does NOT go here:**
> per-run pass/fail rows (those are in `last-run.md`), tool bug
> reports being actively fixed (those are commit messages), or
> hypotheses without evidence.

Format per finding:

```
F-NNN  <one-line title>
       Surfaced: <run / commit / probe>
       Layer:    <tool | app | ops>
       Status:   <open | fix planned elsewhere | accepted>
```

---

## F-001 — JetStream Source delivery blocked by operator JWT exports

**Surfaced:** Runs 36, 41, 43, 46 of the multi-site smoke loop;
confirmed definitively by the supercluster probe run alongside
Run 46.

**Layer:** Ops / IaC. Not a tool bug, not a chat-app bug.

**Status:** Mitigation in progress (`setup-jwt-supercluster.sh`
framework landed; nsc body pending empirical work). The fix's
eventual home is the chat-app's `docker-local/setup.sh` —
`setup-jwt-supercluster.sh` is the test-tool-side stop-gap allowed by
`ARCHITECTURE.md` §0's "developer-audience setup-time prep" exception
(the only exception named in the limits doc).

### Symptom

`cross-site-room-rename-federation.yaml` Surface 5
(`jetstream_consume site=site-b stream=INBOX_site-b`) times out with
0 events polled — even though Surface 4 (`OUTBOX_site-a`) passes
cleanly and `OUTBOX_site-a` carries the rename event with the
correct envelope shape.

```
   Surface 4   OUTBOX_site-a publish      ✓ msgs=1, envelope OK
   Surface 5   INBOX_site-b federation    ✗ 0 events in 15s
```

### Probe results

A standalone probe run during the same boot of the stack:

```
   Test                                                     Result
   ────────────────────────────────────────────────────     ──────
   Core NATS pub/sub across the supercluster gateway        PASS
   Core NATS request/reply across the gateway               PASS
   Cross-domain JS API (admin-on-A queries B's domain)      FAIL
       error: "nats: no responders available for request"
   INBOX_site-b Source state (read locally on NATS-B)
       msgs       = 0
       sources[0] = { name: OUTBOX_site-a,
                       filter: outbox.site-a.to.site-b.>,
                       domain: ""        ← empty (set to "site-a" at Apply)
                       active: -1ns      ← never active
                     }
   OUTBOX_site-a state (read locally on NATS-A)
       msgs       = 1
       firstSeq   = 1                    ← rename event IS there
```

### Diagnosis

The supercluster gateway works fine for Core NATS subjects (Tests 1
and 2 confirm). What does NOT cross the gateway is the
`$JS.<domain>.API.>` subject pattern that JetStream Sources use to
fetch from a peer stream — Test 3 returns "no responders."

The configured Source on `INBOX_site-b` carries an empty `domain`
field at runtime even though `internal/infra/federation.go`'s
`Apply` sets `Domain: peerDomain(s.On) = "site-a"` at creation. NATS
strips the field because it cannot validate the peer through the
operator JWT — the chatapp account in `docker-local/setup.sh`'s
generated trust chain does not have a corresponding `$JS.>` export
on the publishing site or import on the consuming site.

### Why this is ops / IaC

Fixing the gap requires regenerating the operator + account JWTs in
`docker-local/setup.sh` with `JSAccountClaims` declaring the
`$JS.>` exports/imports between sites. That is an operator-key
rotation, not a tool change and not an app change.

Per `tools/integration-suite-multisite/ARCHITECTURE.md` §0 "the
tool's limits", the harness reports this gap verbatim through the
federation scenario's localised Surface 5 failure and stops. It
does NOT auto-patch the trust chain, does NOT skip the assertion,
and does NOT provide a recovery procedure.

### What unblocks this

When the JWT change ships (either via `setup-jwt-supercluster.sh`
once its nsc body is finalised, or via `docker-local/setup.sh` once
the chat-app project grows multi-site support), no scenario YAML
and no harness code needs to change. The same federation scenario
will reach Surface 5 green on the next run.

### Mitigation in this repo

`tools/integration-suite-multisite/setup-jwt-supercluster.sh` is the
test-tool-side mitigation. It is:

- **Framework complete:** idempotency check (parses the chatapp
  account JWT and looks for the required exports), pre-mutation
  backup of `nats.conf` + `backend.creds` with timestamped suffix,
  post-mutation re-verification with backup restoration on failure.
- **nsc body pending:** the actual nsc invocations are marked with
  a `TODO(suite-multisite)` block. The script fails loudly with a
  pointer to this finding when run before the body is filled in —
  intentional, because shipping a half-correct nsc mutation would
  create a subtly broken trust chain that's worse than the clear
  Surface 5 failure we currently report.

Invocation: `make -C tools/integration-suite-multisite setup-jwt`.
Once per machine. Re-running on an already-correct JWT is a no-op.

### Verification target for the nsc body work

Run 54 (with `prep-outbox.sh` already provisioning the OUTBOX streams
via `pre_fire_scripts`, the JWT untouched) gave the nsc body author
a binary pass criterion. After the nsc invocations land in
`setup-jwt-supercluster.sh` and the operator runs it, a fresh
federation-scenario run must produce ALL of the following:

```
   federation scenario Surface 5 passes within 15s timeout
   
   post-scenario probe of INBOX_site-b (via backend.creds on NATS-B):
     cfg.sources[0].domain    = "site-a"     (currently "")
     state.sources[0].active  = > 0          (currently -1ns)
     state.sources[0].lag     ≥ 0            (Source actively pulling)
     msgs                     > 0            (event delivered)
   
   OUTBOX_site-a continues to show msgs=1 firstSeq=1 (unchanged)
```

If `cfg.sources[0].domain` still nulls after the JWT mutation, the
export/import shape is wrong — NATS only retains the Domain field
when the account JWT permits validation of the peer.

Reproducer for an nsc body developer:

```
   1. Build the createoutbox probe at /tmp/create-outbox (see
      tester's run notes for the source — uses
      jetstream.NewWithDomain to inspect streams locally).
   2. Wire pre_fire_scripts: [prep-outbox.sh] in
      scenarios/drafts/cross-site-room-rename-federation.yaml
      (uncommitted operator hook).
   3. USE_INFRA=true make -C tools/integration-suite-multisite local
   4. Probe both NATSes after "stack ready":
        OUTBOX_site-a → msgs > 0 ✓ (production publish path works)
        INBOX_site-b  → msgs = 0, domain = "" (current state)
   5. Implement the nsc body in setup-jwt-supercluster.sh.
   6. make -C tools/integration-suite-multisite setup-jwt
   7. Re-run from step 3. Probe must show INBOX_site-b's Source
      with domain = "site-a", state.active > 0, msgs > 0.
```

Out of scope for the nsc body:

- Surfaces 1–4 are already green; the JWT change doesn't need to
  re-prove them.
- `prep-outbox.sh` continues to provision the OUTBOX streams. That is
  a separate operator concern (stream creation, not JWT scope).

### Why this exception is named explicitly in the limits doc

`ARCHITECTURE.md` §0 lists exactly one exception to "the tool does
not fill ops/infra gaps": developer-audience setup-time prep, with
four criteria that all must hold. F-001 is the case the exception
was written for; the criteria match exactly:

1. Fix is unambiguously infra/SRE — operator-key custody is a
   separate role from chat-app development.
2. Gap is pre-boot — proven empirically (`pre_fire` cannot reach
   the trust chain; NATS strips the Source.Domain field silently
   because the JWT doesn't allow validation).
3. Gap blocks developer verification — the federation code in
   `room-worker` cannot be confirmed by tests without the fix.
4. No alternative home in the developer's PR cycle — filing a
   chat-app PR for `docker-local/setup.sh` is out of the
   developer's iteration loop.

The script's existence and the exception's existence are
load-bearing on each other. If the chat-app project ships
multi-site support in `docker-local/setup.sh`, both should be
removed — the exception expires, the script becomes dead code.

### What scenarios will hit the same gap

Every cross-site metadata-federation event uses the same
`OUTBOX_<site> → INBOX_<peer>` Sources path:

```
   member_added         room-worker
   member_removed       room-worker
   room_renamed         room-worker      (this finding's scenario)
   role_updated         room-service
   subscription_read    room-service
   subscription_mute    room-service
   subscription_fav     room-service
   thread_read          room-service
   room_restricted      room-service
   thread_sub_upserted  message-worker
```

Any scenario testing any of those will fail at the federation tail
with the same localisation. Adding more cross-site scenarios is
useful work (broader coverage of what producers emit) but they will
ALL report the same downstream finding until the JWT is fixed.

---
