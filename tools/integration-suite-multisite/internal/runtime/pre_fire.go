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
// Working directory: the directory containing the scenario YAML so
// the script's own relative paths are predictable.
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

	dir := filepath.Dir(s.SourcePath)
	env := buildPreFireEnv(s, cfg)

	for i, script := range s.PreFireScripts {
		path := filepath.Join(dir, script)
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
