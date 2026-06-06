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
