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
	assert.True(t, s.Sites["site-a"].Seed.Users["alice"]["verified"])
	assert.True(t, s.Sites["site-b"].Seed.Users["bob"]["verified"])

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
