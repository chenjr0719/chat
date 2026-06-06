# Multi-site Integration Suite Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fork `tools/integration-suite/` into a new sibling `tools/integration-suite-multisite/` that boots a 2-site chat stack (NATS supercluster + per-site Mongo/Valkey/services + shared Cassandra + 1 Toxiproxy) and runs one federation happy-path scenario to green.

**Architecture:** Verbatim copy of single-site, then collapse the case-loop model (no `Cases`/`BaseInput`/`CaseInputOverride` — just one `Input` + one `Expected` list per scenario), add a `Site` field on `Input` and `Expected` for routing, expand infra to 2-site with one Toxiproxy hosting site-named proxies, attach JetStream federation Sources from a catalog YAML, and write one end-to-end scenario that proves alice's create-room federates to site-b's Mongo.

**Tech Stack:** Go 1.25, NATS (supercluster + JetStream domains), MongoDB driver v2, gocql, Toxiproxy, testcontainers-go, Gomega matchers, `gopkg.in/yaml.v3`.

**Spec:** `docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md` — read it end-to-end before starting Task 1. Especially §11 (5 rounds of locked decisions) which records why each shape was chosen.

**Mishaps are disabled for this work** — per spec §5.3, do not touch `internal/mishap/` or add chaos to the new tool. Toxiproxy sits passively in the connection path; nothing injects faults.

---

## File Structure

After Task 1's copy + rewrite, the fork starts as a verbatim duplicate. The plan then modifies/creates these specific files in subsequent tasks. The single-site tool at `tools/integration-suite/` is **frozen** — no task in this plan touches it.

**New tool root: `tools/integration-suite-multisite/`**

### Scenario types (collapsed grammar)
- `internal/scenario/types.go` — Rewrite to drop `BaseInput`, `Case`, `CaseInputOverride`, `Cases`; add `Input`, `Expected.Site`, `Tag` on `Scenario`.
- `internal/scenario/loader.go` — Update to load the new shape; reject deprecated tokens (`${site}`, `${siteA}`, `${siteB}`, `${alias.site}`, `${service.*.credential}`).
- `internal/scenario/validate.go` — Add `site:` presence/absence rules per location type.

### Per-site config + runtime
- `internal/runtime/config.go` (if exists) or `cmd/runner/main.go` — `Config` struct gets `SiteA`/`SiteB` fields.
- `internal/verbs/types.go` — `Input` struct gets `Site string` field; `Credential` does NOT change (no `Site` field).
- `internal/verbs/nats_request.go` — `NATSURL string` → `SiteURLs map[string]string`; pick by `in.Site`.
- `internal/verbs/jetstream_publish.go` — mirrors above.
- `internal/runtime/dispatcher.go` — Pass `Site` through from input.
- `internal/runtime/sandbox.go` — `SandboxDeps` gains `MongoByeSite map[string]*mongo.Database`, `AuthURLBySite map[string]string`; Setup loops Mongo drop/profile-insert/seed per site.
- `internal/runtime/runner_scenario.go` — Drop case loop; one `Fire + assert + record` per scenario.
- `internal/runtime/runner_report.go` + `internal/runtime/reporter.go` — `Cases []CaseReport` → `Scenarios []ScenarioReport`; rename `recordCase` → `recordScenario`; key by `<scenario>` (no case-name component).

### Pollers
- `internal/runtime/pollers/registry.go` — `Poller.PollFn` signature gains `site string`.
- `internal/runtime/pollers/mongo_find.go` — `MongoDB *mongo.Database` → `Sites map[string]*mongo.Database`; PollFn picks by site.
- `internal/runtime/pollers/jetstream_consume.go` — cache key `(site, stream, filter_subject)`; uses `jetstream.WithDomain(site)`.
- `internal/runtime/pollers/nats_subscribe.go` — cache key `(site, subject)`; same Warmer pattern but site-aware.
- `internal/runtime/pollers/logs_tail.go` — takes site arg; container name suffix.
- `internal/runtime/pollers/cassandra_select.go` — unchanged (shared cluster).
- `internal/runtime/pollers/reply.go` — unchanged.

### Infra (`internal/infra/`)
- `internal/infra/config.go` — `Config` gains nothing structural; site IDs are constants `"site-a"`, `"site-b"`.
- `internal/infra/stack.go` — 2× NATS/Mongo/Valkey, 1× Cassandra, 1× Toxiproxy; new `Stack.NATSURL(site)`, `Stack.MongoURI(site)`, `Stack.AuthURL(site)` accessors.
- `internal/infra/deps.go` — `startNATS` parameterized by site; mounts shared `docker-local/nats.conf` + new per-site gateway include.
- `internal/infra/services.go` — `startService` and `serviceEnv` parameterized by site.
- `internal/infra/toxiproxy.go` — **NEW.** Programmatically POST 6 proxies to admin API.
- `internal/infra/federation.go` — **NEW.** Load `catalogs/federation.yaml`; wait for INBOX streams; apply Sources.

### Catalogs + NATS configs
- `catalogs/federation.yaml` — **NEW.** Two sources (a→b, b→a).
- `internal/infra/nats.gateway.site-a.conf` — **NEW.** Embedded (not on disk). Per-site gateway + JS domain config.
- `internal/infra/nats.gateway.site-b.conf` — **NEW.** Mirror.

### Entry point + scenarios
- `cmd/runner/main.go` — Build `Config{SiteA, SiteB, …}` from env or `infra.Stack` accessors.
- `cmd/validator/main.go` — Update known executors / known services for the multisite vocabulary.
- `scenarios/drafts/room-creates-federates-to-site-b.yaml` — **NEW.** The one happy-path scenario.

---

## Task 1: Fork the tool

**Files:**
- Create: `tools/integration-suite-multisite/` (whole tree, via cp)
- Delete inside copy: `scenarios/drafts/*.yaml` (single-site scenarios irrelevant)

- [ ] **Step 1: Verify single-site is green BEFORE you touch anything**

Run:
```bash
go test -race ./tools/integration-suite/... 2>&1 | tail -20
```
Expected: all packages `ok`. If anything fails, STOP — investigate before forking.

- [ ] **Step 2: Copy the tree**

Run:
```bash
cp -r tools/integration-suite tools/integration-suite-multisite
```

- [ ] **Step 3: Rewrite import paths in every Go file inside the copy**

Run:
```bash
find tools/integration-suite-multisite -name "*.go" -exec \
  sed -i 's|github.com/hmchangw/chat/tools/integration-suite/|github.com/hmchangw/chat/tools/integration-suite-multisite/|g' {} \;
```

Verify nothing in the single-site tree was touched:
```bash
git diff --stat tools/integration-suite/ | head -5
```
Expected: empty (no changes to single-site).

- [ ] **Step 4: Delete the inherited scenarios**

Run:
```bash
rm tools/integration-suite-multisite/scenarios/drafts/*.yaml
```

- [ ] **Step 5: Update the fork's Makefile so `make -C tools/integration-suite-multisite validate` works**

Verify the Makefile contains no remaining `tools/integration-suite/` references:
```bash
grep -n "tools/integration-suite[^-]" tools/integration-suite-multisite/Makefile
```
Expected: empty.

- [ ] **Step 6: Confirm the copy builds cleanly**

Run:
```bash
go build ./tools/integration-suite-multisite/...
go test -race -count=1 ./tools/integration-suite-multisite/... 2>&1 | tail -10
```
Expected: build succeeds; tests pass (it's a verbatim copy minus scenarios).

- [ ] **Step 7: Commit**

```bash
git add tools/integration-suite-multisite/
git commit -m "feat(suite-multisite): fork from single-site tool

Copy tools/integration-suite -> tools/integration-suite-multisite,
rewrite import paths, drop inherited single-site scenarios. Single-site
tool at tools/integration-suite/ is untouched and frozen.

Verified: single-site tests still green; fork builds and passes its
own (inherited) tests."
```

---

## Task 2: Collapse `Scenario` to one-input-one-expected — failing tests first

**Files:**
- Test: `tools/integration-suite-multisite/internal/scenario/types_test.go`

- [ ] **Step 1: Write the failing tests for the new shape**

Replace the contents of `tools/integration-suite-multisite/internal/scenario/types_test.go` with:

```go
package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestScenario_ParsesNewShape(t *testing.T) {
	src := `
scenario: alice-creates-room-federates
source: room-service/handler.go:296-374
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
  site-b:
    seed:
      users:
        bob: { verified: true }

cassandra_data: []

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Engineering
    users: ["${alice.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match: { body_json: { status: accepted } }
  - location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering }
  - location: mongo_find
    site: site-b
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering }
    timeout: 10s
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(src), &s))

	assert.Equal(t, "alice-creates-room-federates", s.Name)
	assert.Equal(t, "positive", s.Tag)

	require.NotNil(t, s.Sites)
	require.Contains(t, s.Sites, "site-a")
	require.Contains(t, s.Sites, "site-b")
	assert.True(t, bool(s.Sites["site-a"].Seed.Users["alice"]["verified"]))
	assert.True(t, bool(s.Sites["site-b"].Seed.Users["bob"]["verified"]))

	assert.Equal(t, "site-a", s.Input.Site)
	assert.Equal(t, "nats_request", s.Input.Verb)
	assert.Equal(t, "${alice.credential}", s.Input.Credential)

	require.Len(t, s.Expected, 3)
	assert.Equal(t, "reply", s.Expected[0].Location)
	assert.Empty(t, s.Expected[0].Site, "reply must have no site")
	assert.Equal(t, "mongo_find", s.Expected[1].Location)
	assert.Equal(t, "site-a", s.Expected[1].Site)
	assert.Equal(t, "site-b", s.Expected[2].Site)
}

