# STALE — single-site integration suite

**Status:** archived feature reference. **Not maintained.**

## Use the multi-site tool instead

All active integration testing — for both single-site and multi-site
scenarios — runs through:

```
tools/integration-suite-multisite/
```

Multi-site accepts sparse `sites:` maps, so a scenario that only
seeds and asserts against one site (e.g. `sites: {site-a: ...}` with
no `site-b` block) runs cleanly without paying any cross-site cost.
The infra-sanity scenarios at
`tools/integration-suite-multisite/scenarios/drafts/infra-sanity-rooms-pipeline-site-a.yaml`
are the canonical example of the single-site shape under the
multi-site runner.

## Why this folder exists

This tree is preserved as a **feature reference library**. Multi-site
does not yet have full parity with single-site on:

- `cases:` array grammar (one scenario file, multiple ordered
  experiments sharing a sandbox).
- Mishaps (`crash`, `mongo-partition-500ms`,
  `cassandra-partition-500ms`) — multi-site dropped these during
  the federation push and hasn't lifted them back.
- Single-purpose reader names (`mongo.rooms`, `logs.room-service`,
  `jetstream.rooms-canonical`) — multi-site uses generic
  `mongo_find`, `logs_tail`, `jetstream_consume` with `args:`.

When any of these capabilities is genuinely needed in multi-site,
this archive is where you go to crib the implementation. Until then
the code stays put.

## What's here

```
tools/archived/integration-suite/
├── STALE.md                 (this file)
├── README.md                original tool README — historical
├── ARCHITECTURE.md          original tool architecture — historical
├── AUTHORING.md             original scenario authoring workflow
├── SCENARIO-REFERENCE.md    original YAML grammar reference
├── RUNBOOK.md               original ops doc (paths updated to the new reports/ dir)
├── Makefile                 output paths point at reports/ co-located
├── cmd/, internal/, ...     Go source — imports rewritten under tools/archived/integration-suite/
├── catalogs/                closed-vocabulary YAMLs for the v3 grammar
├── scenarios/               historical scenario set
├── docs/                    spec docs that scoped this tool
│   ├── sync-register.md
│   ├── oss-replacement-research.md
│   ├── spec-scenario-dev-mode.md
│   └── spec-single-scenario-execution.md
└── reports/                 historical run outputs (performance.json tracked)
```

## What's NOT here

- `docs/integration-suite-plan-ahead.md` stays in `docs/`. It's a
  forward-looking design note (envelope + DAG + chaos engine model)
  that applies to **both** suites — not single-site-specific. It's
  the north star for the next big lift on multi-site, not a
  retirement artifact.
- The findings doc `docs/integration-suite-multisite-findings.md`
  stays in `docs/`. It's a chat-app-team-facing report from the
  multi-site work, nothing to do with this archive.

## Can I still run it?

Yes. The Makefile output paths have been updated to write reports
to `tools/archived/integration-suite/reports/` instead of
`docs/integration-suite/` (the old location no longer exists). Go
import paths have been rewritten under
`github.com/hmchangw/chat/tools/archived/integration-suite/`. The
tool builds clean and the historical scenarios validate, so a
diagnostic run is possible if you ever need one. The expectation
is you won't.
