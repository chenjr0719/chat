package scenario

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// canonical is the staff-engineer example YAML from spec §5.1
// (with banned → unverified per Option B ruling 2026-05-31).
const canonical = `
scenario: "Create Room Sandbox"
source: "design-doc#room-creation"

seed:
  users:
    alice:
      verified: true
    eve_unverified:
      verified: false

base_input:
  verb: nats_request
  subject: "chat.user.${alice.account}.request.room.create"
  payload:
    name: "Engineering Chat"

cases:
  - name: "Happy Path - Room Created Successfully"
    tag: positive
    expected:
      - location: mongo.rooms
        match:
          name: "Engineering Chat"
          owner_id: "${alice.id}"
      - location: reply
        match:
          status: 200

  - name: "Negative - Unverified User Cannot Create Room"
    tag: negative
    input:
      subject: "chat.user.${eve_unverified.account}.request.room.create"
    expected:
      - location: reply
        match:
          status: 403

  - name: "Chaos - Withstands Mongo Partition"
    tag: positive
    mishap: mongo-partition-500ms
    expected:
      - location: mongo.rooms
        match:
          name: "Engineering Chat"
`

func TestScenario_ParsesCanonicalYAML(t *testing.T) {
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(canonical), &s))

	assert.Equal(t, "Create Room Sandbox", s.Name)
	assert.Equal(t, "design-doc#room-creation", s.Source)

	require.Len(t, s.Seed.Users, 2)
	assert.True(t, s.Seed.Users["alice"]["verified"])
	assert.False(t, s.Seed.Users["eve_unverified"]["verified"])

	assert.Equal(t, "nats_request", s.BaseInput.Verb)
	assert.Equal(t, "chat.user.${alice.account}.request.room.create", s.BaseInput.Subject)
	assert.Equal(t, "Engineering Chat", s.BaseInput.Payload["name"])

	require.Len(t, s.Cases, 3)

	// Case 0: happy path with two expected reads.
	c0 := s.Cases[0]
	assert.Equal(t, "Happy Path - Room Created Successfully", c0.Name)
	assert.Equal(t, "positive", c0.Tag)
	assert.Nil(t, c0.Input)
	assert.Empty(t, c0.Mishap)
	require.Len(t, c0.Expected, 2)
	assert.Equal(t, "mongo.rooms", c0.Expected[0].Location)
	assert.Equal(t, "Engineering Chat", c0.Expected[0].Match["name"])
	assert.Equal(t, "${alice.id}", c0.Expected[0].Match["owner_id"])

	// Case 1: negative with input override (subject only).
	c1 := s.Cases[1]
	assert.Equal(t, "negative", c1.Tag)
	require.NotNil(t, c1.Input)
	require.NotNil(t, c1.Input.Subject)
	assert.Equal(t, "chat.user.${eve_unverified.account}.request.room.create", *c1.Input.Subject)
	assert.Nil(t, c1.Input.Payload, "payload not overridden in this case")
	assert.Equal(t, 403, c1.Expected[0].Match["status"])

	// Case 2: chaos.
	c2 := s.Cases[2]
	assert.Equal(t, "mongo-partition-500ms", c2.Mishap)
}

func TestDuration_ParsesGoDurationStrings(t *testing.T) {
	tests := []struct {
		input string
		want  time.Duration
	}{
		{"5s", 5 * time.Second},
		{"100ms", 100 * time.Millisecond},
		{"1m", time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			var d Duration
			require.NoError(t, yaml.Unmarshal([]byte(tt.input), &d))
			assert.Equal(t, tt.want, time.Duration(d))
		})
	}
}

func TestDuration_MissingFieldStaysZero(t *testing.T) {
	// In a parent struct, a missing duration field stays at the zero value.
	type wrap struct {
		Timeout Duration `yaml:"timeout,omitempty"`
	}
	var w wrap
	require.NoError(t, yaml.Unmarshal([]byte("other: x\n"), &w))
	assert.Equal(t, time.Duration(0), time.Duration(w.Timeout))
}

func TestExpected_NotFlagDefaultsFalse(t *testing.T) {
	yamlSrc := `
location: reply
match:
  status: 200
`
	var e Expected
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &e))
	assert.False(t, e.Not)
	assert.Equal(t, time.Duration(0), time.Duration(e.Timeout))
	assert.Equal(t, time.Duration(0), time.Duration(e.Polling))
}

func TestExpected_NotFlagExplicit(t *testing.T) {
	yamlSrc := `
location: logs.room-service
match:
  msg: "should never appear"
not: true
timeout: 2s
polling: 50ms
`
	var e Expected
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &e))
	assert.True(t, e.Not)
	assert.Equal(t, 2*time.Second, time.Duration(e.Timeout))
	assert.Equal(t, 50*time.Millisecond, time.Duration(e.Polling))
}

