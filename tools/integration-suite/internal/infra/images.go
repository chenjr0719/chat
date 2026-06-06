package infra

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/client"
)

// inspectImages calls the Docker daemon for each tag and returns the
// subset that are not locally present. Pulled-from-registry images
// are NOT auto-pulled — Phase 2's contract is bring-your-own-images.
func inspectImages(ctx context.Context, tags []string) ([]string, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	defer cli.Close()

	var missing []string
	for _, t := range tags {
		_, _, err := cli.ImageInspectWithRaw(ctx, t)
		if err != nil {
			if errIsNotFound(err) {
				missing = append(missing, t)
				continue
			}
			return nil, fmt.Errorf("inspect %s: %w", t, err)
		}
	}
	return missing, nil
}

// errIsNotFound treats any error whose message contains a "no such
// image" form as a missing-image error. Docker's errdefs package has
// stricter checks but they differ across moby/docker versions; the
// substring match is robust to that drift.
func errIsNotFound(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "No such image") ||
		strings.Contains(s, "no such image")
}

// reportMissingImages wraps a non-empty slice into an operator-friendly
// error pointing at the build target. nil/empty input returns nil so
// the caller can chain without branching.
func reportMissingImages(missing []string) error {
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf(
		"infra.Up: %d microservice image(s) not found on docker daemon: %s\n"+
			"Run `make build-test-images` (or `docker compose -f docker-local/compose.services.yaml build`) before starting the suite",
		len(missing),
		strings.Join(missing, ", "),
	)
}

// serviceImage formats the docker tag for a Phase 2 microservice image.
// Matches the default tag produced by docker compose -f
// docker-local/compose.services.yaml build (which uses the file's
// `name: chat-local-services` project name as the tag prefix).
func serviceImage(svc, tag string) string {
	return "chat-local-services-" + svc + ":" + tag
}

// requiredImages builds the full list of microservice image tags
// the stack will need, in the same order as the input services slice.
// Used by the pre-flight check (Task 13) so missing images fail-fast
// with an operator-friendly message before any container starts.
func requiredImages(services []string, tag string) []string {
	out := make([]string, len(services))
	for i, s := range services {
		out[i] = serviceImage(s, tag)
	}
	return out
}
