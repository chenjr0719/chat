package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// writeExecutable writes content to dir/name, chmod 0o755. Test helper.
func writeExecutable(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o755))
	return p
}

// TestRunPreFireScripts_RelativePathResolvesAgainstScenarioDir covers
// the Bug 26 regression — cmd.Dir + relative cmd.Path stacked under
// the chdir'd directory and execve hunted in dir/dir/script.sh. The
// test puts the script next to a fake scenario YAML, points
// SourcePath at the YAML, and asserts the script runs successfully
// from a different working directory.
func TestRunPreFireScripts_RelativePathResolvesAgainstScenarioDir(t *testing.T) {
	dir := t.TempDir()
	// scenario YAML doesn't have to be parseable for runPreFireScripts —
	// only SourcePath matters here. The script is what gets executed.
	yamlPath := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("# placeholder"), 0o644))
	writeExecutable(t, dir, "ok.sh", "#!/bin/sh\nexit 0\n")

	s := &scenario.Scenario{
		Name:           "test-rel",
		PreFireScripts: []string{"ok.sh"},
		SourcePath:     yamlPath,
	}

	// Run from a non-scenario CWD to prove the resolution doesn't
	// depend on the caller's working directory.
	saved, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(t.TempDir()))
	defer func() { _ = os.Chdir(saved) }()

	err = runPreFireScripts(context.Background(), s, &Config{})
	assert.NoError(t, err)
}

// TestRunPreFireScripts_AbsolutePathPassesThrough verifies that an
// absolute path in the YAML is execve'd as-is — useful when the
// operator stages a script somewhere outside the scenarios dir.
func TestRunPreFireScripts_AbsolutePathPassesThrough(t *testing.T) {
	scenarioDir := t.TempDir()
	yamlPath := filepath.Join(scenarioDir, "scenario.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("# placeholder"), 0o644))

	elsewhere := t.TempDir()
	abs := writeExecutable(t, elsewhere, "ok.sh", "#!/bin/sh\nexit 0\n")

	s := &scenario.Scenario{
		Name:           "test-abs",
		PreFireScripts: []string{abs},
		SourcePath:     yamlPath,
	}
	err := runPreFireScripts(context.Background(), s, &Config{})
	assert.NoError(t, err)
}

// TestRunPreFireScripts_NonZeroExitFailsWithOutput proves the failure
// path captures stdout+stderr verbatim so diagnosis lives in
// last-run.md rather than in container logs.
func TestRunPreFireScripts_NonZeroExitFailsWithOutput(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("# placeholder"), 0o644))
	writeExecutable(t, dir, "boom.sh", "#!/bin/sh\necho oops >&2\nexit 7\n")

	s := &scenario.Scenario{
		Name:           "test-fail",
		PreFireScripts: []string{"boom.sh"},
		SourcePath:     yamlPath,
	}
	err := runPreFireScripts(context.Background(), s, &Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "boom.sh")
	assert.Contains(t, err.Error(), "exit status 7")
	assert.Contains(t, err.Error(), "oops")
}

// TestRunPreFireScripts_SubsequentScriptsSkippedAfterFailure asserts
// the declared-order single-threaded semantics: once one script
// fails, the remaining list is not executed.
func TestRunPreFireScripts_SubsequentScriptsSkippedAfterFailure(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("# placeholder"), 0o644))
	writeExecutable(t, dir, "boom.sh", "#!/bin/sh\nexit 1\n")
	// If this ran, it would create the file. Test asserts it doesn't exist after.
	sentinel := filepath.Join(dir, "second-ran")
	writeExecutable(t, dir, "second.sh", "#!/bin/sh\ntouch \""+sentinel+"\"\nexit 0\n")

	s := &scenario.Scenario{
		Name:           "test-skip",
		PreFireScripts: []string{"boom.sh", "second.sh"},
		SourcePath:     yamlPath,
	}
	err := runPreFireScripts(context.Background(), s, &Config{})
	require.Error(t, err)

	_, statErr := os.Stat(sentinel)
	assert.True(t, os.IsNotExist(statErr), "second.sh must NOT have run after boom.sh failed")
}

// TestRunPreFireScripts_EnvVarsExposed proves the ISM_ env vars reach
// the script — pinned to a small subset so the assertion stays
// readable but the wiring is verified end-to-end.
func TestRunPreFireScripts_EnvVarsExposed(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "scenario.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte("# placeholder"), 0o644))
	outFile := filepath.Join(dir, "captured")
	writeExecutable(t, dir, "capture.sh",
		"#!/bin/sh\n"+
			"echo \"$ISM_SITE_A_NATS_URL|$ISM_SCENARIO_NAME\" > \""+outFile+"\"\n",
	)

	s := &scenario.Scenario{
		Name:           "env-test",
		PreFireScripts: []string{"capture.sh"},
		SourcePath:     yamlPath,
	}
	cfg := &Config{}
	cfg.SiteA.NATSURL = "nats://example:4222"

	err := runPreFireScripts(context.Background(), s, cfg)
	require.NoError(t, err)
	got, err := os.ReadFile(outFile)
	require.NoError(t, err)
	assert.Equal(t, "nats://example:4222|env-test\n", string(got))
}

// TestRunPreFireScripts_NoScriptsIsNoop guards the early-return path.
func TestRunPreFireScripts_NoScriptsIsNoop(t *testing.T) {
	s := &scenario.Scenario{Name: "no-scripts"}
	assert.NoError(t, runPreFireScripts(context.Background(), s, &Config{}))
}

// TestRunPreFireScripts_RejectsMissingSourcePath catches the wiring
// regression where LoadFile forgets to populate s.SourcePath.
func TestRunPreFireScripts_RejectsMissingSourcePath(t *testing.T) {
	s := &scenario.Scenario{
		Name:           "no-source",
		PreFireScripts: []string{"x.sh"},
		// SourcePath intentionally empty
	}
	err := runPreFireScripts(context.Background(), s, &Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SourcePath")
}
