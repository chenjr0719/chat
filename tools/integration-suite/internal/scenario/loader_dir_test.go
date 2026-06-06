package scenario

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadAllInDir(t *testing.T) {
	tmp := t.TempDir()

	validYAML := []byte(`
scenario: verified_user_creates_channel_room
source: docs/superpowers/specs/example.md
seed:
  users:
    requester:
      verified: true
base_input:
  verb: nats_request
  subject: chat.user.${requester.account}.request.room.${site}.create
  payload:
    name: $auto
  credential: ${requester.credential}
cases:
  - name: room-created
    tag: positive
    expected:
      - location: reply
        match:
          status: 200
`)

	// missing source: citation — should fail validation
	invalidYAML := []byte(`
scenario: bad_scenario
base_input:
  verb: nats_request
  subject: chat.user.test.request.room.site.create
cases:
  - name: x
    tag: positive
    expected:
      - location: reply
        match: {}
`)

	// Create service/ subdir with one valid and one invalid file
	serviceDir := filepath.Join(tmp, "service")
	require.NoError(t, os.MkdirAll(serviceDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(serviceDir, "valid.yaml"), validYAML, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(serviceDir, "invalid.yaml"), invalidYAML, 0o644))

	// Create journey/ subdir with one valid file
	journeyDir := filepath.Join(tmp, "journey")
	require.NoError(t, os.MkdirAll(journeyDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(journeyDir, "valid.yaml"), validYAML, 0o644))

	loaded, errs := LoadAllInDir(tmp)

	assert.Equal(t, 2, loaded, "expected 2 successfully parsed files")
	require.Len(t, errs, 1, "expected exactly 1 error")
	assert.Contains(t, errs[0].Error(), filepath.Join(serviceDir, "invalid.yaml"), "error should contain the failing file's path")
	assert.Contains(t, errs[0].Error(), "source", "error should mention missing source citation")
}

func TestLoadAllInDir_MissingRoot(t *testing.T) {
	loaded, errs := LoadAllInDir("/nonexistent/path/that/does/not/exist")
	assert.Equal(t, 0, loaded)
	assert.Nil(t, errs, "missing root should return nil errors, not an error")
}
