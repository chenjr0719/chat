# Authoring scenarios

This is the **hand-edit fallback** for integration-suite scenarios.
The full guided workflow lives in the `/scenario-author` slash command;
use this doc for small, targeted edits.

## Six hard rules

1. **Expected behavior comes from a cited design doc — not from
   training data.** Every scenario has a `source:` line.

2. **If the design is silent, STOP.** Do not invent expectations.

3. **Catalog vocabulary is closed.** Verbs, matchers, readers, mishap
   kinds, and seed-effect flags must already exist. If they don't,
   STOP and surface the gap.

4. **One scenario per file. Cases within a scenario share a
   sandbox.** Cases see each other's effects — that's intentional.
   If two cases must not share state, write two scenarios.

5. **Scenarios always land in `drafts/`.** Promotion to `approved/`
   is a separate, human-reviewed PR.

6. **Transport is implicit.** The author picks a verb name; HTTP vs
   NATS follows from the verb (today: `nats_request` and
   `jetstream_publish` are the only choices).

## Where things live

| Asset                        | Path                                                  |
|------------------------------|-------------------------------------------------------|
| Scenario drafts              | `scenarios/drafts/<name>.yaml` (flat — no subfolders) |
| Approved scenarios (CI gate) | `scenarios/approved/` (PR promotion only)             |
| Verbs                        | `catalogs/verbs/*.yaml`                               |
| Matchers                     | `catalogs/matchers.yaml`                              |
| Readers                      | `catalogs/readers/*.yaml`                             |
| Service profiles             | `catalogs/services/*.yaml`                            |
| Seed-effects                 | `catalogs/seed-effects/*.yaml` (closed catalog)       |
| Mishaps                      | `catalogs/mishaps/*.yaml` (closed catalog)            |
| Static seed (Valkey only)    | `seed/room-keys.json`                                 |

**For the v3 YAML field-by-field reference** — anatomy, substitution
tokens, the closed vocabularies, a worked example, common mistakes —
see [SCENARIO-REFERENCE.md](SCENARIO-REFERENCE.md). That doc is the
schema; this doc is the workflow.

## Designing a scenario

Three questions to answer before writing the YAML:

1. **What seed do my cases need?** Each scenario's sandbox starts
   with `users`/`rooms`/`subscriptions` dropped. Declare the actors
   you need in `seed.users` with the appropriate effect flags. For
   verified users (the common case): `verified: true`. For negative
   actors that should fail at NATS auth: omit `verified` (or set it
   `false` for explicit intent).

2. **What's the case decomposition?** There is no Cartesian expansion.
   Each case is one experiment you explicitly chose. The happy path
   is one case; each negative variant is another case; each mishap
   you want to test is another case. Cases run sequentially and
   share the sandbox.

3. **Which `expected[]` blocks capture the outcome?** Cases assert
   via Gomega streaming matchers (`Eventually` / `Consistently`).
   The defaults (5s timeout, 100ms polling) are usually right;
   override per-block when you need more time (slow downstream, big
   replay) or a tighter "must not happen" window.

## Mishap injection (per-case)

Mishaps attach to individual cases:

```yaml
cases:
  - name: room-creation-survives-mongo-partition
    tag: positive
    mishap: mongo-partition-500ms
    expected:
      - location: mongo_find
        args:
          collection: rooms
        match:
          name: ${input.payload.name}
```

`mishap:` takes one kind from `catalogs/mishaps/`. The mishap fires
as soon as `RunCase` starts (pre-closed trigger — there's no
gather-and-fire boundary). `Cleanup` runs in defer on a fresh 30s
context, so the partition heals even if the case panics or the parent
context cancels.

There is no Cartesian grid; you only get the cases you write.

## Substitution & runtime globals

Available in subject/payload/credential templates and per-assertion
`match` blocks:

| Token | Resolves to |
|-------|------------|
| `${<alias>.account}` | seed user's account (== alias) |
| `${<alias>.id}` | `u-` + account |
| `${<alias>.jwt}` | minted NATS JWT (empty unless `verified: true`) |
| `${<alias>.nkey}` | nkey seed (empty unless `verified: true`) |
| `${<alias>.credential}` | user-level cred shorthand |
| `${service.<name>.credential}` | service-level creds (today: `${service.backend.credential}` from `NATS_CREDS_FILE`) |
| `${site}` | `cfg.SiteID` (default `site-local`) |
| `${now}` | `time.Now().UTC().UnixMilli()` |
| `${input.subject}` | post-substitution subject (assertion-only) |
| `${input.payload.<key>}` | post-substitution payload field (assertion-only) |
| `${input.requestId}` | UUIDv7 X-Request-ID set by the dispatcher (assertion-only) |
| `$auto` | runtime-generated unique value (`it-<runID>-room-auto-<N>`) |

Anything else `${…}` is an authoring error.

## How Claude and you split the work

When authoring with `/scenario-author`, the conversation has a
natural division of labor:

- Claude reads the code and cites sources as `file:line` in the
  `source:` field and inline `#` comments.
- Claude asks you when it can't find a source for a field, rather
  than guessing. Architecture often lives in your head or in design
  docs outside this repo.
- When you assert something contrary to what the code shows, the
  scenario follows your assertion (architecture wins) and Claude
  adds a `# arch:` comment noting the divergence — so a later
  reviewer can decide whether the code or the scenario is the bug.

For hand-editing, the same stance applies: the `source:` field is the
architecture citation; a `# arch:` comment is the right vehicle for a
known code/architecture mismatch.

## Validating a hand-edit

```bash
make -C tools/integration-suite validate
```

This runs the catalog validator and the scenario loader against
every YAML in the tree. It does NOT run scenarios — for that:

```bash
make -C tools/integration-suite local
USE_INFRA=true make -C tools/integration-suite local   # boots full stack from Go
```

## When to escalate to `/scenario-author`

- You are adding a NEW scenario from scratch.
- You are unsure which verb/reader/effect/mishap to pick.
- The behavior you want to test isn't obviously documented — the
  command walks you through finding (or not finding) a source
  citation.
- You want to author multiple related scenarios (positive + negative
  pair) in one pass.

For trivial fixes (typo in a `match` value, swapping a verified flag,
renaming a case) hand-editing is fine. Run `make validate` afterwards.

## Architecture

`tools/integration-suite/ARCHITECTURE.md` explains the runtime
model — how a scenario becomes a Sandbox + sequential cases
asserted via Gomega. Read it once before authoring your first
scenario.
