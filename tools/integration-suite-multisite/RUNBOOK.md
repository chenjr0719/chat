# Integration Suite Multisite — Run & Teardown Runbook

## What it is

`tools/integration-suite-multisite/` is a black-box scenario suite
that runs the chat backend across a two-site NATS supercluster
federation and asserts behavior spanning both sites. `USE_INFRA=true`
is the only supported mode — the suite boots its own 26-container
stack via testcontainers. There is no "manual stack" mode.

---

## Prerequisites (once per machine)

```sh
# 1. A reachable Docker daemon (verify):
docker ps >/dev/null && echo OK

# 2. On the right branch:
git fetch origin && git checkout claude/integration-test-automation-LXQHP && git pull

# 3. Generate the NATS trust-chain files (operator/account keys,
#    backend.creds, .env). Required for the infra — the NATS and auth
#    containers mount these files. The multi-site infra also mounts
#    backend.creds into both NATS containers so site-b's leafnode
#    remote can authenticate into site-a.
cd docker-local && ./setup.sh && cd ..
```

---

## Build the service images (once, and after any service code change)

```sh
make build-test-images        # ~8 min cold; faster on incremental rebuilds
```

This tags `chat-local-services-<svc>:latest`, which the multisite
infra package consumes when booting the 18 service containers.

---

## Run the suite

`USE_INFRA=true` is the only supported mode. The runner boots its own
26-container stack and tears it down on exit.

```sh
time USE_INFRA=true make -C tools/integration-suite-multisite local
```

What happens:

1. `infra.Up` boots 26 containers (2 NATS, 2 Mongo, 2 Valkey,
   1 Cassandra, 1 Toxiproxy, 18 service replicas).
2. Runner waits for `INBOX_site-a` and `INBOX_site-b` to exist on
   their respective JetStream domains.
3. Runner applies federation Sources from `catalogs/federation.yaml`:
   `OUTBOX_site-a → INBOX_site-b` and `OUTBOX_site-b → INBOX_site-a`.
4. Runner walks `scenarios/` and runs each scenario.
5. Reports are written to `docs/integration-suite-multisite/`.
6. `TerminateAll` reaps the stack on clean exit.

Exit 0 = all scenarios pass. Exit 1 = at least one failure.

---

## Validate scenario YAML without booting infra

```sh
make -C tools/integration-suite-multisite validate
```

Runs the catalog validator and scenario loader against every YAML.
Catches loader errors (forbidden tokens, missing `site:`, unknown
effect flags) before any container is booted.

---

## Read the results

```sh
cat docs/integration-suite-multisite/last-run.md           # full report
cat docs/integration-suite-multisite/last-run-approved.md  # @status:approved only
```

Report contents:

- **Confusion matrix** — positive (through) vs negative (rejection),
  pass/fail breakdown.
- **Scenarios table** — per-scenario duration.
- **Failure Details** — exact Gomega mismatch per failing scenario.
- **performance.json** — latest/best/worst across runs.

`@status:approved` scenarios form the CI-gating score. Drafts are
informational.

---

## Teardown

Normally automatic — the run reaps its own stack on clean exit.
Manual safety net (use if a run was killed or Ryuk is disabled):

```sh
docker ps -aq | xargs -r docker rm -f     # remove leftover containers
docker network prune -f                   # remove orphaned networks
```

---

## Gotchas