func TestScenario_HasNoCasesNoBaseInput(t *testing.T) {
	// Compile-time check: the Scenario struct exposes Input + Expected
	// directly (no BaseInput, no Cases).
	var s Scenario
	_ = s.Input    // must compile
	_ = s.Expected // must compile

	// The OLD fields should not exist; uncomment to verify the spec at
	// review time:
	// _ = s.BaseInput  // expected compile error
	// _ = s.Cases      // expected compile error
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -20
```
Expected: compile errors / failures referencing `Scenario.Sites`, `Scenario.Input`, `Scenario.Expected`, `Scenario.Tag`, and absence of `Scenario.BaseInput` / `Scenario.Cases`.

- [ ] **Step 3: Commit the failing test alone**

```bash
git add tools/integration-suite-multisite/internal/scenario/types_test.go
git commit -m "test(suite-multisite): failing test for collapsed scenario shape

Scenario should expose Input + Expected directly (no BaseInput, no
Cases); SiteBlock under sites map; Tag at top level."
```

---

## Task 3: Rewrite `Scenario` types

**Files:**
- Modify: `tools/integration-suite-multisite/internal/scenario/types.go`

- [ ] **Step 1: Replace the file with the new shape**

Replace the contents of `tools/integration-suite-multisite/internal/scenario/types.go` with:

```go
package scenario

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario is one test unit: one input fire + one set of assertions.
// No cases, no base_input/case.input distinction. Variants are
// separate scenario files.
type Scenario struct {
	Name          string                  `yaml:"scenario"`
	Source        string                  `yaml:"source"`
	Status        string                  `yaml:"status,omitempty"`
	Tag           string                  `yaml:"tag"` // "positive" | "negative"
	Sites         map[string]SiteBlock    `yaml:"sites"`
	CassandraData []SeedCassandraTable    `yaml:"cassandra_data,omitempty"`
	Input         Input                   `yaml:"input"`
	Expected      []Expected              `yaml:"expected"`
}

// SiteBlock is the per-site seed data — exactly the single-site
// SeedBlock, but nested under sites.<site>.
type SiteBlock struct {
	Seed SiteSeed `yaml:"seed"`
}

// SiteSeed mirrors single-site's SeedBlock minus cassandra_data
// (which moves to scenario top level since Cassandra is shared).
type SiteSeed struct {
	Users       map[string]SeedUserFlags    `yaml:"users,omitempty"`
	Rooms       []SeedRoom                  `yaml:"rooms,omitempty"`
	Memberships map[string][]SeedMembership `yaml:"memberships,omitempty"`
}

// SeedCassandraTable, SeedCassandraRow, SeedUserFlags, SeedRoom,
// RoomType, SeedMembership, role constants — preserved verbatim from
// single-site so the seed engine is identical.

type SeedCassandraTable struct {
	Table string             `yaml:"table"`
	Rows  []SeedCassandraRow `yaml:"rows"`
}

type SeedCassandraRow map[string]any

type SeedUserFlags map[string]bool

type SeedRoom struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name,omitempty"`
	Type      RoomType `yaml:"type,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`
}

type RoomType string

const (
	RoomTypeChannel    RoomType = "channel"
	RoomTypeDM         RoomType = "dm"
	RoomTypeBotDM      RoomType = "botDM"
	RoomTypeDiscussion RoomType = "discussion"
)

type SeedMembership struct {
	Room  string   `yaml:"room"`
	Roles []string `yaml:"roles,omitempty"`
}

func (m *SeedMembership) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return fmt.Errorf("scenario: empty membership scalar")
		}
		m.Room = node.Value
		m.Roles = nil
		return nil
	case yaml.MappingNode:
		var raw struct {
			Room  string   `yaml:"room"`
			Roles []string `yaml:"roles,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("scenario: parse membership object: %w", err)
		}
		m.Room = raw.Room
		m.Roles = raw.Roles
		return nil
	default:
		return fmt.Errorf("scenario: membership must be string or object, got kind=%d", node.Kind)
	}
}

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Input is the scenario's single fire.
type Input struct {
	Site       string         `yaml:"site"`
	Verb       string         `yaml:"verb"`
	Subject    string         `yaml:"subject"`
	Payload    map[string]any `yaml:"payload"`
	Credential string         `yaml:"credential,omitempty"`
}

// Expected is one assertion.
type Expected struct {
	Location string         `yaml:"location"`
	Site     string         `yaml:"site,omitempty"` // required for site-scoped pollers, forbidden for reply/cassandra_select
	Args     map[string]any `yaml:"args,omitempty"`
	Match    map[string]any `yaml:"match"`
	Timeout  Duration       `yaml:"timeout,omitempty"`
	Polling  Duration       `yaml:"polling,omitempty"`
	Not      bool           `yaml:"not,omitempty"`
}

// Duration unchanged from single-site.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Value == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("scenario: parse duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}
```

- [ ] **Step 2: Run the tests to verify they pass**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -10
```
Expected: `TestScenario_ParsesNewShape` and `TestScenario_HasNoCasesNoBaseInput` PASS. Other inherited tests may now fail (they reference old types) — that's expected and Task 4 fixes them.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-multisite/internal/scenario/types.go
git commit -m "feat(suite-multisite): rewrite Scenario types — one input + one expected

Drop BaseInput / Case / CaseInputOverride / Cases. Scenario exposes
Input and Expected at top level; SiteBlock nests seed data per site;
CassandraData stays at scenario top level (shared Cassandra cluster).
Add Site to Input and Expected per the explicit-site decision (Round 4)."
```

---

## Task 4: Delete inherited tests that reference the dead types

**Files:**
- Modify (delete or rewrite): all test files in `internal/scenario/` that referenced `BaseInput`, `Cases`, `CaseInputOverride`.

- [ ] **Step 1: Identify which tests fail to compile**

Run:
```bash
go build ./tools/integration-suite-multisite/... 2>&1 | head -30
```
List every `_test.go` file with `Cases` / `BaseInput` / `CaseInputOverride` references.

- [ ] **Step 2: Delete each tied-to-old-shape test file (or its specific tests) in `internal/scenario/`**

```bash
# Adjust list based on Step 1's output. Likely candidates:
rm tools/integration-suite-multisite/internal/scenario/loader_test.go
rm tools/integration-suite-multisite/internal/scenario/validate_*_test.go 2>/dev/null || true
```

These will be re-added in Tasks 5 + 6 against the new shape.

- [ ] **Step 3: Verify scenario package compiles**

Run:
```bash
go build ./tools/integration-suite-multisite/internal/scenario/... 2>&1
```
Expected: clean build.

- [ ] **Step 4: Commit**

```bash
git add -A tools/integration-suite-multisite/internal/scenario/
git commit -m "chore(suite-multisite): drop tests for removed BaseInput/Cases types

Tests will be re-added against the new shape in subsequent tasks."
```

---

## Task 5: Loader rejects deprecated substitution tokens — failing test

**Files:**
- Test: `tools/integration-suite-multisite/internal/scenario/loader_test.go`

- [ ] **Step 1: Write failing tests for token rejection**

Create `tools/integration-suite-multisite/internal/scenario/loader_test.go`:

```go
package scenario

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTempScenario(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestLoadFile_AcceptsNewShape(t *testing.T) {
	path := writeTempScenario(t, `
scenario: tiny
source: nowhere
tag: positive
sites:
  site-a:
    seed:
      users: { alice: { verified: true } }
input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.req
  payload: {}
  credential: ${alice.credential}
expected:
  - location: reply
    match: { body_json: { status: accepted } }
`)
	s, err := LoadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "tiny", s.Name)
}

func TestLoadFile_RejectsDeprecatedSiteToken(t *testing.T) {
	path := writeTempScenario(t, `
scenario: tiny
source: nowhere
tag: positive
sites:
  site-a:
    seed:
      users: { alice: { verified: true } }
input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.${site}.create
  payload: {}
  credential: ${alice.credential}
expected:
  - location: reply
    match: { body_json: { status: accepted } }
`)
	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "${site}",
		"loader must reject the deprecated ${site} token")
}

func TestLoadFile_RejectsServiceCredentialToken(t *testing.T) {
	path := writeTempScenario(t, `
scenario: tiny
source: nowhere
tag: positive
sites:
  site-a:
    seed:
      users: { alice: { verified: true } }
input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.req
  payload: {}
  credential: ${service.backend.credential}
expected:
  - location: reply
    match: { body_json: { status: accepted } }
`)
	_, err := LoadFile(path)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "service",
		"loader must reject ${service.*.credential} tokens")
}
```

- [ ] **Step 2: Run the tests; expect failures**

Run:
```bash
go test -race -count=1 -run TestLoadFile ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -10
```
Expected: `TestLoadFile_AcceptsNewShape` may pass; the two rejection tests FAIL (no rejection logic yet).

- [ ] **Step 3: Commit failing tests**

```bash
git add tools/integration-suite-multisite/internal/scenario/loader_test.go
git commit -m "test(suite-multisite): failing tests for deprecated-token rejection"
```

---

## Task 6: Make the loader reject deprecated tokens

**Files:**
- Modify: `tools/integration-suite-multisite/internal/scenario/loader.go`

- [ ] **Step 1: Read the existing loader**

```bash
cat tools/integration-suite-multisite/internal/scenario/loader.go
```

- [ ] **Step 2: Add a deprecated-token check after YAML parse**

Add this helper function at the bottom of `internal/scenario/loader.go`:

```go
// rejectDeprecatedTokens scans the scenario for tokens that were
// removed in the multi-site fork. Run after YAML parse, before any
// validation that uses the parsed values.
func rejectDeprecatedTokens(s *Scenario) error {
	deprecated := []string{
		"${site}",
		"${siteA}",
		"${siteB}",
	}
	check := func(field, value string) error {
		for _, tok := range deprecated {
			if strings.Contains(value, tok) {
				return fmt.Errorf("scenario %q: %s contains deprecated token %q — write the site name literally (e.g. \"site-a\")", s.Name, field, tok)
			}
		}
		if strings.Contains(value, "${service.") {
			return fmt.Errorf("scenario %q: %s contains ${service.*.credential} which is removed in multi-site; use user creds only", s.Name, field)
		}
		// Reject ${<alias>.site} as a substitution token. Match
		// pattern ${<word>.site} where <word> is the alias.
		// Quick rejection: any occurrence of ".site}" hints at it.
		if strings.Contains(value, ".site}") {
			return fmt.Errorf("scenario %q: %s contains ${<alias>.site} which is removed in multi-site; write the site name literally", s.Name, field)
		}
		return nil
	}

	if err := check("input.subject", s.Input.Subject); err != nil {
		return err
	}
	if err := check("input.credential", s.Input.Credential); err != nil {
		return err
	}
	for k, v := range s.Input.Payload {
		if vs, ok := v.(string); ok {
			if err := check(fmt.Sprintf("input.payload.%s", k), vs); err != nil {
				return err
			}
		}
	}
	for i, e := range s.Expected {
		for k, v := range e.Match {
			if vs, ok := v.(string); ok {
				if err := check(fmt.Sprintf("expected[%d].match.%s", i, k), vs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
```

Add `"strings"` to the loader's imports if not already present.

Then, in the loader's main parse path (likely a function named `LoadFile`), after the YAML unmarshal returns successfully and before returning the scenario, call:

```go
if err := rejectDeprecatedTokens(&s); err != nil {
    return nil, err
}
```

- [ ] **Step 3: Run the loader tests**

Run:
```bash
go test -race -count=1 -run TestLoadFile ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -10
```
Expected: all three tests PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-multisite/internal/scenario/loader.go
git commit -m "feat(suite-multisite): loader rejects \${site}, \${siteA}, \${siteB}, \${alias.site}, \${service.*.credential}

Tokens are removed in multi-site (site is structural via YAML's
site: field, not substitution). Loader fails fast at scenario load
with the offending field name."
```

---

## Task 7: Validator for `site:` presence rules — failing test

**Files:**
- Test: `tools/integration-suite-multisite/internal/scenario/validate_site_test.go`

- [ ] **Step 1: Write the test**

Create `tools/integration-suite-multisite/internal/scenario/validate_site_test.go`:

```go
package scenario

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// New validator: ValidateSiteFields checks the rules in spec §3.2:
// - site: required on Input
// - site: required on Expected[i] where Location ∈ {mongo_find, jetstream_consume, nats_subscribe, logs_tail}
// - site: forbidden on Expected[i] where Location ∈ {reply, cassandra_select}
// - site: must be "site-a" or "site-b"

func TestValidateSiteFields_InputMissingSite(t *testing.T) {
	s := &Scenario{
		Name: "x",
		Input: Input{Verb: "nats_request"}, // no Site
		Expected: []Expected{},
	}
	err := ValidateSiteFields(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input.site")
}

func TestValidateSiteFields_InputBadSite(t *testing.T) {
	s := &Scenario{
		Name: "x",
		Input: Input{Site: "site-c", Verb: "nats_request"},
	}
	err := ValidateSiteFields(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "site-c")
}

func TestValidateSiteFields_ExpectedMongoFindMissingSite(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: Input{Site: "site-a", Verb: "nats_request"},
		Expected: []Expected{
			{Location: "mongo_find"}, // no Site
		},
	}
	err := ValidateSiteFields(s)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "expected[0]")
	assert.Contains(t, err.Error(), "site")
}

