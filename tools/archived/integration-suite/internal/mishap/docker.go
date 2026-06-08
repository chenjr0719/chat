package mishap

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// dockerExec is the production DockerCLI. Each method shells out to
// the `docker` binary on the host. Tests use FakeDockerCLI instead.
//
// Phase 1: DockerCLI is scoped exclusively to the `crash` mishap. All
// network-fault operations moved to ChaosEngine (Toxiproxy).
type dockerExec struct{}

// NewDockerCLI returns the production DockerCLI implementation.
func NewDockerCLI() DockerCLI { return &dockerExec{} }

func (d *dockerExec) Restart(ctx context.Context, container string) error {
	return runDocker(ctx, "restart", container)
}

// StartIfStopped is the Cleanup safety net for the rare case where
// Restart fails partway through (e.g., docker daemon hiccup). Idempotent.
func (d *dockerExec) StartIfStopped(ctx context.Context, container string) error {
	running, err := isRunning(ctx, container)
	if err != nil {
		return fmt.Errorf("check running state: %w", err)
	}
	if running {
		return nil
	}
	return runDocker(ctx, "start", container)
}

func runDocker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, string(out))
	}
	return nil
}

func isRunning(ctx context.Context, container string) (bool, error) {
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", container).CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w: %s", container, err, string(out))
	}
	return strings.TrimSpace(string(out)) == "true", nil
}
