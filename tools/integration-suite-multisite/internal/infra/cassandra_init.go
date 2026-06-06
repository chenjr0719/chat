package infra

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/docker/docker/pkg/stdcopy"
	"github.com/testcontainers/testcontainers-go"
)

// cassandraInit replicates the cassandra-init compose profile: for each
// docker-local/cassandra/init/*.cql file in lexicographic order, copy
// the file into /tmp/init/ inside the Cassandra container and exec
// `cqlsh -f` on it. Non-zero exit aborts the loop with stdout+stderr
// embedded in the returned error.
//
// Idempotent: the CQL is written with CREATE … IF NOT EXISTS, so a
// re-run against an already-initialized container is a no-op at the
// schema level.
func cassandraInit(ctx context.Context, c testcontainers.Container, repoRoot string) error {
	initDir := filepath.Join(repoRoot, "docker-local", "cassandra", "init")
	entries, err := os.ReadDir(initDir)
	if err != nil {
		return fmt.Errorf("read %s: %w", initDir, err)
	}
	var cqlFiles []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".cql" {
			cqlFiles = append(cqlFiles, e.Name())
		}
	}
	sort.Strings(cqlFiles)
	if len(cqlFiles) == 0 {
		return fmt.Errorf("no .cql files in %s", initDir)
	}

	for _, name := range cqlFiles {
		hostPath := filepath.Join(initDir, name)
		containerPath := "/tmp/init/" + name
		if err := c.CopyFileToContainer(ctx, hostPath, containerPath, 0o644); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
		exitCode, reader, err := c.Exec(ctx, []string{"cqlsh", "-f", containerPath})
		if err != nil {
			return fmt.Errorf("exec cqlsh %s: %w", name, err)
		}
		if exitCode != 0 {
			out := readCombinedExecOutput(reader)
			return fmt.Errorf("cqlsh -f %s exited %d: %s", name, exitCode, out)
		}
	}
	return nil
}

// readCombinedExecOutput drains the docker-multiplexed exec stream into
// a single string for embedding in error messages.
func readCombinedExecOutput(r io.Reader) string {
	if r == nil {
		return ""
	}
	var stdout, stderr bytes.Buffer
	_, _ = stdcopy.StdCopy(&stdout, &stderr, r)
	return stdout.String() + stderr.String()
}
