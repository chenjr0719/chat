---
description: Author v3 integration-suite scenarios — sandbox + seed-effects + explicit cases + Gomega expected[] assertions
argument-hint: [free-form intent text]
---

You are authoring v3 scenarios for the integration-suite at
`tools/integration-suite/`. Read this entire file before taking
any action. The companion architecture doc is
`tools/integration-suite/ARCHITECTURE.md`; the v3 field reference
is `tools/integration-suite/SCENARIO-REFERENCE.md` — open the
reference doc now if you have not internalised the v3 schema.

**Phase 3 has burnt the v2 path.** Do NOT generate:
- `placeholders` blocks with `predicate` lookups against
  `catalogs/fixture-cast.yaml` (the cast roster is deleted).
- `sequence[]` with `reads[]` and explicit `matcher:` per read.
- `mishaps.enabled` / `mishaps.ignore` (the Cartesian grid is gone).
- `kind: positive | negative` at scenario top-level (positivity is
  now per-case via `tag:`).

The v3 model is **scenario = sandbox + ordered cases**. A scenario
declares its own seed users inline via a closed effect catalog; each
case is an explicit experiment with at most one mishap; assertions
are Gomega `Eventually`-driven `expected[]` blocks.

## Hard rules (verbatim, enforced)

```
RULE 1: Expected behavior comes from a cited design doc — not from
        training data. Every scenario has a `source:` line.

RULE 2: If the design is silent, STOP. Do not invent expectations.

RULE 3: Catalog vocabulary is closed. Verbs, matchers, readers,
        mishap kinds, AND seed-effect flags must already exist. If
        they don't, STOP and surface the gap.

RULE 4: One scenario per file. Cases within a scenario share the
        sandbox — they see each other's effects. If two experiments
        MUST start from a fresh world, write two scenarios.

RULE 5: Scenarios always land in `drafts/`. Promotion to `approved/`
        is a separate, human-reviewed PR.

RULE 6: Transport is implicit. The author picks a verb name; HTTP vs
        NATS follows from the verb (today: nats_request,
        jetstream_publish).
```

## Authoring stance — how you and the human split the work

The conversation has a natural division of labor.

- **You know the code.** Source citations come from files you can
  read — `file:line` refs in `source:` and inline `#` comments.
- **The human knows the architecture.** Architecture often lives in
  the human's head or in documents outside this repo. When you can't
  find a source for a field, **ASK** rather than guess. Empty-source
  guesses are how scenarios drift away from architectural truth.
- **Human assertion outranks your code reading.** If the human says
  "architecture says X" and the code appears to do Y, the scenario
  follows X. Add a short `# arch:` comment noting the divergence so
  the reviewer can later decide whether the code or the scenario is
  the bug.
- **Tone is collaborative, not interrogative.** One question at a
  time during brainstorm; no rigid checklist. The hard-stop check
  (Step 4) is the only gate — everything before it is conversation.

## Step 0 — Load the v3 catalog universe

Before doing anything else, read these files and form an in-memory
summary of the closed vocabulary you can use:

- `tools/integration-suite/catalogs/verbs/*.yaml` — every verb name + executor
- `tools/integration-suite/catalogs/matchers.yaml` — every matcher name (only `matches_shape` is in active use; v3 expected[].match defaults to it implicitly)
- `tools/integration-suite/catalogs/readers/*.yaml` — every reader location + emit shape (these become `expected[].location` values)
- `tools/integration-suite/catalogs/services/*.yaml` — every service's `container:` (used only by the crash mishap)
- `tools/integration-suite/catalogs/mishaps/*.yaml` — every mishap kind (closed; cases reference these by name in their `mishap:` field)
- `tools/integration-suite/catalogs/seed-effects/*.yaml` — every seed-effect flag (closed; today: `verified.yaml` only)

**There is NO `fixture-cast.yaml`** — that file was deleted in Phase 3.
The v2 cast roster is replaced by inline `seed.users[]` blocks.

Quote nothing from training data — every fact about the vocabulary
must come from a file you just read.

## Step 1 — Pick a mode based on $ARGUMENTS

- If `$ARGUMENTS` is empty: **audit mode**. Go to Step 2A.
- If `$ARGUMENTS` is non-empty free-form text: **intent mode**. The
  argument is the user's intent. Skip to Step 3.

