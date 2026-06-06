// Package infra orchestrates the Phase 2/3 test stack: an ephemeral
// Docker network with dependency containers (NATS with the operator
// trust chain, Mongo, Cassandra, Toxiproxy, Valkey) plus the chat
// microservice containers needed for the room/message/history/
// notification/broadcast/inbox subsystems, all reachable via
// production DNS aliases.
//
// Phase 3.5 cut: the search subsystem (search-service, search-sync-
// worker, Elasticsearch) is no longer part of the default boot. The
// integration suite never exercises them, and their boot-time strict
// env validation kept failing the whole stack. Re-introduce by
// adding to Config.Services + booting an ES container manually if a
// future scenario needs them.
package infra

import (
	"log/slog"
	"os"
)

// Config tunes infra.Up. Zero value boots the full stack with the 9
// default microservices the integration suite exercises today.
type Config struct {
	// ImageTag overrides the suffix on chat-local-services-<svc>:<tag>.
	// Empty → resolveImageTag reads TEST_IMAGE_TAG env, then "latest".
	ImageTag string

	// Services overrides the default service set. Empty → all 9.
	// mock-user-service is not in the default set; opt in by listing
	// it explicitly here. Adding search-service or search-sync-worker
	// requires also wiring an Elasticsearch container yourself —
	// Phase 3.5 removed the built-in ES support.
	Services []string

	// RepoRoot is the absolute path to the repo root. Empty → walk up
	// from the test binary until a go.mod with the chat module is found.
	RepoRoot string

	// SiteID is plumbed to every service's SITE_ID env. Default "site-local".
	SiteID string

	// Logger is used for slog.Info(boot timings) and slog.Warn(terminate
	// errors). nil → slog.Default().
	Logger *slog.Logger

	// MessageBucketHours is plumbed into MESSAGE_BUCKET_HOURS for
	// history-service + message-worker so suite seeds and service
	// reads target the same Cassandra partition (CLAUDE.md §6). Zero
	// leaves the env var unset, letting each service fall back to its
	// own envDefault.
	MessageBucketHours int
}

// defaultServices lists the microservices the integration suite
// exercises, in alphabetical order so the resolved slice is
// deterministic. Phase 3.5 dropped search-service + search-sync-worker
// (the suite never tests them and their boot-time strict env validation
// was tripping infra.Up). mock-user-service is intentionally absent —
// callers opt it in via Config.Services.
var defaultServices = []string{
	"auth-service",
	"broadcast-worker",
	"history-service",
	"inbox-worker",
	"message-gatekeeper",
	"message-worker",
	"notification-worker",
	"room-service",
	"room-worker",
}

func resolveImageTag(cfg *Config) string {
	if cfg.ImageTag != "" {
		return cfg.ImageTag
	}
	if t := os.Getenv("TEST_IMAGE_TAG"); t != "" {
		return t
	}
	return "latest"
}

func resolveServices(cfg *Config) []string {
	if len(cfg.Services) > 0 {
		out := make([]string, len(cfg.Services))
		copy(out, cfg.Services)
		return out
	}
	out := make([]string, len(defaultServices))
	copy(out, defaultServices)
	return out
}

func resolveSiteID(cfg *Config) string {
	if cfg.SiteID != "" {
		return cfg.SiteID
	}
	return "site-local"
}

func resolveLogger(cfg *Config) *slog.Logger {
	if cfg.Logger != nil {
		return cfg.Logger
	}
	return slog.Default()
}
