# integration-suite-multisite

A scenario-driven black-box integration test platform for the chat
backend running across a two-site NATS JetStream federation. Each
scenario YAML declares per-site seed data, a single verb fire, and an
ordered list of assertions. A **Sandbox** materializes the seed on each
site and asserts outcomes via Gomega streaming matchers against six
universal data-source primitives (Mongo, Cassandra, JetStream, Core
NATS, container logs, NATS replies).

This is a fork of `tools/integration-suite/`. The two tools coexist.
Single-site scenarios and docs at `tools/integration-suite/` are frozen
and remain authoritative for single-site behavior.

> **Design contract: the tool is feature-agnostic.** New scenarios for
> new app features should require *zero* tool changes. Tool grammar
> extensions must make their effect explicit in the YAML the author
> writes — the tool never infers state the author didn't declare. See
> `ARCHITECTURE.md` §0 for the full contract and the two gates every
> proposed tool change must pass.

---

## Single-site vs multi-site at a glance

| Dimension              | Single-site (`tools/integration-suite/`) | Multi-site (`tools/integration-suite-multisite/`) |
|------------------------|------------------------------------------|---------------------------------------------------|
| Sites                  | 1                                        | 2 (`site-a`, `site-b`)                            |
| Scenario shape         | `base_input` + `cases[]` loop            | one `input` + one `expected` list                 |
| `site:` field          | not present                              | required on `input` and site-scoped `expected[]`  |
| Seed block             | top-level `seed:`                        | `sites.<site>.seed:`                              |
| Stack mode             | manual or `USE_INFRA=true`               | `USE_INFRA=true` only                             |
| Container count        | ~14                                      | 26                                                |
| Mishaps / chaos        | enabled                                  | disabled (deferred — see Open Concerns)           |

---

## How to run

`USE_INFRA=true` is the only supported mode. The suite boots its own
26-container stack via testcontainers.

```sh
time USE_INFRA=true make -C tools/integration-suite-multisite local
```

Cold boot takes roughly 5-6 minutes (Cassandra is the bottleneck). A
warm subsequent run (images already cached) takes about 1-2 minutes.
Plan for at least 6-8 GB of free RAM during steady state.

To validate scenario YAML files without booting any infrastructure:

```sh
make -C tools/integration-suite-multisite validate
```

---

## Where reports land

After a run, four files are written under the repo:

```
docs/integration-suite-multisite/last-run.md           # full report
docs/integration-suite-multisite/last-run-approved.md  # @status:approved scenarios only
docs/integration-suite-multisite/last-run-interactive.md  # interactive session picks
docs/integration-suite-multisite/performance.json      # per-scenario latest/best/worst
```

`@status:approved` scenarios form the authoritative CI-gating score.
All others are informational drafts.

---

## Scenarios shipped

Two scenarios in `scenarios/drafts/`:

- `room-creates-federates-to-site-b.yaml` — single-site happy-path
  baseline on multi-site infra. Seeds alice on site-a, creates a room,
  asserts the reply and a site-a Mongo write. No federation assertion.
- `room-create-federates-cross-site.yaml` — federation tail. Seeds
  alice on site-a and bob on site-b, creates a room with both members,
  asserts the room lands in site-a Mongo and federates to site-b Mongo
  within 10 seconds.

---

## Open concerns

**Mishaps disabled.** Per spec §5.3, chaos injection via Toxiproxy
is deferred until the two-site system is reliably green. Toxiproxy
boots passively in the connection path but no toxics are injected.
Mishaps will re-enable in a follow-up phase.

**Federation may not fire on room create.** If the cross-site scenario
times out at the site-b assertion, the production code may only
federate on message-send rather than on room-metadata events. That is
a hypothesis the smoke run tests. It is not a tool bug.

**Ryuk reaper.** Testcontainers reaps failed containers about one
second after a panic, so the runner's error message may appear before
the container logs are captured. Set
`TESTCONTAINERS_REAPER_DISABLED=true` to keep failed containers alive
for post-mortem inspection.

**Two infra unit tests are skipped.** `TestStartNATS_ReachableOnHostPort`
and `TestStartToxiproxy_AdminReachableAndProxiesProvisioned` are
`t.Skip`-ped in the Go test suite. The per-site NATS topology conf
only mounts correctly with the full repo layout, and the proxy-name
assertions cover old single-site names. Both paths are verified by
the smoke run.

---

## Further reading

- `ARCHITECTURE.md` — the 26-container stack, cross-site NATS
  transport, federation sources, Sandbox lifecycle, and the
  verb/reader primitive catalog.
- `AUTHORING.md` — how to write a scenario in the multi-site shape.
- `SCENARIO-REFERENCE.md` — strict YAML field-by-field grammar.
- `RUNBOOK.md` — prerequisites, build, run, teardown, gotchas.