func TestScenario_EmptySeedAllowed(t *testing.T) {
	yamlSrc := `
scenario: no-seed
source: doc#x
base_input:
  verb: nats_request
  subject: x
  payload: {}
cases: []
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &s))
	assert.Empty(t, s.Seed.Users)
}

// ─── Phase 4.2: SeedRoom + SeedMembership YAML decoding ──────────────────

func TestSeedRoom_DecodesWithExplicitTypeAndName(t *testing.T) {
	yamlSrc := `
seed:
  rooms:
    - id: r-engineering
      name: Engineering Chat
      type: channel
    - id: r-alice-bob-dm
      type: dm
base_input:
  verb: nats_request
  subject: x
  payload: {}
cases: []
scenario: rooms-test
source: doc#x
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &s))
	require.Len(t, s.Seed.Rooms, 2)
	assert.Equal(t, "r-engineering", s.Seed.Rooms[0].ID)
	assert.Equal(t, "Engineering Chat", s.Seed.Rooms[0].Name)
	assert.Equal(t, RoomTypeChannel, s.Seed.Rooms[0].Type)
	assert.Equal(t, RoomTypeDM, s.Seed.Rooms[1].Type)
}

func TestSeedMembership_BareStringShorthand(t *testing.T) {
	// `- r-engineering` is the bare-string sugar for the default-member
	// case; Roles stays empty so the validator's default (member) kicks in.
	yamlSrc := `
seed:
  memberships:
    alice:
      - r-engineering
      - r-design
base_input: {verb: x, subject: y, payload: {}}
cases: []
scenario: bare-strings
source: doc#x
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &s))
	got := s.Seed.Memberships["alice"]
	require.Len(t, got, 2)
	assert.Equal(t, "r-engineering", got[0].Room)
	assert.Empty(t, got[0].Roles, "bare-string form leaves Roles nil; default applied at validation/insertion")
	assert.Equal(t, "r-design", got[1].Room)
}

func TestSeedMembership_FullObjectForm(t *testing.T) {
	yamlSrc := `
seed:
  memberships:
    alice:
      - room: r-engineering
        roles: [owner]
      - room: r-design
        roles: [member, admin]
base_input: {verb: x, subject: y, payload: {}}
cases: []
scenario: object-form
source: doc#x
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &s))
	got := s.Seed.Memberships["alice"]
	require.Len(t, got, 2)
	assert.Equal(t, "r-engineering", got[0].Room)
	assert.Equal(t, []string{"owner"}, got[0].Roles)
	assert.Equal(t, "r-design", got[1].Room)
	assert.Equal(t, []string{"member", "admin"}, got[1].Roles)
}

func TestSeedMembership_MixedBareAndObjectInOneList(t *testing.T) {
	// The CRITICAL ergonomic ask: one user's membership list may
	// freely interleave the two forms.
	yamlSrc := `
seed:
  memberships:
    alice:
      - r-engineering                # bare
      - room: r-design                # object, no roles
      - room: r-product               # object with roles
        roles: [owner]
      - r-platform                   # bare again
base_input: {verb: x, subject: y, payload: {}}
cases: []
scenario: mixed
source: doc#x
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &s))
	got := s.Seed.Memberships["alice"]
	require.Len(t, got, 4)
	assert.Equal(t, "r-engineering", got[0].Room)
	assert.Empty(t, got[0].Roles)
	assert.Equal(t, "r-design", got[1].Room)
	assert.Empty(t, got[1].Roles)
	assert.Equal(t, "r-product", got[2].Room)
	assert.Equal(t, []string{"owner"}, got[2].Roles)
	assert.Equal(t, "r-platform", got[3].Room)
	assert.Empty(t, got[3].Roles)
}

func TestSeedMembership_EmptyScalarRejected(t *testing.T) {
	yamlSrc := `
seed:
  memberships:
    alice: [""]
base_input: {verb: x, subject: y, payload: {}}
cases: []
scenario: empty-bare
source: doc#x
`
	var s Scenario
	err := yaml.Unmarshal([]byte(yamlSrc), &s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty membership scalar")
}

func TestSeedUserFlags_BackwardCompatPreserved(t *testing.T) {
	// Phase 4.2 keeps SeedUserFlags as map[string]bool so sandbox.go's
	// type conversion stays intact. This test pins the existing
	// behavior — `verified: true` parses into the map exactly as before.
	yamlSrc := `
seed:
  users:
    alice:
      verified: true
    eve_unverified:
      verified: false
base_input: {verb: x, subject: y, payload: {}}
cases: []
scenario: flags-back-compat
source: doc#x
`
	var s Scenario
	require.NoError(t, yaml.Unmarshal([]byte(yamlSrc), &s))
	assert.True(t, s.Seed.Users["alice"]["verified"])
	assert.False(t, s.Seed.Users["eve_unverified"]["verified"])
}
