# integration-suite

A scenario-driven black-box integration test platform. Each scenario
YAML declares seed users, a base verb input, and an ordered list of
cases. Per-scenario a **Sandbox** materializes the seed and runs each
case sequentially; cases assert outcomes via **Gomega streaming
matchers** against universal data-source primitives (Mongo, Cassandra,
JetStream, Core NATS, container logs, NATS replies).

## Quick start (after `make deps-up && make up` from repo root)

```bash
make -C tools/integration-suite local        # run all scenarios
make -C tools/integration-suite validate     # catalog + scenario static validation
INTERACTIVE=true make -C tools/integration-suite run   # author-iteration menu
```

## Layout

- `catalogs/` — YAML data: verbs, readers, matchers, mishaps, services, seed-effects.
- `scenarios/` — scenario YAML files (`drafts/` + `approved/`).
- `internal/` — Go runtime (verbs, readers, matchers, scenario loader, sandbox, pollers, case_runner).
- `cmd/runner` — main test runner binary.
- `cmd/validator` — catalog + scenario validator CLI.
- Reports are written to `<repo>/docs/integration-suite/last-run.md`.

## Scenario shape

Each scenario YAML declares:

1. `seed.users.<alias>` — inline seed users with closed-catalog effect
   flags (today: `verified: true`). At scenario startup, Sandbox.Setup
   materializes each user (Account/ID/JWT/NkeySeed); `${<alias>.account}`,
   `${<alias>.id}`, `${<alias>.jwt}`, `${<alias>.nkey}`,
   `${<alias>.credential}` substitutions resolve into the YAML.
2. `seed.rooms` / `seed.memberships` — optional pre-seeded rooms +
   subscriptions for scenarios that fire `msg.send` (gatekeeper requires
   the subscription to exist).
3. `seed.cassandra_data` — optional pre-seeded Cassandra rows with
   relative-time tokens (`${now - 2m}`) and auto-computed bucket values
   for history-service scenarios.
4. `base_input` — verb + subject + payload + credential template,
   inherited by every case.
5. `cases[]` — explicit, ordered experiments. Each case may override
   `base_input.subject`/`payload`/`credential`, attach a single
   `mishap:` kind (e.g. `mongo-partition-500ms`), and declares
   `expected[]` assertions (location + match shape + optional
   timeout/polling/not).

Full schema reference in `SCENARIO-REFERENCE.md`.

## Universal primitives

The runtime ships six universal data-source primitives. Each is an
application-agnostic Poller that takes per-assertion `args` from the
YAML — no application-specific subject names / table names / collection
names are compiled into Go.

| Location           | Args                                | Backs                                    |
|--------------------|-------------------------------------|------------------------------------------|
| `reply`            | (none)                              | Synchronous NATS request/reply outcomes  |
| `mongo_find`       | `collection`, `filter`              | Any Mongo collection                     |
| `cassandra_select` | `query`, `params?`                  | Any Cassandra table                      |
| `jetstream_consume`| `stream`, `filter_subject`          | Any JetStream stream (replay via DeliverByStartTime) |
| `nats_subscribe`   | `subject`                           | Any Core NATS subject (Warmer — subscription opens before the verb fires) |
| `logs_tail`        | `container`, `service?`             | Any container's stdout/stderr            |

## Mishap kinds

Closed catalog under `catalogs/mishaps/`; each case references a kind
by name. RunCase looks up the factory at fire time.

| Kind                          | What it perturbs                                                     |
|-------------------------------|----------------------------------------------------------------------|
| `crash`                       | `docker restart` of the target pod, fires once at case start.        |
| `mongo-partition-500ms`       | Toxiproxy `proxy.Disable()` on MongoProxy for 500ms.                 |
| `cassandra-partition-500ms`   | Toxiproxy `proxy.Disable()` on CassandraProxy for 500ms.             |

## Configuration knobs

The runner reads these env vars in `cmd/runner/main.go`. All are
optional; defaults match production-local defaults.