func TestValidateSiteFields_ExpectedReplyHasSite(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: Input{Site: "site-a", Verb: "nats_request"},
		Expected: []Expected{
			{Location: "reply", Site: "site-a"}, // forbidden
		},
	}
	err := ValidateSiteFields(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "reply")
}

func TestValidateSiteFields_ExpectedCassandraSelectHasSite(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: Input{Site: "site-a", Verb: "nats_request"},
		Expected: []Expected{
			{Location: "cassandra_select", Site: "site-b"}, // forbidden
		},
	}
	err := ValidateSiteFields(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cassandra_select")
}

func TestValidateSiteFields_HappyPath(t *testing.T) {
	s := &Scenario{
		Name:  "x",
		Input: Input{Site: "site-a", Verb: "nats_request"},
		Expected: []Expected{
			{Location: "reply"},                       // no site, allowed
			{Location: "mongo_find", Site: "site-a"},  // site required, present
			{Location: "mongo_find", Site: "site-b"},  // same
			{Location: "cassandra_select"},            // no site, allowed
		},
	}
	require.NoError(t, ValidateSiteFields(s))
}
```

- [ ] **Step 2: Run the test; expect failures**

Run:
```bash
go test -race -count=1 -run TestValidateSiteFields ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -10
```
Expected: compile error — `ValidateSiteFields` doesn't exist yet.

- [ ] **Step 3: Commit failing tests**

```bash
git add tools/integration-suite-multisite/internal/scenario/validate_site_test.go
git commit -m "test(suite-multisite): failing tests for ValidateSiteFields"
```

---

## Task 8: Implement `ValidateSiteFields`

**Files:**
- Create: `tools/integration-suite-multisite/internal/scenario/validate_site.go`

- [ ] **Step 1: Write the implementation**

Create `tools/integration-suite-multisite/internal/scenario/validate_site.go`:

```go
package scenario

import "fmt"

// knownSites is the closed list of valid site names. Multi-site is
// always exactly 2 sites — see spec §1 decision row 2.
var knownSites = map[string]struct{}{
	"site-a": {},
	"site-b": {},
}

// requiresSite returns true for poller locations that need a site
// arg. mongo_find, jetstream_consume, nats_subscribe, logs_tail.
func requiresSite(location string) bool {
	switch location {
	case "mongo_find", "jetstream_consume", "nats_subscribe", "logs_tail":
		return true
	}
	return false
}

// forbidsSite returns true for poller locations that MUST NOT carry
// a site arg. reply (intrinsic to the fire) and cassandra_select
// (shared cluster).
func forbidsSite(location string) bool {
	switch location {
	case "reply", "cassandra_select":
		return true
	}
	return false
}

// ValidateSiteFields checks the rules in spec §3.2:
//   - input.site is required and must be one of knownSites.
//   - expected[i].site is required when location is site-scoped,
//     forbidden when location is reply or cassandra_select, and
//     when present must be one of knownSites.
func ValidateSiteFields(s *Scenario) error {
	if s.Input.Site == "" {
		return fmt.Errorf("scenario %q: input.site is required (one of site-a, site-b)", s.Name)
	}
	if _, ok := knownSites[s.Input.Site]; !ok {
		return fmt.Errorf("scenario %q: input.site = %q; must be site-a or site-b", s.Name, s.Input.Site)
	}
	for i, e := range s.Expected {
		switch {
		case requiresSite(e.Location):
			if e.Site == "" {
				return fmt.Errorf("scenario %q: expected[%d] location=%s requires a site field", s.Name, i, e.Location)
			}
			if _, ok := knownSites[e.Site]; !ok {
				return fmt.Errorf("scenario %q: expected[%d].site = %q; must be site-a or site-b", s.Name, i, e.Site)
			}
		case forbidsSite(e.Location):
			if e.Site != "" {
				return fmt.Errorf("scenario %q: expected[%d] location=%s must not carry a site field", s.Name, i, e.Location)
			}
		default:
			// Unknown location — let the registry catch it at run time.
		}
	}
	return nil
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run:
```bash
go test -race -count=1 -run TestValidateSiteFields ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -10
```
Expected: all six tests PASS.

- [ ] **Step 3: Wire `ValidateSiteFields` into the loader**

Edit `internal/scenario/loader.go` and call `ValidateSiteFields(&s)` immediately after `rejectDeprecatedTokens(&s)` and before returning. Then run the full scenario package tests:

```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/scenario/... 2>&1 | tail -10
```
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-multisite/internal/scenario/validate_site.go \
        tools/integration-suite-multisite/internal/scenario/loader.go
git commit -m "feat(suite-multisite): ValidateSiteFields enforces site: presence/absence rules

input.site required; expected[i].site required for site-scoped pollers
(mongo_find, jetstream_consume, nats_subscribe, logs_tail), forbidden
for reply and cassandra_select. Wired into loader so failures surface
at scenario-load time."
```

---

## Task 9: Update `verbs.Input` and `NATSRequest` for per-site routing — failing test

**Files:**
- Test: `tools/integration-suite-multisite/internal/verbs/nats_request_test.go`

- [ ] **Step 1: Read current Input and NATSRequest shapes**

```bash
cat tools/integration-suite-multisite/internal/verbs/types.go
cat tools/integration-suite-multisite/internal/verbs/nats_request.go
```

- [ ] **Step 2: Replace test file with new tests**

Replace `tools/integration-suite-multisite/internal/verbs/nats_request_test.go`:

```go
package verbs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNATSRequest_PicksURLByInputSite(t *testing.T) {
	n := &NATSRequest{
		SiteURLs: map[string]string{
			"site-a": "nats://nonexistent-site-a:4222",
			"site-b": "nats://nonexistent-site-b:4222",
		},
		Timeout: 50 * time.Millisecond,
	}
	in := &Input{
		Site:    "site-a",
		Subject: "x",
		Payload: []byte("{}"),
		// no Credential — connect will fail at "no credential" or
		// at TCP. Either is fine; we're testing URL selection only.
	}
	out := n.Execute(context.Background(), in)
	// The actual connection should fail (no server), but the error
	// must mention site-a's URL, not site-b's.
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "nonexistent-site-a",
		"executor must have tried site-a's URL based on in.Site")
}

