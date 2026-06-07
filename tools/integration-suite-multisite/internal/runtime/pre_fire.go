package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// runPreFireScripts executes each script declared in s.PreFireScripts,
// in declared order, between Sandbox.Setup and Dispatcher.Fire. A
// non-zero exit on any script returns an error; remaining scripts are
// not executed. Per ARCHITECTURE.md §0, the harness runs whatever the
// author declared — it does not interpret, transform, or validate the
// script's intent.
//
// Path resolution: the YAML's `pre_fire_scripts` entries are paths
// relative to the directory containing the scenario YAML. We resolve
// each to an absolute path before exec because cmd.Dir + a relative
// cmd.Path stack: the child fork's chdir(cmd.Dir) happens BEFORE
// execve(cmd.Path), so a relative cmd.Path resolves from inside the
// already-chdir'd directory — double-joining the directory and
// looking in dir/dir/script.sh.
//
// Working directory: dir of the scenario YAML, so the script's own
// relative paths (e.g. helpers it sources) are predictable.
//
// Environment: the script inherits os.Environ() plus a small set of
// ISM_* (Integration-Suite-Multisite) variables exposing the live
// stack's host-mapped URLs, the NATS creds path, and the run/scenario
// identifiers. The script reads whatever it needs.
func runPreFireScripts(ctx context.Context, s *scenario.Scenario, cfg *Config) error {
	if len(s.PreFireScripts) == 0 {
		return nil
	}
	if s.SourcePath == "" {
		return fmt.Errorf("pre_fire_scripts declared but scenario.SourcePath is empty (LoadFile did not populate it)")
	}

	dir, err := filepath.Abs(filepath.Dir(s.SourcePath))
	if err != nil {
		return fmt.Errorf("pre_fire_scripts: resolve scenario dir: %w", err)
	}
	env := buildPreFireEnv(s, cfg)

	for i, script := range s.PreFireScripts {
		path, err := resolveScriptPath(dir, script)
		if err != nil {
			return fmt.Errorf("pre_fire_scripts[%d] %q: %w", i, script, err)
		}
		cmd := exec.CommandContext(ctx, path)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			snippet := truncateOutput(string(out), 4096)
			return fmt.Errorf("pre_fire_scripts[%d] %q: %w; output: %s", i, script, err, snippet)
		}
	}
	return nil
}

// resolveScriptPath turns a YAML-declared script path into the
// absolute file path used for execve. Relative paths join under dir;
// absolute paths are passed through unchanged. Always returns an
// absolute path so the cmd.Dir + cmd.Path stacking bug can't recur.
func resolveScriptPath(dir, script string) (string, error) {
	if filepath.IsAbs(script) {
		return script, nil
	}
	abs, err := filepath.Abs(filepath.Join(dir, script))
	if err != nil {
		return "", fmt.Errorf("resolve script path: %w", err)
	}
	return abs, nil
}

// buildPreFireEnv assembles the env vars exposed to pre-fire scripts.
// Single ISM_ prefix so all suite-injected vars are greppable in the
// script. Inherits the caller's PATH and other env (HOME, USER, etc.)
// so installed tooling (nats CLI, mongosh, cqlsh) is discoverable.
func buildPreFireEnv(s *scenario.Scenario, cfg *Config) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"ISM_SITE_A_NATS_URL="+cfg.SiteA.NATSURL,
		"ISM_SITE_B_NATS_URL="+cfg.SiteB.NATSURL,
		"ISM_NATS_CREDS_FILE="+cfg.NATSCredsFile,
		"ISM_SITE_A_MONGO_URI="+cfg.SiteA.MongoURI,
		"ISM_SITE_B_MONGO_URI="+cfg.SiteB.MongoURI,
		"ISM_CASSANDRA_HOSTS="+cfg.CassandraHosts,
		"ISM_RUN_ID="+currentRunID(),
		"ISM_SCENARIO_NAME="+s.Name,
	)
	return env
}

// truncateOutput trims script output for inclusion in the fail reason.
// 4 KB is enough to surface the typical error line from nats / mongosh
// / cqlsh CLIs without bloating last-run.md.
func truncateOutput(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "... (truncated)"
}