| Env var                | Default                       | Purpose |
|------------------------|-------------------------------|---------|
| `SITE_ID`              | `site-local`                  | Resolves `${site}` in scenario subjects/payloads. |
| `AUTH_SERVICE_URL`     | `http://localhost:8080`       | Auth service base URL; consumed by `seedeffect.VerifiedEffect`. |
| `NATS_URL`             | `nats://localhost:4222`       | NATS endpoint shared by readers and the dispatcher. |
| `NATS_CREDS_FILE`      | _(unset)_                     | Optional admin creds path; when set, opens the admin NATS connection backing `jetstream_consume` + `nats_subscribe` AND populates `${service.backend.credential}`. |
| `MONGO_URI`            | `mongodb://localhost:27017`   | MongoDB URI; consumed by `mongo_find` poller and `Sandbox.Setup`'s drop-collections step. |
| `MONGO_DB`             | `chat`                        | Mongo database name. |
| `CASSANDRA_HOSTS`      | `localhost:9042`              | Cassandra contact points; consumed by `cassandra_select` poller and Cassandra-seed engine. |
| `CASSANDRA_KEYSPACE`   | `chat`                        | Cassandra keyspace. |
| `MESSAGE_BUCKET_HOURS` | `24`                          | Bucket window for `messages_by_room` partitioning. MUST match production services — see `docs/integration-suite-sync-register.md` §3.1. |
| `VALKEY_ADDR`          | _(unset)_                     | When set, runner seeds pure-room-worker room keys at startup from `seed/room-keys.json`. |
| `SCENARIOS_DIR`        | `scenarios`                   | Directory of scenario YAMLs to walk. |
| `CATALOGS_DIR`         | `catalogs`                    | Directory of catalog YAMLs to load. |
| `OUTPUT_PATH`          | `last-run.md`                 | Where the full markdown report is written. |
| `APPROVED_OUTPUT_PATH` | _(unset)_                     | When set, also writes a filtered approved-only report. |
| `PERFORMANCE_PATH`     | _(unset)_                     | When set, persists per-case latest/best/worst across runs. Keys are `<scenario>/<case-name>`. |
| `REPO_ROOT`            | _(unset)_                     | When set, passed to git for HEAD + latest-merge commit lookup in the report header. |
| `USE_INFRA`            | `false`                       | When `true`, runner boots the full Docker stack via `internal/infra` (no `make deps-up` / `make up` needed). |
| `TEST_IMAGE_TAG`       | _(unset)_                     | Image tag the `USE_INFRA=true` path consumes. |
| `INTERACTIVE`          | `false`                       | When `true`, drops into a stdin-driven menu instead of running all scenarios. See `RUNBOOK.md`. |

Example:

```bash
USE_INFRA=true TEST_IMAGE_TAG=latest make -C tools/integration-suite local
```

## Authoring

See `AUTHORING.md` for the hand-edit workflow and `SCENARIO-REFERENCE.md`
for the field-by-field YAML grammar.

## Architecture

See `ARCHITECTURE.md` for the runtime model — how a scenario becomes a
Sandbox + sequential cases asserted via Gomega, and what each
subsystem (mishaps, chaos engine, programmatic infra, seed effects)
contributes.

## Operations

See `RUNBOOK.md` for run + teardown procedures, interactive-mode usage,
gotchas, and how to read the report.

## Programmatic stack (`internal/infra`)

When `USE_INFRA=true` is set, the runner calls `infra.Up`, threads the
host-mapped URLs (Toxiproxy admin, NATS, Mongo, auth-service) into the
runner config, and defers `TerminateAll`. Zero-value `infra.Config{}`
boots the full stack — Elasticsearch and Valkey included. Cold start
is ~3-5 min; warm is ~30-90 s (Cassandra is the long pole).

When `USE_INFRA` is unset, the runner uses the env-var path
(`AUTH_SERVICE_URL`, `NATS_URL`, …) and assumes you've already brought
the stack up via `make deps-up && make up`.