func TestNATSRequest_RejectsUnknownSite(t *testing.T) {
	n := &NATSRequest{
		SiteURLs: map[string]string{
			"site-a": "nats://a:4222",
			"site-b": "nats://b:4222",
		},
		Timeout: 50 * time.Millisecond,
	}
	in := &Input{Site: "site-c", Subject: "x"}
	out := n.Execute(context.Background(), in)
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "site-c")
}
```

Add `"github.com/stretchr/testify/require"` to imports if needed.

- [ ] **Step 3: Run; expect failures**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/verbs/... 2>&1 | tail -10
```
Expected: compile error — `NATSRequest.SiteURLs` and `Input.Site` don't exist.

- [ ] **Step 4: Commit failing tests**

```bash
git add tools/integration-suite-multisite/internal/verbs/nats_request_test.go
git commit -m "test(suite-multisite): failing test for per-site NATS URL routing"
```

---

## Task 10: Implement per-site URL map in `NATSRequest` + `JetStreamPublish`

**Files:**
- Modify: `tools/integration-suite-multisite/internal/verbs/types.go`
- Modify: `tools/integration-suite-multisite/internal/verbs/nats_request.go`
- Modify: `tools/integration-suite-multisite/internal/verbs/jetstream_publish.go`

- [ ] **Step 1: Add Site to Input**

Edit `internal/verbs/types.go`:
- Locate the `Input` struct.
- Add `Site string` as the first field.

```go
type Input struct {
	Site        string
	Subject     string
	Payload     []byte
	Credential  Credential
	Traceparent string
}
```

- [ ] **Step 2: Rewrite NATSRequest**

Edit `internal/verbs/nats_request.go`:
- Replace `NATSURL string` with `SiteURLs map[string]string`.
- In `Execute`, look up `n.SiteURLs[in.Site]` before the `nats.Connect` call.
- Replace constructor:

```go
func NewNATSRequest(siteURLs map[string]string) *NATSRequest {
	return &NATSRequest{SiteURLs: siteURLs, Timeout: 5 * time.Second}
}

func (n *NATSRequest) Execute(ctx context.Context, in *Input) Outcome {
	requestID := idgen.GenerateRequestID()

	url, ok := n.SiteURLs[in.Site]
	if !ok || url == "" {
		return Outcome{
			Err:       fmt.Errorf("nats_request: no NATS URL for site %q", in.Site),
			RequestID: requestID,
		}
	}
	// ... rest of the function uses `url` instead of `n.NATSURL`.
}
```

- [ ] **Step 3: Mirror in JetStreamPublish**

Edit `internal/verbs/jetstream_publish.go` with the same shape change.

- [ ] **Step 4: Run verb tests**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/verbs/... 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 5: Build the whole tree to see what else breaks**

Run:
```bash
go build ./tools/integration-suite-multisite/... 2>&1 | head -30
```
Expected: `cmd/runner/main.go` and possibly `internal/runtime/runner.go` fail to compile because they pass the old `cfg.NATSURL` (single string) to `NewNATSRequest`. That's expected; Task 14 fixes the runner wiring.

- [ ] **Step 6: Commit**

```bash
git add tools/integration-suite-multisite/internal/verbs/
git commit -m "feat(suite-multisite): verbs route by Input.Site via SiteURLs map

NATSRequest and JetStreamPublish hold SiteURLs map[string]string;
pick the URL by in.Site at Execute time. Credential carries no Site
field (per the shared trust chain — both sites' NATS accept the same
JWTs; routing is structural via YAML's site: field)."
```

---

## Task 11: Per-site poller backends — failing test for `mongo_find`

**Files:**
- Test: `tools/integration-suite-multisite/internal/runtime/pollers/mongo_find_test.go`

- [ ] **Step 1: Read existing MongoFindPoller**

```bash
cat tools/integration-suite-multisite/internal/runtime/pollers/mongo_find.go
```

- [ ] **Step 2: Replace test file**

Open `internal/runtime/pollers/mongo_find_test.go`. Replace any test that constructs `MongoFindPoller{MongoDB: ...}` with one that uses `Sites map[string]*mongo.Database` and a `PollFn(site, args, tp)` signature. If the file does not exist, create it:

```go
package pollers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMongoFindPoller_NilSiteIsWarnNotPanic(t *testing.T) {
	// Sanity: an empty Sites map should produce a poller whose
	// PollFn returns nil events without panicking. (Real backend
	// tests live in an integration test under -tags integration.)
	p := &MongoFindPoller{Sites: nil}
	pollFn := p.PollFn("site-a", map[string]any{
		"collection": "rooms",
		"filter":     map[string]any{},
	}, "")
	events := pollFn()
	assert.Empty(t, events)
}

func TestMongoFindPoller_UnknownSiteIsWarnNotPanic(t *testing.T) {
	p := &MongoFindPoller{
		Sites: map[string]*mongo.Database{}, // empty but non-nil
	}
	pollFn := p.PollFn("site-z", map[string]any{
		"collection": "rooms",
	}, "")
	events := pollFn()
	assert.Empty(t, events)
}
```

Add `"go.mongodb.org/mongo-driver/v2/mongo"` to imports.

- [ ] **Step 3: Run; expect failures**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/runtime/pollers/... 2>&1 | tail -10
```
Expected: compile error.

- [ ] **Step 4: Commit**

```bash
git add tools/integration-suite-multisite/internal/runtime/pollers/mongo_find_test.go
git commit -m "test(suite-multisite): failing test for MongoFindPoller per-site Sites map"
```

---

## Task 12: Implement per-site `MongoFindPoller`

**Files:**
- Modify: `tools/integration-suite-multisite/internal/runtime/pollers/mongo_find.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/pollers/registry.go`

- [ ] **Step 1: Update the `Poller` interface to take a site arg**

Edit `internal/runtime/pollers/registry.go`. Change the `Poller` interface from:

```go
type Poller interface {
	PollFn(args map[string]any, traceparent string) func() []readers.Event
}
```

to:

```go
type Poller interface {
	PollFn(site string, args map[string]any, traceparent string) func() []readers.Event
}
```

- [ ] **Step 2: Rewrite MongoFindPoller**

Edit `internal/runtime/pollers/mongo_find.go`. Replace the struct + PollFn:

```go
type MongoFindPoller struct {
	Sites     map[string]*mongo.Database
	StartTime time.Time
}

func (p *MongoFindPoller) PollFn(site string, args map[string]any, _ string) func() []readers.Event {
	db, ok := p.Sites[site]
	if !ok || db == nil {
		return func() []readers.Event {
			slog.Warn("mongo_find: no database for site",
				"site", site, "available", siteKeys(p.Sites))
			return nil
		}
	}
	// ... existing query logic, but using `db` instead of `p.MongoDB`.
}

func siteKeys(m map[string]*mongo.Database) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

Adjust the constructor (`NewMongoFindPoller`) to take `sites map[string]*mongo.Database`.

- [ ] **Step 3: Update every other poller's PollFn signature to take site (stub it for now if internal logic doesn't use it)**

Edit each poller file in `internal/runtime/pollers/`:
- `cassandra_select.go` — signature gains `site string`, but the body ignores it (shared cluster).
- `jetstream_consume.go` — fills `site` into cache key + uses `jetstream.WithDomain(site)` (deferred to Task 17).
- `nats_subscribe.go` — same (deferred).
- `logs_tail.go` — picks the container suffix for that site.
- `reply.go` — ignores site (reply is the synthetic single-location poller).

For Tasks 12, only get the **signatures** updated so the package compiles. Internal logic for JS+NATS+logs is deferred to Task 17 once admin connection routing is in place. Cassandra is correct as-is.

- [ ] **Step 4: Run pollers tests**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/runtime/pollers/... 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-multisite/internal/runtime/pollers/
git commit -m "feat(suite-multisite): Poller interface gains site string arg; MongoFindPoller per-site

