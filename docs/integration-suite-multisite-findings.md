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

**Status:** Open. Requires ops/IaC work outside this suite's scope.

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

When the JWT change ships, no scenario YAML and no harness code
needs to change. The same federation scenario will reach Surface 5
green on the next run.

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
