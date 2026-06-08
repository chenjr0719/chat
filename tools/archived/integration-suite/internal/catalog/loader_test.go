package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadVerbCatalog_ParsesYAML(t *testing.T) {
	tmp := t.TempDir()
	verbsDir := filepath.Join(tmp, "verbs")
	require.NoError(t, os.MkdirAll(verbsDir, 0o755))

	yamlData := []byte(`
name: nats_request
description: synchronous NATS request/reply
executor: NATSRequestExecutor
`)
	require.NoError(t, os.WriteFile(filepath.Join(verbsDir, "nats_request.yaml"), yamlData, 0o644))

	c, err := Load(tmp)
	require.NoError(t, err)

	require.Len(t, c.Verbs, 1)
	v := c.Verbs[0]
	assert.Equal(t, "nats_request", v.Name)
	assert.Equal(t, "NATSRequestExecutor", v.Executor)
}

func TestLoadVerbCatalog_EmptyDirectoryProducesEmptyCatalog(t *testing.T) {
	tmp := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(tmp, "verbs"), 0o755))

	c, err := Load(tmp)
	require.NoError(t, err)
	assert.Empty(t, c.Verbs)
}
