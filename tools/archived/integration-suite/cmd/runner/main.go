// Command runner executes the integration test suite.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/infra"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	rt "github.com/hmchangw/chat/tools/archived/integration-suite/internal/runtime"
)

func main() {
	// run() returns the desired process exit code. main() then calls
	// os.Exit AFTER all of run's defers (including infra.TerminateAll)
	// have unwound — os.Exit skips deferred functions, so calling it
	// inline would leak containers.
	os.Exit(run())
}

func run() int {
	useInfra := os.Getenv("USE_INFRA") == "true"

	reg := mishap.NewRegistry()
	mishap.RegisterCrash(reg)
	mishap.RegisterMongoPartition(reg)
	mishap.RegisterCassandraPartition(reg)

	cfg := &rt.Config{
		MishapRegistry:        reg,
		DockerCLI:             mishap.NewDockerCLI(),
		AuthURL:               env("AUTH_SERVICE_URL", "http://localhost:8080"),
		NATSURL:               env("NATS_URL", "nats://localhost:4222"),
		NATSCredsFile:         env("NATS_CREDS_FILE", ""),
		MongoURI:              env("MONGO_URI", "mongodb://localhost:27017"),
		MongoDB:               env("MONGO_DB", "chat"),
		CassandraHosts:        env("CASSANDRA_HOSTS", ""),
		CassandraKeyspace:     env("CASSANDRA_KEYSPACE", ""),
		MessageBucketHours:    envInt("MESSAGE_BUCKET_HOURS", 24),
		ValkeyAddr:            env("VALKEY_ADDR", ""),
		SiteID:                env("SITE_ID", "site-local"),
		ScenariosDir:          env("SCENARIOS_DIR", "scenarios"),
		CatalogsDir:           env("CATALOGS_DIR", "catalogs"),
		OutputPath:            env("OUTPUT_PATH", "last-run.md"),
		ApprovedOutputPath:    env("APPROVED_OUTPUT_PATH", ""),
		InteractiveOutputPath: env("INTERACTIVE_OUTPUT_PATH", "last-run-interactive.md"),
		PerformancePath:       env("PERFORMANCE_PATH", ""),
		RepoRoot:              env("REPO_ROOT", ""),
		// INTERACTIVE — Phase 4.6 dev-mode opt-in. Default false
		// preserves today's batch sweep (CI flow untouched). Set to
		// "true" to open the stdin-driven menu loop. See
		// docs/spec-scenario-dev-mode.md.
		Interactive: env("INTERACTIVE", "") == "true",
	}

	var (
		adminURL = env("TOXIPROXY_ADMIN_URL", "http://localhost:8474")
		expected = []string{"MongoProxy", "CassandraProxy", "WANProxy"}
	)
	if useInfra {
		// Zero-value Config{} → 9-service stack + NATS/Mongo/Cassandra/
		// Valkey/Toxiproxy. Phase 3.5 cut Elasticsearch + search services.
		stack, err := infra.Up(context.Background(), &infra.Config{
			ImageTag:           env("TEST_IMAGE_TAG", ""),
			SiteID:             cfg.SiteID,
			MessageBucketHours: cfg.MessageBucketHours,
		})
		if err != nil {
			panic(fmt.Errorf("infra.Up: %w", err))
		}
		defer stack.TerminateAll(context.Background())

		cfg.AuthURL = stack.AuthURL()
		cfg.NATSURL = stack.NATSURL()
		cfg.MongoURI = stack.MongoURI()
		// Phase 4.1 — wire the dynamically-mapped Cassandra host:port
		// the testcontainers stack exposes (mirrors the Mongo/Valkey
		// pattern above). Without this the cassandra_select primitive
		// stays disabled under USE_INFRA=true even though the container
		// is healthy.
		cfg.CassandraHosts = stack.CassandraHostPort()
		cfg.ValkeyAddr = stack.ValkeyAddrs()
		adminURL = stack.ToxiproxyAdminURL()
	}

	chaos, err := mishap.NewToxiproxyEngine(adminURL, expected)
	if err != nil {
		panic(fmt.Errorf("toxiproxy unreachable at startup (admin=%s): %w", adminURL, err))
	}
	if err := chaos.Reset(context.Background()); err != nil {
		panic(fmt.Errorf("toxiproxy ResetState failed at startup: %w", err))
	}
	cfg.ChaosEngine = chaos

	report, err := rt.Run(context.Background(), cfg)
	if err != nil {
		slog.Error("run failed", "err", err)
		return 2
	}

	fmt.Printf("Run %s — %d cases\n", report.RunID, len(report.Cases))
	pass, fail := 0, 0
	for i := range report.Cases {
		if report.Cases[i].Verdict.Outcome == "pass" {
			pass++
		} else {
			fail++
		}
	}
	fmt.Printf("pass: %d  fail: %d\n", pass, fail)
	if fail > 0 {
		return 1
	}
	return 0
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// envInt parses an integer env var; unset / unparseable falls back to
// the supplied default. Used for tuning knobs (e.g.
// MESSAGE_BUCKET_HOURS) that don't warrant the noise of failing
// startup on a malformed value.
func envInt(k string, def int) int {
	raw := os.Getenv(k)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		slog.Warn("invalid integer env var; using default", "key", k, "value", raw, "err", err, "default", def)
		return def
	}
	return n
}
