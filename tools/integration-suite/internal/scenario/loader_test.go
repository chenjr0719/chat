package scenario

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const v3YAML = `
scenario: phase3_new
source: doc#y
seed:
  users:
    alice:
      verified: true
base_input:
  verb: nats_request
  subject: x
  payload: {}
cases:
  - name: happy
    tag: positive
    expected:
      - location: reply
        match:
          status: 200
`

const legacyV2YAML = `
scenario: legacy_v2
source: doc#x
input:
  verb: nats_request
  subject: x
  payload: {}
sequence:
  - service: room-service
    reads:
      - location: reply
        matcher: matches_shape
        expected:
          status: 200
`

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

func TestLoadFile_ReturnsScenario(t *testing.T) {
	p := writeTemp(t, v3YAML)
	got, err := LoadFile(p)
	require.NoError(t, err)
	s, ok := got.(*Scenario)
	require.True(t, ok, "expected *Scenario, got %T", got)
	assert.Equal(t, "phase3_new", s.Name)
	assert.Len(t, s.Cases, 1)
}

func TestLoadFile_LegacyV2Rejected(t *testing.T) {
	// Phase 3 burnt the v2 path. A scenario YAML with `sequence:` and no
	// `cases:` must surface a clear migration error.
	p := writeTemp(t, legacyV2YAML)
	_, err := LoadFile(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "v2")
}

func TestLoadFile_MissingCasesRejected(t *testing.T) {
	emptyYAML := `
scenario: empty
source: doc#z
`
	p := writeTemp(t, emptyYAML)
	_, err := LoadFile(p)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cases")
}