## Step 2A — Audit mode (4-step summary)

First run `git diff main...HEAD --name-only`. If the output is empty
(no changes on this branch), short-circuit: tell the user

> "No changes detected on this branch. Provide intent directly — e.g. `/scenario-author <your intent>` — or `cancel`."

and stop. Do not produce the four-section summary on an empty diff.

Otherwise, output the following four sections, in order, in a single
message. Be terse: this is a briefing, not an essay.

1. **Diff.** Reuse the `git diff main...HEAD --name-only` output and
   run `git diff main...HEAD --stat`. List touched files. For each
   touched handler file, grep for new subject strings
   (`chat.user.*.request.*`), new HTTP routes (`r.GET`, `r.POST`,
   …), and new NATS subscriptions. State raw facts.

2. **Behavioral impact.** Read each touched handler with intent.
   Translate code changes into user-facing behavior shifts. Anchor
   each bullet to a specific `file:line`. Do NOT speculate beyond
   what the code shows.

3. **Coverage cross-reference.** Grep
   `tools/integration-suite/scenarios/**/*.yaml` for verbs/subjects
   touching each behavior bullet from §2. Report gaps.

4. **Insight.** A short bulleted list — one suggested scenario per
   uncovered behavior. Each suggestion is one line: a candidate
   `scenario:` name plus the gist.

After the four sections, ask:

> "Want to author one of these, or something else? Reply with a number/name to pick, or describe a different scenario in your own words. Reply `cancel` to exit."

If the user picks something: that's the intent. Go to Step 3.

## Step 3 — Produce the outline SET

A single invocation produces between 1 and N scenarios. **Each scenario
typically packs multiple related cases inside it**, because Phase 3
cases share a sandbox. So the natural unit is:

- **One scenario per cohesive behavior area** — e.g. "Create Room"
  covers happy path + negative variants + a mishap case.
- **Multiple scenarios** only when two experiments need fully
  independent seed state (different verified users, different
  pre-existing rooms).

Do NOT invent negatives the design doesn't mention. RULE 1 still
applies — every assertion needs a cited source.

### 3.1 Outline shape — annotated canonical v3 YAML

The outline is the **exact YAML shape of `scenario.ScenarioV3`** that
will be written to disk, with `#` comments carrying intent + source
rationale. Comments are stripped at write time (Step 5); the on-disk
`source:` field is the citation.

Present each candidate in this exact shape:

```yaml
# Scenario — <one-line gist of what this scenario covers>
# Source: <file:line> (<what it shows>), <file:line> (<what it shows>)

scenario: <human-readable name>
source:   <file:line or doc#section — short, machine-readable>

seed:
  users:
    <alias>:                                      # scenario-local handle
      verified: true                              # closed-catalog flag from catalogs/seed-effects/
    # <other aliases as needed; effect flags from the catalog only>

base_input:                                       # case-inherited defaults
  verb:    <name from catalogs/verbs/>
  subject: <subject string with ${alias.account} / ${site}>
  payload:
    <key>: <value or $auto or ${alias.id}>        # short # note when non-obvious
  credential: ${<alias>.credential}               # or ${service.<name>.credential} for service-level

cases:
  - name: <case-name-1>                           # unique within scenario
    tag: positive                                 # confusion-matrix axis
    expected:                                     # Gomega Eventually assertions
      - location: <reader name>
        match:                                    # implicit matches_shape (subset deep match)
          <field>: <value or substitution token>
        # one-line rationale for the match shape

  - name: <case-name-2>
    tag: negative
    input:                                        # case-local override (shallow-merge over base_input)
      subject: <different subject for this case>
      # payload / credential can also override
    expected:
      - location: reply
        match:
          body_json:
            error: "<expected error string>"

  - name: <case-name-3>
    tag: positive
    mishap: <one kind from catalogs/mishaps/>     # at most one per case; v3 has no grid
    expected:
      - location: mongo.rooms
        match:
          name: <expected room name>
        timeout: 5s                               # optional; default 5s
        polling: 100ms                            # optional; default 100ms
        # not: true → Consistently().ShouldNot() — assert the event NEVER happens
```

### 3.2 Notes on what's authored

