package infra

import (
	"time"

	"github.com/testcontainers/testcontainers-go/wait"
)

// readinessFor returns the wait.Strategy a given service uses to signal
// that it has finished booting. Every signature was confirmed by
// grepping each main.go for its final `slog.Info(…)` startup log.
//
// All workers log to stdout via slog JSON; wait.ForLog matches against
// substrings, so the `"msg":"<svc> running"` (or "started") form is
// uniquely identifying.
//
// HTTP services (auth-service) layer a ForListeningPort check on top.
// mock-user-service is included in the table but not in defaultServices;
// callers opt it in via Config.Services.
func readinessFor(svc string) (wait.Strategy, bool) {
	switch svc {
	case "auth-service":
		return wait.ForAll(
			wait.ForLog(`"msg":"auth service starting"`).
				WithStartupTimeout(60*time.Second),
			wait.ForListeningPort("8080/tcp").
				WithStartupTimeout(60*time.Second),
		), true
	case "broadcast-worker":
		return wait.ForLog(`"msg":"broadcast-worker started"`).
			WithStartupTimeout(90 * time.Second), true
	case "history-service":
		return wait.ForLog(`"msg":"history-service running"`).
			WithStartupTimeout(90 * time.Second), true
	case "inbox-worker":
		return wait.ForLog(`"msg":"inbox-worker started"`).
			WithStartupTimeout(90 * time.Second), true
	case "message-gatekeeper":
		return wait.ForLog(`"msg":"message-gatekeeper running"`).
			WithStartupTimeout(90 * time.Second), true
	case "message-worker":
		return wait.ForLog(`"msg":"message-worker running"`).
			WithStartupTimeout(90 * time.Second), true
	case "notification-worker":
		return wait.ForLog(`"msg":"notification-worker started"`).
			WithStartupTimeout(90 * time.Second), true
	case "room-service":
		return wait.ForLog(`"msg":"room-service running"`).
			WithStartupTimeout(90 * time.Second), true
	case "room-worker":
		return wait.ForLog(`"msg":"room-worker running"`).
			WithStartupTimeout(90 * time.Second), true
	case "mock-user-service":
		// Opt-in only; matches the slog.Info("mock-user-service running", …)
		// emit confirmed in mock-user-service/main.go.
		return wait.ForLog(`"msg":"mock-user-service running"`).
			WithStartupTimeout(60 * time.Second), true
	}
	return nil, false
}