| Gotcha | What to do |
|--------|------------|
| **RAM** | Steady state uses 6-8 GB; peak during boot (Cassandra + 18 services starting) can spike higher. Close other memory-heavy processes. On boxes with less than 12 GB free, the stack may OOM during boot. |
| **Cold boot time** | Cassandra is the long pole (~5-6 min on a cold machine; ~1-2 min when images are cached). The runner prints a "stack ready" message when all health checks pass. |
| **INBOX wait** | After services boot, the runner waits for `INBOX_site-a` and `INBOX_site-b` to exist. If `inbox-worker-site-{a,b}` fails to start, this wait will block. Check the service container logs. |
| **Federation is metadata-only** | Production federates ONLY metadata events (member_added/removed, subscription_*, room_renamed, role_updated) — not regular messages. Confirmed by inbox-worker/handler.go:52 and the `subject.Outbox(...)` callers in room-service / room-worker. A scenario whose fire is `chat.user.X.room.Y.<site>.msg.send` will produce a local canonical event but NO cross-site outbox traffic. To exercise the federation tail, fire a metadata-changing verb (room rename, member add/remove, subscription toggle). |
| **`OUTBOX_<site>` is not created by anyone** | No chat service bootstraps the OUTBOX stream; in production it's owned by ops/IaC. The harness intentionally does not auto-create it (see `ARCHITECTURE.md` §0 — "the tool's limits"). When production code tries to publish a cross-site metadata event, the firing service's log line reads `nats: no response from stream`. That is the finding, reported verbatim. What to do about it is outside the tool's scope. |
| **Ryuk reaper UX** | Testcontainers reaps failed containers ~1 second after a panic. The runner's error message may appear before the container logs are captured. Set `TESTCONTAINERS_REAPER_DISABLED=true` to keep failed containers alive for post-mortem inspection. |
| **Two infra unit tests are skipped** | `TestStartNATS_ReachableOnHostPort` and `TestStartToxiproxy_AdminReachableAndProxiesProvisioned` are `t.Skip`-ped. The gateway conf only mounts correctly with the full repo layout; the proxy-name assertions covered old single-site names. Both paths are covered by the smoke run. |
| **Scenarios are drafts** | New scenarios land in `scenarios/drafts/` (informational). Promotion to `scenarios/approved/` is a separate human-reviewed PR. |
| **Diagnosing service failures** | The report shows the assertion reason. For why a service errored, check its logs during the run: `docker logs -f room-service-site-a` (or `-site-b`). The stack is reaped on exit. |
| **MESSAGE_BUCKET_HOURS must match** | Cassandra seed rows and the reading service must use the same bucket window, or reads silently return nothing. The default is 72h. |
| **At-rest encryption disabled** | `pkg/atrest` defaults `ATREST_ENABLED=true` and requires `VAULT_ADDR` at boot. The multi-site stack doesn't run Vault, so `serviceEnv` sets `ATREST_ENABLED=false` for every service. If you wire Vault in for a follow-up, drop the override from `serviceEnv` common env. |
| **Exit-code interpretation under `tee`** | A piped `tee` masks `make`'s exit status — `echo "exit=$?"` after the pipe sees `tee`'s exit, not the runner's. Drop the `tee` for the one-glance check, or use `set -o pipefail`. |
| **`last-run.md` missing on infra failure** | If the stack fails to boot, the scenario walker never runs and `docs/integration-suite-multisite/last-run.md` is not written. Check `make`'s last lines for the panic site — it'll name the failing service. |
| **Silent hang at `waiting for stream`** | If the runner sits at `integration-suite: waiting for stream` for more than 30 s, the admin NATS connection couldn't authenticate against operator-mode NATS. `WaitForStream` has a 30 s deadline; it'll fail loud after that. If you see no error after 30 s, check the admin conn is being constructed with `nats.UserCredentials(docker-local/backend.creds)`. |

---

## One-glance "is it green?" check

```sh
# Note: don't pipe through `tee` here — it masks make's exit status.
time USE_INFRA=true make -C tools/integration-suite-multisite local
echo "exit=$?"

# last-run.md only exists if the stack booted; if absent, the run
# died at infra.Up — grep the make output for "panic" or "FAIL".
grep -E 'pass:|fail:' docs/integration-suite-multisite/last-run.md 2>/dev/null \
  || echo "no report — stack did not reach scenarios"

docker ps -aq | wc -l    # expect 0 — confirms clean teardown
```

---

## Related docs

- `tools/integration-suite-multisite/README.md` — overview, scenarios
  shipped, open concerns.
- `tools/integration-suite-multisite/ARCHITECTURE.md` — 26-container
  stack, NATS supercluster, federation Sources, Sandbox lifecycle,
  verb/reader primitive catalog.
- `tools/integration-suite-multisite/AUTHORING.md` — how to write
  a new multi-site scenario.
- `tools/integration-suite-multisite/SCENARIO-REFERENCE.md` — YAML
  grammar reference, substitution tokens, loader errors.
- `docs/integration-suite-multisite-findings.md` — durable log of
  findings the suite has surfaced (tool / app / ops). Consult when
  triaging a scenario failure: it may already be documented as a
  known operational gap that lives outside both the tool's and the
  app's scope.
