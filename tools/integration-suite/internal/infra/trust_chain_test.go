package infra

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifyNATSTrustChain_AllPresent(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docker-local"), 0o755))
	for _, name := range natsTrustChainFiles {
		require.NoError(t, os.WriteFile(filepath.Join(root, "docker-local", name), []byte("x"), 0o600))
	}
	assert.NoError(t, verifyNATSTrustChain(root))
}

func TestVerifyNATSTrustChain_MissingFilesReported(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docker-local"), 0o755))
	// Only create one of the three.
	require.NoError(t, os.WriteFile(filepath.Join(root, "docker-local", ".env"), []byte("x"), 0o600))

	err := verifyNATSTrustChain(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "./docker-local/setup.sh", "operator-facing remediation must surface")
	assert.Contains(t, err.Error(), "backend.creds")
	assert.Contains(t, err.Error(), "nats.conf")
	assert.NotContains(t, err.Error(), "docker-local/.env\n", ".env was present, must not be in missing list")
}

func TestLoadEnvFile_ParsesKeyValueLines(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	body := `# this is a comment
AUTH_SIGNING_KEY=SABCDEF1234567890
NATS_URL=nats://nats:4222
NATS_CREDS_FILE=/etc/nats/backend.creds

# trailing comment
DEV_MODE=true
`
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	env, err := loadEnvFile(path)
	require.NoError(t, err)
	assert.Equal(t, "SABCDEF1234567890", env["AUTH_SIGNING_KEY"])
	assert.Equal(t, "nats://nats:4222", env["NATS_URL"])
	assert.Equal(t, "/etc/nats/backend.creds", env["NATS_CREDS_FILE"])
	assert.Equal(t, "true", env["DEV_MODE"])
	assert.NotContains(t, env, "this is a comment", "comment lines must not produce entries")
}

func TestLoadEnvFile_MissingFileReturnsError(t *testing.T) {
	_, err := loadEnvFile(filepath.Join(t.TempDir(), "does-not-exist"))
	require.Error(t, err)
}

func TestLoadAuthSigningKey_HappyPath(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docker-local"), 0o755))
	body := "AUTH_SIGNING_KEY=SAAA111\nDEV_MODE=true\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "docker-local", ".env"), []byte(body), 0o600))

	got, err := loadAuthSigningKey(root)
	require.NoError(t, err)
	assert.Equal(t, "SAAA111", got)
}

func TestLoadAuthSigningKey_EmptyValueReportsActionable(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "docker-local"), 0o755))
	body := "AUTH_SIGNING_KEY=\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "docker-local", ".env"), []byte(body), 0o600))

	_, err := loadAuthSigningKey(root)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup.sh")
}

func TestLoadAuthSigningKey_MissingFile(t *testing.T) {
	_, err := loadAuthSigningKey(t.TempDir())
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), ".env"), "error should name the file: %v", err)
}