- **Top-level fields are exactly**: `scenario`, `source`, `status?`,
  `seed`, `base_input`, `cases`. No `kind:` at top-level (positivity
  is per-case via `tag:`). No `mishaps:` block (mishaps are per-case).
  No `input:` at top-level (it's `base_input:` now).
- **Cases share the sandbox.** Each case sees the effects of prior
  cases on Mongo + the chaos engine (between-case Reset clears any
  partition). If two cases must NOT see each other's state, write
  TWO scenarios.
- **`mishap:` is one kind per case.** No `enabled:` switch. No
  `ignore:` list. Phase 3 dropped the Cartesian model entirely.
- **`tag:` is mandatory and per-case.** `positive` = system DOES the
  thing; `negative` = system correctly REJECTS the thing. This
  drives the confusion matrix in the report.
- **Default matcher is `matches_shape`.** The author writes the
  expected payload shape; the runtime does subset deep match against
  the event payload. The matcher field from v2 is gone — there's no
  per-read matcher pick.
- **Annotations express intent, not output.** Use `#` comments to
  cite source, justify a match shape, flag `# arch: human says X,
  code looks like Y`. Comments are stripped at write.

### 3.3 Substitution tokens (two-phase, READ CAREFULLY)

Phase 3 substitution has a **temporal coupling rule** that's easy to
violate. There are TWO substitution moments:

**Phase A — Pre-fire substitution** (subject + payload + credential):
- `${<alias>.account}` / `${<alias>.id}` / `${<alias>.jwt}` /
  `${<alias>.nkey}` / `${<alias>.credential}` — populated by
  `Sandbox.Setup`. Available everywhere.
- `${service.<name>.credential}` — service-level creds (today:
  `${service.backend.credential}` from `NATS_CREDS_FILE`).
- `${site}` — runner site ID.
- `${now}` — `time.Now().UTC().UnixMilli()`.
- `$auto` — runtime-generated unique value.

**Phase B — Post-fire substitution** (only in `expected[].match`):
- `${input.subject}` — the subject as actually fired (post-Phase A).
- `${input.payload.<key>}` — the payload field as actually fired.
- `${input.requestId}` — the UUIDv7 X-Request-ID the dispatcher set.

**The footgun.** `${input.*}` resolves from a snapshot the dispatcher
writes AFTER firing. It is empty in `base_input.subject`,
`base_input.payload`, and case `input:` overrides. It is ONLY
populated for `expected[].match` blocks. Concrete failure modes:

| Wrong | Right |
|-------|-------|
| `base_input.payload: { name: ${input.payload.name} }` | Use `$auto` for the value, then assert `match: { name: ${input.payload.name} }` in expected[] |
| `base_input.subject: chat.user.${input.requestId}…` | RequestID doesn't exist yet — use `${alias.account}` or move the subject construction into the assertion |

If you find yourself wanting `${input.*}` in `base_input` or case
`input:`, you have a temporal coupling bug. The token doesn't have a
value yet at that point. Either generate the value with `$auto` /
`${now}` / a literal and capture it in `expected[]`, OR move the
assertion into `expected[]` where Phase B substitution runs.

After the set, ask:

> "React with: `lgtm` (or `ok`) / `drop N` / `add <intent>` / `edit N.<field>` / `edit <field>` (when set has 1 entry) / `brainstorm [N]` / `cancel`."

Re-present the full current state of the set after EVERY operation
the user takes.

### 3.4 Brainstorming sub-loop

If the user replies `brainstorm` or `brainstorm N`, drop into
one-question-at-a-time Q&A. Each question targets ONE thing
(verb? source? which seed-effect flag for this alias? add a mishap
case? per-case tag? expected[] shape?). Wait for the answer, apply
it to the YAML in place, re-present.

If a question has a bounded answer space, ask it multiple choice
(see bottom of file for style). Otherwise ask open-ended.

## Step 4 — Hard-stop check (run per scenario, fail collectively)

When the user says `lgtm`, run these checks for EVERY scenario in
the set. Collect all failures, then either proceed or stop —
partial writes are forbidden.

| # | Surface          | Check |
|---|------------------|-------|
| 1 | Verb             | `base_input.verb` matches a file in `catalogs/verbs/` AND the `executor:` named in that file is registered in `internal/runtime/runner.go` (look for the `verbReg.Register(…)` calls). |
| 2 | Reader location  | Every `cases[].expected[].location` matches a file in `catalogs/readers/*.yaml`. Phase 3 supports: `reply`, `mongo.rooms`, `logs.room-service`, `logs.room-worker`, `jetstream.rooms-canonical`. |
| 3 | Seed-effect flag | Every `seed.users.<alias>.<flag>` matches a file in `catalogs/seed-effects/*.yaml`. Today the only flag is `verified`. False-valued flags are explicit no-ops and validate fine. |
| 4 | Mishap kind      | Every `cases[].mishap` (when present) matches a file in `catalogs/mishaps/*.yaml`. Today: `crash`, `mongo-partition-500ms`, `cassandra-partition-500ms`. |
| 5 | Case tag         | Every case has `tag: positive` or `tag: negative`. Cases without a tag fail the loader. |
| 6 | Substitution     | Walk every string in `base_input`, `cases[].input`, `cases[].expected[].match`. Reject any `${input.*}` token appearing in `base_input` or `cases[].input` (that's the temporal coupling footgun — `input.*` only resolves in Phase B). |
| 7 | Case uniqueness  | Every `cases[].name` is unique within the scenario (drives the perf-store key `<scenario>/<case-name>`). |

If any check fails, output a per-scenario gap report:

```
Scenario "Create Room" — 2 gaps:
  [3] seed.users.bob.admin: no `admin` flag in catalogs/seed-effects/
      (available today: verified)
  [6] base_input.subject uses ${input.requestId} — temporal coupling:
      input.requestId doesn't exist until the case fires. Drop the
      token or move the construction into expected[].match.
```

After listing all gaps across all scenarios, stop:

> "Hard stop. Resolve the gaps above (extend the surface, or drop the affected scenarios with `drop N`), then re-issue `lgtm`."

Do NOT write any file when a check fails.

## Step 5 — Write the YAML files

If all checks pass, for each scenario in the set:

1. Pick the destination:
   `tools/integration-suite/scenarios/drafts/<filename>.yaml`
   (flat — no scope subfolders). If a file with that path already
   exists, STOP and ask the user to rename.
2. **Render with comments stripped.** Take the annotated YAML from
   the outline and remove every `#` comment line and trailing `#`
   comment. Keep the structured fields exactly as they appeared in
   the outline. Reference example:
   `tools/integration-suite/scenarios/drafts/create-room-sandbox.yaml`.
3. Write the file via the Write tool.

When all files are written, proceed to Step 6.

## Step 6 — Validate

Run, in order:

1. `make -C tools/integration-suite validate` — must exit 0.
2. This walks `scenarios/drafts/` and runs `scenario.LoadFile` on
   each YAML; the v3 schema validator (loader.go) rejects malformed
   files. If any new file fails to parse, the error surfaces here.

If validation fails, surface the failure to the user and DO NOT
delete the file — leave it for inspection. Fix the YAML rendering
and re-validate.

## Step 7 — Final report

Once validation passes, output:

```
Wrote N scenario(s) to:
  - tools/integration-suite/scenarios/drafts/foo.yaml
  - tools/integration-suite/scenarios/drafts/bar.yaml

These are drafts. Promotion to scenarios/approved/ is a separate
human-reviewed PR — do not move them yourself.

Run them with:
  make -C tools/integration-suite local
  USE_INFRA=true make -C tools/integration-suite local   # boots full stack
```

Stop. Do not commit unless the user explicitly asks.

## Brainstorming style reference

When in the brainstorming sub-loop, prefer multiple-choice questions
when the answer space is bounded:

- "Which verb fits? (a) `nats_request` — sync request/reply, (b) `jetstream_publish` — fire-and-ack into a stream"
- "Which seed-effect for this alias? (a) `verified: true` (mints JWT — actor can make NATS requests), (b) omit / `verified: false` (empty JWT — NATS auth rejects, useful for negative cases)"
- "Mishap on this case? (a) none, (b) `mongo-partition-500ms`, (c) `cassandra-partition-500ms`, (d) `crash` (requires target pod context)"
- "Tag? (a) `positive` — system does the thing, (b) `negative` — system correctly rejects"

Use open-ended questions only when the answer is genuinely free-form
(naming the scenario / case, finding the right source citation,
writing the `expected[].match` shape).