Poller.PollFn signature: (site, args, traceparent). MongoFindPoller
holds Sites map[string]*mongo.Database; picks by site. Other pollers
have site-aware signatures stubbed for now — site routing for
JS/NATS/logs lands in Task 17 once admin conn is in place. Cassandra
ignores site (shared cluster)."
```

---

## Task 13: Collapse `Sandbox` Setup loops + drop case loop — failing test

**Files:**
- Test: `tools/integration-suite-multisite/internal/runtime/sandbox_test.go`

- [ ] **Step 1: Read what changed and what needs to**

```bash
grep -n "Cases\|RunCase\|recordCase\|case\\.Input" tools/integration-suite-multisite/internal/runtime/*.go | head -20
```

- [ ] **Step 2: Identify files to gut**

The following files need significant work:
- `runtime/sandbox.go` — Setup must loop Mongo + user mint + profile docs + seed rooms per site.
- `runtime/case_runner.go` — DELETE; subsumed by `RunScenario`.
- `runtime/runner_scenario.go` — replace case loop with single `Fire + assert + record`.
- `runtime/runner_report.go` — `recordCase` → `recordScenario`.
- `runtime/reporter.go` — `Cases []CaseReport` → `Scenarios []ScenarioReport`.

This is a large refactor. Do it as one focused PR.

- [ ] **Step 3: Write a failing test for the new SandboxDeps shape**

Update `internal/runtime/sandbox_test.go` (replace contents or add tests):

```go
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSandboxDeps_PerSiteMongoMap(t *testing.T) {
	// Compile-time: SandboxDeps must expose a per-site Mongo map and
	// a per-site AuthURL map; the single fields are gone.
	var d SandboxDeps
	_ = d.MongoBySite   // map[string]*mongo.Database
	_ = d.AuthURLBySite // map[string]string

	// Cassandra is shared, stays single:
	_ = d.Cassandra

	assert.True(t, true, "compile-time check only")
}
```

- [ ] **Step 4: Run; expect compile failure**

Run:
```bash
go build ./tools/integration-suite-multisite/internal/runtime/... 2>&1 | head -10
```
Expected: `SandboxDeps` doesn't have `MongoBySite` or `AuthURLBySite`.

- [ ] **Step 5: Commit failing test**

```bash
git add tools/integration-suite-multisite/internal/runtime/sandbox_test.go
git commit -m "test(suite-multisite): failing test for per-site SandboxDeps"
```

---

## Task 14: Implement per-site `Sandbox` + drop case loop

**Files:**
- Modify: `tools/integration-suite-multisite/internal/runtime/sandbox.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/runner.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/runner_scenario.go`
- Delete:  `tools/integration-suite-multisite/internal/runtime/case_runner.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/runner_report.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/reporter.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/dispatcher.go`

This is the largest single task. Break it into mental sub-steps but commit at the end.

- [ ] **Step 1: SandboxDeps gets per-site fields**

Edit `internal/runtime/sandbox.go`. Replace the SandboxDeps struct:

```go
type SandboxDeps struct {
	// Per-site backends:
	MongoBySite   map[string]*mongo.Database
	AuthURLBySite map[string]string

	SiteID string // unused going forward; kept for now to avoid unrelated breakage. Remove in cleanup pass.

	// Shared:
	Cassandra         *gocql.Session
	CassandraKeyspace string
	MessageBucketWindow time.Duration

	Chaos         mishap.ChaosEngine // unused — chaos is disabled per spec §5.3
	Dispatcher    *Dispatcher
	SeedEffectReg *seedeffect.Registry

	ReplyReader *readers.NATSReplyReader

	// Single admin conn — multiplexes both JS domains.
	AdminConn *nats.Conn

	MatcherReg *matchers.Registry

	// FactoryByKind / DockerCLI / MishapRegistry kept for parity but
	// unused (chaos disabled).
	MishapRegistry *mishap.Registry
	FactoryByKind  map[string]string
	DockerCLI      mishap.DockerCLI
}
```

Delete the `Mongo *mongo.Database` and `AuthURL string` and `Services map[string]Credential` fields.

- [ ] **Step 2: Setup loops per site**

In `sandbox.go`, find the existing `Setup` method. For each step that touches Mongo OR `AuthURL`, replace single-backend access with a per-site loop. Specifically:

- Step 6 (drop Mongo collections): loop `for site, db := range sb.Deps.MongoBySite { for _, name := range sandboxOwnedCollections { db.Collection(name).Drop(ctx) } }`.
- Step 8 (materialize seed users): loop `for site, siteBlock := range sb.Scenario.Sites { for alias, flags := range siteBlock.Seed.Users { ... mint at sb.Deps.AuthURLBySite[site] ... } }`.
- Step 9 (insert profile docs): loop per site, insert into `sb.Deps.MongoBySite[site]`.
- Step 11 (insert seeded rooms): loop per site, insert into `sb.Deps.MongoBySite[site]`.
- Step 7 + 12 (Cassandra): unchanged — `sb.Deps.Cassandra` is single.

Add a guard: every step that fans out per-site must check the site has a backend (`if db, ok := sb.Deps.MongoBySite[site]; ok { ... } else { return fmt.Errorf(...) }`).

- [ ] **Step 3: Drop the case loop in `runner_scenario.go`**

Edit `internal/runtime/runner_scenario.go`. Replace the case loop with a single Fire + assert:

```go
func runScenario(ctx context.Context, s *scenario.Scenario, deps *runnerDeps) ScenarioReport {
	sb := NewSandbox(s, deps.toSandboxDeps())
	if err := sb.Setup(ctx); err != nil {
		return ScenarioReport{
			Name:   s.Name,
			Tag:    s.Tag,
			Status: "fail",
			Verdict: Verdict{Outcome: "fail", Reason: fmt.Sprintf("sandbox setup: %v", err)},
		}
	}
	defer sb.Teardown(context.Background())

	start := time.Now()

	// Build the substitution context.
	subCtx := buildSubContext(sb)

	// Resolve credential.
	cred := resolveCredential(s.Input.Credential, sb.Users)

	// Build InputSpec (scenario.Input → verbs.Input):
	in := &verbs.Input{
		Site:       s.Input.Site,
		// Subject + Payload are substituted by the dispatcher.
		Credential: cred,
	}
	if err := sb.Deps.Dispatcher.Fire(ctx, in, s.Input.Subject, s.Input.Payload, subCtx); err != nil {
		return ScenarioReport{
			Name:    s.Name,
			Tag:     s.Tag,
			Status:  "fail",
			Verdict: Verdict{Outcome: "fail", Reason: fmt.Sprintf("dispatcher.Fire: %v", err)},
			Duration: time.Since(start).String(),
		}
	}

	// Assertions.
	verdict := runAssertions(ctx, sb, s.Expected, subCtx)
	rep := ScenarioReport{
		Name:     s.Name,
		Tag:      s.Tag,
		Duration: time.Since(start).String(),
		Verdict:  verdict,
	}
	if verdict.Outcome == "pass" {
		rep.Status = "pass"
	} else {
		rep.Status = "fail"
	}
	return rep
}
```

`runAssertions` iterates `expected[]`, looks up each poller, calls `PollFn(e.Site, e.Args, traceparent)`, and runs Gomega `Eventually`. (Pattern stays close to today's case_runner — just one outer loop instead of two.)

- [ ] **Step 4: Delete `case_runner.go`**

```bash
git rm tools/integration-suite-multisite/internal/runtime/case_runner.go
```

Move any helpers from `case_runner.go` (substitution context build, credential resolve) into `runner_scenario.go`.

- [ ] **Step 5: Reporter migration: `Cases` → `Scenarios`**

In `internal/runtime/reporter.go`:
- Rename `Cases []CaseReport` → `Scenarios []ScenarioReport`.
- Rename type `CaseReport` → `ScenarioReport`. Drop the `CaseName` field. Keep `ScenarioName` → rename to just `Name`.
- Rendering helpers: tables now have one row per scenario.

In `runner_report.go`:
- Rename `recordCase` → `recordScenario`.
- Performance key: `<scenario>` (drop the `/<case-name>` component).

- [ ] **Step 6: Build the full tree**

Run:
```bash
go build ./tools/integration-suite-multisite/... 2>&1 | head -30
```
Expected: clean. `cmd/runner/main.go` will need updates too — fix any compile errors there now (build the Config from per-site env vars). Detailed wiring is Task 15 but trivial breakage here is fine to fix in-line.

- [ ] **Step 7: Run all unit tests**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/... 2>&1 | tail -20
```
Expected: PASS. (Integration tests behind `-tags integration` may be broken; that's fine for this task.)

- [ ] **Step 8: Commit**

```bash
git add tools/integration-suite-multisite/
git rm tools/integration-suite-multisite/internal/runtime/case_runner.go 2>/dev/null || true
git commit -m "feat(suite-multisite): per-site Sandbox + drop case loop

SandboxDeps gets MongoBySite + AuthURLBySite per-site maps; Cassandra
stays single (shared cluster). Setup steps 6/8/9/11 loop per site;
steps 7/12 single. Drop the case_runner.go file entirely. runScenario
is: Setup -> Dispatcher.Fire(scenario.Input) -> assertions per
expected[] (each pollFn takes expected[i].site) -> ScenarioReport.
Reporter renames Cases -> Scenarios; performance.json keys collapse to
<scenario>. Chaos.Reset removed from Setup (mishaps disabled per
spec §5.3)."
```

---

## Task 15: Wire the new Config in `cmd/runner/main.go`

**Files:**
- Modify: `tools/integration-suite-multisite/cmd/runner/main.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/runner.go`

- [ ] **Step 1: Update the Config struct**

Edit `internal/runtime/runner.go`. Find the `Config` struct. Replace:
```go
NATSURL string
MongoURI string
AuthURL string
```
with:
```go
SiteA SiteConfig
SiteB SiteConfig
```

Add a `SiteConfig` struct:

```go
type SiteConfig struct {
	NATSURL  string
	MongoURI string
	AuthURL  string
}
```

Keep `CassandraHosts`, `CassandraKeyspace`, `MongoDB`, `NATSCredsFile` at top level.

- [ ] **Step 2: Wire env vars in `cmd/runner/main.go`**

Edit `cmd/runner/main.go`. Replace the env-var reads for `NATSURL`/`MongoURI`/`AuthURL` with:

```go
cfg.SiteA = rt.SiteConfig{
	NATSURL:  env("SITE_A_NATS_URL", "nats://localhost:4222"),
	MongoURI: env("SITE_A_MONGO_URI", "mongodb://localhost:27017"),
	AuthURL:  env("SITE_A_AUTH_SERVICE_URL", "http://localhost:8080"),
}
cfg.SiteB = rt.SiteConfig{
	NATSURL:  env("SITE_B_NATS_URL", "nats://localhost:14222"),
	MongoURI: env("SITE_B_MONGO_URI", "mongodb://localhost:37017"),
	AuthURL:  env("SITE_B_AUTH_SERVICE_URL", "http://localhost:8081"),
}
```

Drop any code referencing `NATSURL`, `MongoURI`, `AuthURL` as single fields.

- [ ] **Step 3: Wire per-site connections in `buildSession`**

Edit `internal/runtime/runner.go` `buildSession`. Replace single Mongo open with per-site:

```go
mongoA, err := openMongo(ctx, cfg.SiteA.MongoURI, cfg.MongoDB)
if err != nil { ... }
mongoB, err := openMongo(ctx, cfg.SiteB.MongoURI, cfg.MongoDB)
if err != nil { ... }

cfg.MongoBySite = map[string]*mongo.Database{
	"site-a": mongoA,
	"site-b": mongoB,
}
```

(Helper `openMongo` factored out of today's single-call code.)

- [ ] **Step 4: Build verb registry with per-site URL map**

In `buildSession` or its `runnerDeps`:

```go
nrq := verbs.NewNATSRequest(map[string]string{
	"site-a": cfg.SiteA.NATSURL,
	"site-b": cfg.SiteB.NATSURL,
})
jsp := verbs.NewJetStreamPublish(map[string]string{
	"site-a": cfg.SiteA.NATSURL,
	"site-b": cfg.SiteB.NATSURL,
})
verbReg := verbs.NewRegistry()
verbReg.Register("NATSRequestExecutor", nrq)
verbReg.Register("JetStreamPublishExecutor", jsp)
```

- [ ] **Step 5: Build pollers BuiltinDeps per-site**

In `pollers.RegisterBuiltinPollers`'s call site, pass `Sites map[string]*mongo.Database` instead of single `MongoDB`. (The pollers package change happened in Task 12; runner wiring lands here.)

- [ ] **Step 6: Build admin conn (single, with JS domain awareness)**

```go
admin, err := nats.Connect(cfg.SiteA.NATSURL, nats.UserCredentials(cfg.NATSCredsFile))
if err != nil { ... }
// admin conn drives both JS domains via jetstream.WithDomain(...) at use sites.
deps.AdminConn = admin
```

- [ ] **Step 7: Build + test**

Run:
```bash
go build ./tools/integration-suite-multisite/...
go test -race -count=1 ./tools/integration-suite-multisite/... 2>&1 | tail -10
```
Expected: clean build; unit tests pass.

- [ ] **Step 8: Commit**

```bash
git add tools/integration-suite-multisite/cmd/runner/main.go \
        tools/integration-suite-multisite/internal/runtime/runner.go
git commit -m "feat(suite-multisite): wire per-site Config and buildSession

Config.SiteA + SiteB hold per-site NATS / Mongo / Auth URLs.
buildSession opens two Mongo clients, builds verb registry with
SiteURLs maps, single admin NATS conn (which targets either site's
JetStream by domain prefix). Cassandra / shared knobs unchanged."
```

---

## Task 16: Federation catalog YAML + loader — failing test

**Files:**
- Create: `tools/integration-suite-multisite/internal/infra/federation_test.go`

- [ ] **Step 1: Write the test**

Create `tools/integration-suite-multisite/internal/infra/federation_test.go`:

```go
//go:build !integration

package infra

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadFederationSources_ReadsTwoEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "federation.yaml")
	body := `
sources:
  - on: site-b
    stream: INBOX_site-b
    from_stream: OUTBOX_site-a
    filter: outbox.site-a.to.site-b.>
  - on: site-a
    stream: INBOX_site-a
    from_stream: OUTBOX_site-b
    filter: outbox.site-b.to.site-a.>
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	specs, err := LoadFederationSources(path)
	require.NoError(t, err)
	require.Len(t, specs, 2)

	assert.Equal(t, "site-b", specs[0].On)
	assert.Equal(t, "INBOX_site-b", specs[0].Stream)
	assert.Equal(t, "OUTBOX_site-a", specs[0].FromStream)
	assert.Equal(t, "outbox.site-a.to.site-b.>", specs[0].Filter)

	assert.Equal(t, "site-a", specs[1].On)
}

func TestLoadFederationSources_RejectsUnknownSite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "federation.yaml")
	body := `
sources:
  - on: site-c
    stream: INBOX_site-c
    from_stream: OUTBOX_site-a
    filter: outbox.>
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	_, err := LoadFederationSources(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "site-c")
}
```

- [ ] **Step 2: Run; expect failures**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/infra/... 2>&1 | tail -10
```
Expected: compile error.

- [ ] **Step 3: Commit**

```bash
git add tools/integration-suite-multisite/internal/infra/federation_test.go
git commit -m "test(suite-multisite): failing test for LoadFederationSources"
```

---

## Task 17: Implement `federation.go` + Toxiproxy programmatic proxy creation

**Files:**
- Create: `tools/integration-suite-multisite/internal/infra/federation.go`
- Create: `tools/integration-suite-multisite/internal/infra/toxiproxy.go`
- Create: `tools/integration-suite-multisite/catalogs/federation.yaml`

- [ ] **Step 1: Federation loader + Apply**

Create `internal/infra/federation.go`:

```go
package infra

import (
	"context"
	"fmt"
	"os"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"gopkg.in/yaml.v3"
)

type SourceSpec struct {
	On         string `yaml:"on"`
	Stream     string `yaml:"stream"`
	FromStream string `yaml:"from_stream"`
	Filter     string `yaml:"filter"`
}

type federationCatalog struct {
	Sources []SourceSpec `yaml:"sources"`
}

var knownSites = map[string]struct{}{
	"site-a": {},
	"site-b": {},
}

func LoadFederationSources(path string) ([]SourceSpec, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("federation: read %s: %w", path, err)
	}
	var cat federationCatalog
	if err := yaml.Unmarshal(body, &cat); err != nil {
		return nil, fmt.Errorf("federation: parse %s: %w", path, err)
	}
	for i, s := range cat.Sources {
		if _, ok := knownSites[s.On]; !ok {
			return nil, fmt.Errorf("federation: sources[%d].on = %q; must be site-a or site-b", i, s.On)
		}
	}
	return cat.Sources, nil
}

// peerDomain returns "site-b" for "site-a" and vice versa.
func peerDomain(site string) string {
	if site == "site-a" {
		return "site-b"
	}
	return "site-a"
}

// Apply creates JetStream Sources on each target INBOX. Admin conn
// drives both JS domains via WithDomain(s.On). The source Source
// uses Domain = peer site so the gateway knows it's a remote stream.
func Apply(ctx context.Context, specs []SourceSpec, admin *nats.Conn) error {
	for _, s := range specs {
		js, err := jetstream.New(admin, jetstream.WithDomain(s.On))
		if err != nil {
			return fmt.Errorf("federation Apply: js context for %s: %w", s.On, err)
		}
		_, err = js.UpdateStream(ctx, jetstream.StreamConfig{
			Name: s.Stream,
			Sources: []*jetstream.StreamSource{{
				Name:          s.FromStream,
				FilterSubject: s.Filter,
				Domain:        peerDomain(s.On),
			}},
		})
		if err != nil {
			return fmt.Errorf("federation Apply: UpdateStream %s on %s: %w", s.Stream, s.On, err)
		}
	}
	return nil
}

// WaitForStream polls until the named stream exists on the given
// JetStream domain. Used after services come up but before Apply.
func WaitForStream(ctx context.Context, admin *nats.Conn, domain, stream string) error {
	js, err := jetstream.New(admin, jetstream.WithDomain(domain))
	if err != nil {
		return err
	}
	for {
		_, err := js.Stream(ctx, stream)
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for stream %s on %s: %w", stream, domain, ctx.Err())
		case <-time.After(500 * time.Millisecond):
		}
	}
}
```

Add `"time"` to imports.

- [ ] **Step 2: Catalog YAML**

Create `catalogs/federation.yaml`:

```yaml
sources:
  - on: site-b
    stream: INBOX_site-b
    from_stream: OUTBOX_site-a
    filter: outbox.site-a.to.site-b.>
  - on: site-a
    stream: INBOX_site-a
    from_stream: OUTBOX_site-b
    filter: outbox.site-b.to.site-a.>
```

- [ ] **Step 3: Toxiproxy programmatic proxy creation**

Create `internal/infra/toxiproxy.go`:

```go
package infra

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type toxiproxySpec struct {
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"`
	Enabled  bool   `json:"enabled"`
}

// CreateSiteNamedProxies POSTs the 6 site-named proxies to the
// Toxiproxy admin API. Called once at boot, after Toxiproxy is up
// and before services start.
//
// Mongo and Cassandra route through site-specific proxies; both
// CassandraProxy-site-X upstream the same shared cassandra alias.
func CreateSiteNamedProxies(ctx context.Context, adminURL string) error {
	proxies := []toxiproxySpec{
		{Name: "MongoProxy-site-a", Listen: "0.0.0.0:27017", Upstream: "mongo-site-a:27017", Enabled: true},
		{Name: "MongoProxy-site-b", Listen: "0.0.0.0:27018", Upstream: "mongo-site-b:27017", Enabled: true},
		{Name: "CassandraProxy-site-a", Listen: "0.0.0.0:9042", Upstream: "cassandra:9042", Enabled: true},
		{Name: "CassandraProxy-site-b", Listen: "0.0.0.0:9043", Upstream: "cassandra:9042", Enabled: true},
		{Name: "NATSProxy-site-a", Listen: "0.0.0.0:4222", Upstream: "nats-site-a:4222", Enabled: true},
		{Name: "NATSProxy-site-b", Listen: "0.0.0.0:4223", Upstream: "nats-site-b:4222", Enabled: true},
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, p := range proxies {
		body, _ := json.Marshal(p)
		req, _ := http.NewRequestWithContext(ctx, "POST", adminURL+"/proxies", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("toxiproxy: create %s: %w", p.Name, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("toxiproxy: create %s: HTTP %d", p.Name, resp.StatusCode)
		}
	}
	return nil
}
```

Add `"context"` to imports.

- [ ] **Step 4: Run tests**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/internal/infra/... 2>&1 | tail -10
```
Expected: `TestLoadFederationSources_*` tests pass.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-multisite/internal/infra/federation.go \
        tools/integration-suite-multisite/internal/infra/toxiproxy.go \
        tools/integration-suite-multisite/catalogs/federation.yaml
git commit -m "feat(suite-multisite): federation Sources loader + Toxiproxy proxy creator

LoadFederationSources reads catalogs/federation.yaml. Apply uses
jetstream.WithDomain(s.On) on a single admin conn to drive both
domains; the source StreamSource carries Domain = peer site so the
gateway resolves remote streams. WaitForStream polls before Apply
runs (so inbox-worker's BOOTSTRAP_STREAMS has time to create the
INBOX). CreateSiteNamedProxies POSTs all 6 site-named proxies to
Toxiproxy admin API at boot step 2.5; no static JSON config."
```

---

## Task 18: 2-site infra wiring in `stack.go` and `deps.go`

**Files:**
- Modify: `tools/integration-suite-multisite/internal/infra/stack.go`
- Modify: `tools/integration-suite-multisite/internal/infra/deps.go`
- Modify: `tools/integration-suite-multisite/internal/infra/services.go`
- Create: `tools/integration-suite-multisite/internal/infra/nats.gateway.site-a.conf`
- Create: `tools/integration-suite-multisite/internal/infra/nats.gateway.site-b.conf`

This is a large infra refactor; do it carefully.

- [ ] **Step 1: Write per-site NATS gateway config files**

Create `internal/infra/nats.gateway.site-a.conf`:

```
server_name: nats-site-a
cluster: { name: site-a, listen: 0.0.0.0:6222 }
gateway: {
  name: site-a
  port: 7222
  gateways: [
    { name: site-b, url: nats://nats-site-b:7222 }
  ]
}
jetstream: {
  domain: site-a
  store_dir: /data/jetstream
}
```

Create `internal/infra/nats.gateway.site-b.conf` (mirror).

- [ ] **Step 2: `startNATS` parameterized by site**

Edit `internal/infra/deps.go`. Add a `site` parameter to `startNATS`. The container request mounts BOTH `docker-local/nats.conf` AND the per-site gateway config (or composes them).

For simplicity, the new approach: each NATS container's `Cmd` becomes `-c /etc/nats/nats.conf -c /etc/nats/gateway.conf`. Mount both. Use `NetworkAliases` of `nats-site-a` / `nats-site-b` per the site.

```go
func startNATS(ctx context.Context, networkName, repoRoot, site string) (testcontainers.Container, string, error) {
	gatewayConfFile := filepath.Join("internal", "infra", "nats.gateway."+site+".conf")
	req := testcontainers.ContainerRequest{
		Image:        testimages.NATS,
		ExposedPorts: []string{"4222/tcp", "8222/tcp", "7222/tcp"},
		Cmd:          []string{"-c", "/etc/nats/nats.conf", "-c", "/etc/nats/gateway.conf"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"nats-" + site},
		},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      filepath.Join(repoRoot, "docker-local", "nats.conf"),
				ContainerFilePath: "/etc/nats/nats.conf",
				FileMode:          0o400,
			},
			{
				HostFilePath:      filepath.Join(repoRoot, "tools", "integration-suite-multisite", gatewayConfFile),
				ContainerFilePath: "/etc/nats/gateway.conf",
				FileMode:          0o400,
			},
		},
		WaitingFor: wait.ForLog("Server is ready").WithStartupTimeout(60 * time.Second),
	}
	// ... existing GenericContainer call.
	// Return the host-mapped 4222 URL as today.
}
```

- [ ] **Step 3: `startMongo` + `startValkey` per site**

Edit `internal/infra/deps.go`. Add `site` parameter to each. Network alias becomes `mongo-site-a` / `mongo-site-b` / etc. No config changes inside the containers — only the alias differs.

- [ ] **Step 4: `stack.go` Up: boot 2× of each, 1 Cassandra, 1 Toxiproxy**

Edit `internal/infra/stack.go`. In the parallel-deps boot block, replace single calls with per-site:

```go
g.Go(func() error {
	c, url, err := startNATS(gctx, nw.Name, repoRoot, "site-a")
	if err != nil { return err }
	s.deps.natsBySite["site-a"] = c
	s.deps.natsURLBySite["site-a"] = url
	return nil
})
g.Go(func() error {
	c, url, err := startNATS(gctx, nw.Name, repoRoot, "site-b")
	// ...
})
// Same for Mongo, Valkey.
// Cassandra single.
// Toxiproxy single (boots empty; proxies created next).
```

After deps come up:
```go
// Step 2.5: create Toxiproxy site-named proxies programmatically.
if err := CreateSiteNamedProxies(ctx, s.deps.toxiproxyAdmin); err != nil { ... }
```

- [ ] **Step 5: 18 services**

Edit `internal/infra/services.go`. The existing `startService` already takes a `siteID` parameter. Loop over both sites × 9 services in the existing parallel-service block.

Each service's env (`serviceEnv` helper) gets distinct per-site `SITE_ID`, `NATS_URL` (points at its site's NATS), `MONGO_URI` (via its site's Toxiproxy MongoProxy listen port), `CASSANDRA_HOSTS` (via its site's CassandraProxy), `VALKEY_ADDRS` (its site's Valkey).

- [ ] **Step 6: Wait for INBOX streams, then Apply**

After services are up:

```go
admin, err := nats.Connect(s.deps.natsURLBySite["site-a"], nats.UserCredentials(...))
if err != nil { return ... }
defer admin.Drain()

if err := WaitForStream(ctx, admin, "site-a", "INBOX_site-a"); err != nil { return ... }
if err := WaitForStream(ctx, admin, "site-b", "INBOX_site-b"); err != nil { return ... }

specs, err := LoadFederationSources(repoRoot + "/tools/integration-suite-multisite/catalogs/federation.yaml")
if err != nil { return ... }
if err := Apply(ctx, specs, admin); err != nil { return ... }
```

- [ ] **Step 7: Stack accessors**

Add to `internal/infra/stack.go`:

```go
func (s *Stack) NATSURL(site string) string  { return s.deps.natsURLBySite[site] }
func (s *Stack) MongoURI(site string) string { return s.deps.mongoURIBySite[site] }
func (s *Stack) AuthURL(site string) string {
	c, ok := s.services["auth-service-"+site]
	if !ok { return "" }
	// ... existing host:port resolution
}
func (s *Stack) CassandraHostPort() string { return s.deps.cassandraHostPort } // unchanged
func (s *Stack) Sites() []string           { return []string{"site-a", "site-b"} }
```

Update `cmd/runner/main.go`'s `USE_INFRA=true` branch to use these.

- [ ] **Step 8: Build**

Run:
```bash
go build ./tools/integration-suite-multisite/... 2>&1 | head -30
```
Expected: clean.

- [ ] **Step 9: Run unit tests**

Run:
```bash
go test -race -count=1 ./tools/integration-suite-multisite/... 2>&1 | tail -15
```
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add tools/integration-suite-multisite/
git commit -m "feat(suite-multisite): 2-site infra wiring

Per-site NATS containers (supercluster gateway + JS domain). Per-site
Mongo + Valkey. Single shared Cassandra. Single Toxiproxy with 6
site-named proxies created via admin API after Toxiproxy boots.
18 services (9 per site) — each gets distinct SITE_ID, NATS_URL,
MONGO_URI (via its site Toxiproxy), CASSANDRA_HOSTS (via its site
Toxiproxy), VALKEY_ADDRS. After services boot, wait for both INBOX
streams to exist, then Apply federation Sources from catalog.

Stack exposes per-site accessors: NATSURL(site), MongoURI(site),
AuthURL(site). CassandraHostPort() unchanged (shared)."
```

---

## Task 19: Site-aware `jetstream_consume` + `nats_subscribe` + `logs_tail`

**Files:**
- Modify: `tools/integration-suite-multisite/internal/runtime/pollers/jetstream_consume.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/pollers/nats_subscribe.go`
- Modify: `tools/integration-suite-multisite/internal/runtime/pollers/logs_tail.go`

- [ ] **Step 1: `jetstream_consume` keyed by site**

Edit `internal/runtime/pollers/jetstream_consume.go`. Change cache key from `(stream, filter_subject)` to `(site, stream, filter_subject)`. Use `jetstream.New(admin, jetstream.WithDomain(site))` to open the consumer.

- [ ] **Step 2: `nats_subscribe` keyed by site**

Edit `internal/runtime/pollers/nats_subscribe.go`. Change cache key from `subject` to `(site, subject)`. The Warmer opens the subscription using the conn for the named site — but with a single admin conn, the Warmer's "site" is just a logical tag (Core NATS subs flow across the gateway). For multi-site Core NATS subscribes, use the admin conn directly — subject filter naturally limits to the right side.

- [ ] **Step 3: `logs_tail` per-site**

Edit `internal/runtime/pollers/logs_tail.go`. Look up the container by name suffixed with the site (e.g. `room-service-site-a`). Cache key is just the container name (already site-suffixed).

- [ ] **Step 4: Build + tests**

Run:
```bash
go build ./tools/integration-suite-multisite/...
go test -race -count=1 ./tools/integration-suite-multisite/internal/runtime/pollers/... 2>&1 | tail -10
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add tools/integration-suite-multisite/internal/runtime/pollers/
git commit -m "feat(suite-multisite): site-aware jetstream_consume / nats_subscribe / logs_tail

jetstream_consume: cache key (site, stream, filter_subject); admin conn
multiplexes both JS domains via WithDomain(site).
nats_subscribe: cache key (site, subject); admin conn handles both sides
(gateway propagates Core NATS subjects).
logs_tail: container name is already site-suffixed via the YAML; cache
key is the container name."
```

---

## Task 20: First federation scenario YAML + smoke run

**Files:**
- Create: `tools/integration-suite-multisite/scenarios/drafts/room-creates-federates-to-site-b.yaml`

- [ ] **Step 1: Write the scenario**

Create `tools/integration-suite-multisite/scenarios/drafts/room-creates-federates-to-site-b.yaml`:

```yaml
scenario: room-creates-federates-to-site-b
source: spec docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md §8
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: Engineering
    users: ["${alice.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match: { body_json: { status: accepted } }
  - location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { name: Engineering } }
    match: { name: Engineering, createdBy: ${alice.id} }
```

**Note:** this first scenario asserts site-a only — proves the multi-site infra runs a single-site flow without breaking. The cross-site federation assertion is the next scenario; we want a known-passing baseline first per spec §10's risk note.

- [ ] **Step 2: Validate**

Run:
```bash
go run ./tools/integration-suite-multisite/cmd/validator -scenarios tools/integration-suite-multisite/scenarios/drafts 2>&1 | tail -5
```
Expected: `scenarios: ok — 1 drafts parsed`.

- [ ] **Step 3: Commit (the scenario file itself, before trying to run it live)**

```bash
git add tools/integration-suite-multisite/scenarios/drafts/room-creates-federates-to-site-b.yaml
git commit -m "test(suite-multisite): first scenario — single-site happy path on multi-site infra

Asserts alice@site-a creates a room and the row lands in site-a's
Mongo. No federation assertion yet — that's the next scenario, after
we've proven the multi-site infra runs a single-site flow end-to-end."
```

---

## Task 21: Smoke-run the first scenario under `USE_INFRA=true`

**Files:** none modified — operational verification only.

- [ ] **Step 1: Build service images (if not already built)**

Run:
```bash
make build-test-images 2>&1 | tail -5
```
Expected: `chat-local-services-<svc>:latest` tags exist after.

- [ ] **Step 2: Boot infra and run the scenario**

Run:
```bash
cd tools/integration-suite-multisite && USE_INFRA=true make local 2>&1 | tail -40
```
Expected behavior:
- testcontainers boot (~3-5 min cold).
- supercluster handshake reports both gateways connected.
- 6 Toxiproxy proxies created.
- 18 service containers boot.
- WaitForStream for INBOX_site-a and INBOX_site-b succeeds.
- federation.Apply succeeds.
- Scenario `room-creates-federates-to-site-b` runs.
- Report shows 1 scenario pass.

- [ ] **Step 3: Read the report**

Run:
```bash
cat docs/integration-suite-multisite/last-run.md 2>&1 | head -30
```
Expected: a single scenario row, status `pass`, tag `positive`.

- [ ] **Step 4: If the scenario FAILS, debug systematically**

Per spec §10:
1. If the room never lands in site-a's Mongo, the issue is likely in the per-site Mongo wiring or sandbox setup, NOT the federation layer. Check that `room-service-site-a` connects to `mongo-site-a` (logs).
2. If the room lands but the assertion times out, check the `mongo_find` poller's `Sites["site-a"]` lookup — it must point at the right DB.
3. If services don't boot, check trust-chain mount (`docker-local/nats.conf`) and the gateway config file.

Do not proceed to Task 22 until this scenario is reliably green.

- [ ] **Step 5: When green, commit any debug-related fixes**

If you had to fix anything in earlier code to get the smoke to pass, commit each fix with its own descriptive commit. No code changes here are required if the smoke worked first try.

---

## Task 22: Second scenario — the federation tail (cross-site)

**Files:**
- Create: `tools/integration-suite-multisite/scenarios/drafts/room-create-federates-cross-site.yaml`

- [ ] **Step 1: Write the federation scenario**

Create `tools/integration-suite-multisite/scenarios/drafts/room-create-federates-cross-site.yaml`:

```yaml
scenario: room-create-federates-cross-site
source: spec docs/superpowers/specs/2026-06-03-integration-suite-multisite-design.md §8
status: draft
tag: positive

sites:
  site-a:
    seed:
      users:
        alice: { verified: true }
  site-b:
    seed:
      users:
        bob: { verified: true }

input:
  site: site-a
  verb: nats_request
  subject: chat.user.${alice.account}.request.room.site-a.create
  payload:
    name: EngineeringFederated
    users: ["${alice.account}", "${bob.account}"]
  credential: ${alice.credential}

expected:
  - location: reply
    match: { body_json: { status: accepted } }
  - location: mongo_find
    site: site-a
    args: { collection: rooms, filter: { name: EngineeringFederated } }
    match: { name: EngineeringFederated }
  - location: mongo_find
    site: site-b
    args: { collection: rooms, filter: { name: EngineeringFederated } }
    match: { name: EngineeringFederated }
    timeout: 10s
```

- [ ] **Step 2: Run both scenarios**

Run:
```bash
cd tools/integration-suite-multisite && USE_INFRA=true make local 2>&1 | tail -40
```

- [ ] **Step 3: Interpret the outcome**

Three possible outcomes — handle each:

**(a) Both scenarios pass.** Federation works as hypothesized. Commit and move on to Task 23.

**(b) The cross-site scenario times out on the site-b assertion.** This means production code does NOT federate room metadata on create (it might only federate on first message-send). Two options:
- Change the seed to declare a room on both sites' Mongo (sites.site-a.seed.rooms AND sites.site-b.seed.rooms with the same `id`), then fire a message-send instead of a create. The federation tail tests message propagation, not metadata propagation.
- Mark the scenario `tag: negative` if the expected outcome is "this doesn't federate" — but that probably means we picked the wrong test.

**(c) The cross-site scenario fails at the create itself (no room on site-a either).** This is likely a wiring bug in the multi-site infra (wrong Mongo URI, wrong NATS URL, etc.). Debug per Task 21 step 4.

- [ ] **Step 4: Commit the working scenario (or the rewritten version that does work)**

```bash
git add tools/integration-suite-multisite/scenarios/drafts/room-create-federates-cross-site.yaml
git commit -m "test(suite-multisite): federation tail scenario

Alice creates a room on site-a with bob (site-b) as member. Asserts
both the local write on site-a's Mongo AND the federated copy on
site-b's Mongo within 10s. Proves OUTBOX -> INBOX -> inbox-worker
pipeline end-to-end under the suite."
```

---

## Task 23: Verify the single-site tool is still untouched

**Files:** none modified — verification only.

- [ ] **Step 1: Confirm nothing changed under `tools/integration-suite/`**

Run:
```bash
git log --oneline -- tools/integration-suite/ | head -10
```
Expected: no commits since the start of this plan reference `tools/integration-suite/` (only the multi-site fork should have been touched).

- [ ] **Step 2: Verify single-site tests still pass**

Run:
```bash
go test -race -count=1 ./tools/integration-suite/... 2>&1 | tail -10
```
Expected: every package `ok`.

- [ ] **Step 3: Run the single-site tool against its manual stack (if possible)**

```bash
make -C tools/integration-suite validate 2>&1 | tail -3
```
Expected: `catalog: ok` + `scenarios: ok`.

- [ ] **Step 4: Final commit (if any)**

If Step 1 surfaced unintended changes, revert them in their own commit. Otherwise nothing to commit.

---

## Self-Review (run before declaring complete)

This is the writer's review of the plan, not an additional task.

**Spec coverage:** Walked through every section of the design spec:
- §1 decisions (rounds 1-5) — each Round's decision is reflected in at least one task.
- §3 YAML grammar — Tasks 2, 3, 5, 6, 7, 8 cover the loader and validators.
- §4 Sandbox.Setup — Task 14 covers the per-site loops.
- §5.1 Verb executors per-site — Tasks 9, 10.
- §5.2 Pollers — Tasks 11, 12, 19.
- §5.3 Mishaps DEFERRED — no task; spec is explicit.
- §6.1 Infra — Tasks 17, 18.
- §6.1.1 NATS supercluster — Task 18 step 1.
- §6.1.2 Federation catalog — Tasks 16, 17.
- §8 First scenario — Tasks 20, 22.
- §9 Implementation order — this plan follows the 6 steps in §9 with TDD granularity inserted.

**Placeholder scan:** searched for TBD/TODO/etc. — none.

**Type consistency:**
- `Input.Site` defined in Task 10, used in Tasks 12, 14, 15, 19.
- `SiteURLs` map shape consistent in Tasks 10, 15.
- `MongoBySite` / `AuthURLBySite` consistent in Tasks 13, 14, 15.
- `Poller.PollFn(site, args, traceparent)` signature consistent in Tasks 12, 19.
- `ScenarioReport` / `recordScenario` / `Scenarios` field consistent in Task 14.

Plan saved. Ready for execution choice.
