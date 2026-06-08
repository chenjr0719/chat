package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/matchers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
)

// runnerDeps bundles the runner-startup-built dependencies the case
// loop needs. Built once in Run() and reused for every scenario.
//
// Phase 4.0 universal-primitive design: the runner no longer carries
// per-stream / per-collection / per-container reader handles. Instead,
// it surfaces three backend connections (Mongo + Cassandra + admin
// NATS), one dispatcher-fed reply reader, and the bucket-window knob.
// The universal primitives (mongo_find / cassandra_select /
// jetstream_consume / logs_tail / reply) handle the rest from YAML
// args at assertion time.
type runnerDeps struct {
	Cfg            *Config
	Mongo          *mongo.Database
	Cassandra      *gocql.Session // nil disables cassandra_select
	AdminConn      *nats.Conn     // nil disables jetstream_consume
	Dispatcher     *Dispatcher
	SeedEffectReg  *seedeffect.Registry
	MatcherReg     *matchers.Registry
	MishapRegistry *mishap.Registry
	FactoryByKind  map[string]string
	DockerCLI      mishap.DockerCLI
	ChaosEngine    mishap.ChaosEngine

	// ReplyReader is the dispatcher-fed singleton that backs the
	// `reply` poller (the dispatcher injects directly into it on
	// Fire). Required.
	ReplyReader *readers.NATSReplyReader

	// Service-level credentials (e.g. ${service.backend}). Built from
	// cfg.NATSCredsFile by buildServiceCreds at runner startup.
	Services map[string]Credential

	// MessageBucketWindow tunes cassandra_select.BucketAt. Zero falls
	// back to 24h (production MESSAGE_BUCKET_HOURS).
	MessageBucketWindow time.Duration

	// Per-run sinks.
	Perf   *PerformanceStore
	Report *RunReport

	// afterSetup is a test hook called immediately after Sandbox.Setup
	// returns successfully. Production wiring leaves this nil. Tests
	// use it to mutate the live PollerReg (e.g. register a stub poller
	// so assertions can fire without real readers attached).
	afterSetup func(*Sandbox)
}

// runScenario implements spec §6.7: build Sandbox, Setup, loop
// cases with between-case chaos reset, defer Teardown. Each case
// produces one CaseReport in deps.Report.Cases via recordCase;
// between-case reset failures produce a "skipped" row via
// recordSkipped and continue to the next case.
func runScenario(ctx context.Context, s *scenario.Scenario, deps *runnerDeps) error {
	sbDeps := &SandboxDeps{
		Mongo:               deps.Mongo,
		Cassandra:           deps.Cassandra,
		CassandraKeyspace:   deps.Cfg.CassandraKeyspace,
		AdminConn:           deps.AdminConn,
		MessageBucketWindow: deps.MessageBucketWindow,
		AuthURL:             deps.Cfg.AuthURL,
		SiteID:              deps.Cfg.SiteID,
		Chaos:               deps.ChaosEngine,
		Dispatcher:          deps.Dispatcher,
		SeedEffectReg:       deps.SeedEffectReg,
		ReplyReader:         deps.ReplyReader,
		MatcherReg:          deps.MatcherReg,
		MishapRegistry:      deps.MishapRegistry,
		FactoryByKind:       deps.FactoryByKind,
		DockerCLI:           deps.DockerCLI,
		Services:            deps.Services,
	}
	sb := NewSandbox(s, sbDeps)
	if err := sb.Setup(ctx); err != nil {
		return fmt.Errorf("sandbox setup %s: %w", s.Name, err)
	}
	defer sb.Teardown(context.Background())

	if deps.afterSetup != nil {
		deps.afterSetup(sb)
	}

	for i := range s.Cases {
		c := &s.Cases[i]

		// Between-case chaos reset (mirror runner_phases.go:121-132).
		// Skip-on-failure: a reset error is operationally serious but
		// shouldn't abort the rest of the scenario — record the case
		// as skipped and continue so other cases still report.
		if deps.ChaosEngine != nil {
			resetCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err := deps.ChaosEngine.Reset(resetCtx)
			cancel()
			if err != nil {
				slog.Warn("between-case chaos reset failed; skipping case",
					"scenario", s.Name, "case", c.Name, "err", err)
				recordSkipped(deps.Perf, deps.Report, s, c, "chaos engine reset failed")
				continue
			}
		}

		caseStart := time.Now()
		verdict, err := RunCase(ctx, sb, c)
		if err != nil {
			return fmt.Errorf("run %s/%s: %w", s.Name, c.Name, err)
		}
		recordCase(deps.Perf, deps.Report, s, c, verdict, time.Since(caseStart))
	}
	return nil
}
