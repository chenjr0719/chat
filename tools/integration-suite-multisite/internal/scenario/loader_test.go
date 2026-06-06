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
	assert.Equal(t, "tiny", s.(*Scenario).Name)
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
