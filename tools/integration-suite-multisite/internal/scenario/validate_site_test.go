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
		Name:     "x",
		Input:    Input{Verb: "nats_request"}, // no Site
		Expected: []Expected{},
	}
	err := ValidateSiteFields(s)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input.site")
}

func TestValidateSiteFields_InputBadSite(t *testing.T) {
	s := &Scenario{
		Name:  "x",
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
			{Location: "reply"},                      // no site, allowed
			{Location: "mongo_find", Site: "site-a"}, // site required, present
			{Location: "mongo_find", Site: "site-b"}, // same
			{Location: "cassandra_select"},           // no site, allowed
		},
	}
	require.NoError(t, ValidateSiteFields(s))
}
