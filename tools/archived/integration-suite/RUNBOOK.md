# Integration Suite — Run & Teardown Runbook

## What it is

`tools/integration-suite/` is a black-box scenario suite that runs
the assembled chat system and asserts behavior. With `USE_INFRA=true`
it boots its own containerized stack (NATS, Mongo, Cassandra, Valkey,
toxiproxy + the microservices) via testcontainers — you don't manage
infra yourself.

## Prerequisites (once per machine)

```sh
# 1. A reachable Docker daemon (verify):
docker ps >/dev/null && echo OK

# 2. On the right branch:
git fetch origin && git checkout claude/integration-test-automation-LXQHP && git pull

# 3. Generate the NATS trust-chain files (operator/account keys,
#    backend.creds, .env). Required even for USE_INFRA — the infra
#    mounts these into the NATS + auth containers.
cd docker-local && ./setup.sh && cd ..
```

## Build the service images (once, and after any service code change)

```sh
make build-test-images        # ~8 min cold; faster on incremental rebuilds
```

This tags `chat-local-services-<svc>:latest`, which `infra.Up` consumes.

## Run the suite (the main loop)

Two stack modes — pick based on whether you want the suite to manage
its own infrastructure or use an already-running manual stack:

```sh
# Manual stack — assumes `make deps-up && make up` is already running.
# Fastest startup; lets you tail container logs and inspect Mongo/Cassandra
# state live during the run.
make -C tools/integration-suite local

# Auto-managed stack — boots its own NATS/Mongo/Cassandra/Valkey/services
# via testcontainers (~72s to "stack ready"). CI-shape; self-cleaning.
USE_INFRA=true make -C tools/integration-suite local
```

- Both paths run all scenarios and auto-reap (Ryuk + `TerminateAll`)
  on exit when `USE_INFRA=true`.
- Exit 0 = all pass; exit 1 = at least one failure.
- Validate scenario YAML without infra: `make -C tools/integration-suite validate`

## Interactive scenario dev loop (new in 4.6)

For iterating on scenarios without paying the full-sweep cost every
run. Process stays alive; connections stay warm; you pick which
scenarios to fire from a stdin menu.

```sh
INTERACTIVE=true make -C tools/integration-suite run
```

- Requires the manual stack (`make deps-up && make up`) — does NOT
  boot its own stack.
- Drops into the menu immediately. **Nothing runs until you pick.**
- Per-pick YAML is re-read from disk → editor saves are picked up.
- Per-pick latency: ~150-400ms (Sandbox.Setup + execution).
- **Reports are separated.** Interactive picks flush to
  `docs/integration-suite/last-run-interactive.md` (env:
  `INTERACTIVE_OUTPUT_PATH`, default `last-run-interactive.md`).
  The canonical full-suite snapshot `last-run.md` and the CI gate
  `last-run-approved.md` are **never** touched — they keep whatever
  the most recent `make local` / `USE_INFRA=true make local` run
  produced, so CI + the human "is master green?" check stay valid
  across an interactive session.

Menu actions:

| Input | What it does |
|---|---|
| `1` … `N` | Reload that scenario's YAML; run it; update result; redisplay menu |
| `a` | Run all scenarios in sequence |
| `f` | Run only scenarios currently showing `✗` |
| `r` | Re-scan `scenarios/drafts/` for new files |
| `q` or Ctrl+D | Drain connections, exit 0 |
| `<empty ENTER>` | Repeat last action (prompt shows what — `[5]`, `[a]`, `[f]`) |

**CI safety:** `INTERACTIVE` is purely opt-in. CI flows that don't
set it see today's behavior bit-identically — full sweep, exit, no
menu. The standard `make local` and `USE_INFRA=true make local`
paths are untouched.

**Gotcha — don't combine with output redirects:** `INTERACTIVE=true
make run > log.txt` will hang waiting for stdin that's been
redirected away. If you want a logged batch run, just don't set
`INTERACTIVE`.

## Read the results

```sh
cat tools/archived/integration-suite/reports/last-run.md              # full report (batch / make local)
cat tools/archived/integration-suite/reports/last-run-approved.md     # CI-gating subset (@status:approved)
cat tools/archived/integration-suite/reports/last-run-interactive.md  # most recent INTERACTIVE session's picks
```

- **Confusion matrix** — positive (through) vs negative (rejection), pass/fail.
- **Cases table** — per-case latest/best/worst durations.
- **Failure Details** — exact Gomega mismatch per failing case (the "reason").
- **performance.json** — perf history across runs.

## Teardown

Normally automatic — the run reaps its own stack on clean exit.
Manual safety net (use if a run was killed, or you disabled Ryuk
for debugging):

```sh
docker ps -aq | xargs -r docker rm -f      # remove leftover containers
docker network prune -f                    # remove orphaned testcontainer networks
```

---

## Gotchas worth telling the team (learned the hard way)

| Gotcha | What to do |
|---|---|
| **RAM (~16 GB box OOMs)** | If the manual stack is up, `make deps-down` first. The two stack modes can't co-exist on memory-constrained hosts. |
| **`make … local` (no `USE_INFRA`)** | Uses the manual stack on host ports and its preflight requires `make deps-up && make up` to be running. The two modes are mutually exclusive. |
| **Working directory** | `setup.sh` does `cd docker-local`, which persists in a shell. Run the suite with an absolute `-C /workspaces/chat/tools/integration-suite` or from repo root. |
| **Diagnosing service-internal failures** | The report shows the assertion reason, but for *why* a service errored you need its logs — and the stack is reaped on exit. Tap them live during the run: `docker logs -f <container>` (find it by `--filter ancestor=chat-local-services-<svc>:latest`) into a file. |
| **Inspecting Cassandra/Mongo state** | Same — query during the ~30s scenario window before teardown (`docker exec <cassandra> cqlsh -e "…"`). |
| **`MESSAGE_BUCKET_HOURS` must match** | Seeded Cassandra rows and the reading service must use the same bucket window, or reads silently return nothing (this bit us — Finding 20). See `docs/integration-suite-sync-register.md` §3.1 for the documented drift and §4.1 for the full register of similar config-mirror surfaces. |
| **Scenarios are drafts** | New scenarios land in `scenarios/drafts/` (informational). Promotion to `scenarios/approved/` (the CI-gating score) is a separate human-reviewed PR. |
| **Interactive mode + output redirect** | `INTERACTIVE=true make run > log.txt` hangs — the menu waits for stdin that's been redirected away. Use plain `make run` for logged batch runs. |

## One-glance "is it green?" check

```sh
USE_INFRA=true make -C tools/integration-suite local; echo "exit=$?"
grep -E 'pass:|fail:' tools/archived/integration-suite/reports/last-run.md
docker ps -aq | wc -l    # expect 0 — confirms clean teardown
```

---

## Related docs

- `docs/integration-suite-sync-register.md` — every suite hardcode that
  mirrors a production source-of-truth (CLAUDE.md, Cassandra DDL,
  subjects, streams, log strings). Consult when an assertion fails
  with a "MISSING" / "no events" / "exact-string mismatch" diagnostic
  — it's often config drift, not a real regression.
- `tools/integration-suite/README.md` — high-level overview.
- `tools/integration-suite/ARCHITECTURE.md` — design + primitives catalog.
- `tools/integration-suite/AUTHORING.md` — how to write new scenarios.
- `tools/integration-suite/SCENARIO-REFERENCE.md` — YAML grammar reference.
