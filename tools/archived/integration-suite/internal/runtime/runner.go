package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/hmchangw/chat/pkg/cassutil"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/catalog"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/matchers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/verbs"
	"github.com/hmchangw/chat/tools/archived/integration-suite/seed"
)

// Config holds the runner's wiring.
type Config struct {
	AuthURL            string
	NATSURL            string
	NATSCredsFile      string // optional; when set the runner opens an admin connection and wires JetStream readers
	MongoURI           string
	MongoDB            string
	CassandraHosts     string // Phase 3.9: optional comma-separated host:port; when set the runner opens a chat-keyspace session for cassandra.* pollers
	CassandraKeyspace  string // Phase 3.9: defaults to "chat" when empty
	MessageBucketHours int    // Phase 3.9: must match production MESSAGE_BUCKET_HOURS to keep partition keys aligned; 0 → 24h
	ValkeyAddr         string // optional; when set the runner seeds pure-room-worker room keys at startup
	SiteID             string
	ScenariosDir       string
	CatalogsDir        string
	OutputPath         string
	ApprovedOutputPath string // optional; when set, writes a filtered approved-only report
	// InteractiveOutputPath is where the menu loop flushes the
	// in-memory report after every pick. Distinct from OutputPath so
	// interactive picks NEVER overwrite the canonical full-suite
	// snapshot CI + humans read. Empty disables the flush entirely;
	// the menu does not fall back to OutputPath.
	InteractiveOutputPath string
	PerformancePath       string // optional; persistent per-test latest/best/worst pass history across runs
	RepoRoot              string // optional; passed to git for HEAD + latest-merge commit lookup

	// Interactive, when true, drops into a stdin-driven menu loop AFTER
	// connections are opened and scenarios are discovered, INSTEAD of
	// running the full sweep. Today's batch flow (sweep → reports →
	// exit) corresponds to Interactive=false; this is the default and
	// CI flows MUST stay on this path. See
	// docs/spec-scenario-dev-mode.md for the full UX + lifecycle.
	Interactive bool

	// MishapRegistry holds the per-kind factories registered at runner
	// startup (cmd/runner/main.go). When nil, cases with a `mishap:`
	// field fail at lookup time — there's no skip-fallback in Phase 3.
	MishapRegistry *mishap.Registry

	// DockerCLI is the toolkit primitive crash executors consume via
	// mishap.FactoryContext.DockerCLI. nil tolerated when no case
	// uses the crash kind.
	DockerCLI mishap.DockerCLI

	// ChaosEngine is the Toxiproxy-backed toolkit primitive partition
	// executors consume via mishap.FactoryContext.ChaosEngine. nil
	// tolerated when no case uses a partition kind. The case
	// loop also calls Reset between cases when non-nil; that single
	// integration is what brings the Phase 1 chaos discipline through
	// to Phase 3 unchanged.
	ChaosEngine mishap.ChaosEngine
}

// session bundles every long-lived resource the runner builds at
// startup — connections, catalog, registries, deps — so both the
// batch sweep (today's behavior) and the Phase 4.6 interactive menu
// can call into the same per-scenario unit of work without
// re-opening connections.
//
// Lifecycle: buildSession opens everything; drainSession closes it.
// All callers MUST defer drainSession; the explicit close beats
// scattered per-resource defers when the menu loop holds onto the
// session across many scenario picks.
type session struct {
	Cfg       *Config
	Catalog   *catalog.Catalog
	Mongo     *mongo.Client
	AdminConn *nats.Conn     // nil-tolerant; jetstream_consume + nats_subscribe degrade
	Cassandra *gocql.Session // nil-tolerant
	Perf      *PerformanceStore
	Report    *RunReport

	Deps *runnerDeps // assembled bundle the per-scenario runScenario consumes
}

// Run executes the suite. Two modes, chosen by cfg.Interactive:
//
//   - false (default, CI flow): load catalog, open connections,
//     walk every scenario, run each case against a per-scenario
//     Sandbox, write reports, exit.
//   - true (author-iteration mode): load catalog, open connections,
//     discover scenarios, drop into a stdin-driven
//     menu loop. Nothing runs until the operator picks a scenario.
//     Connections stay warm across picks. Quit drains everything.
//
// See docs/spec-scenario-dev-mode.md.
func Run(ctx context.Context, cfg *Config) (*RunReport, error) {
	sess, err := buildSession(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer drainSession(ctx, sess)

	scenarioFiles, err := findScenarios(cfg.ScenariosDir)
	if err != nil {
		return nil, fmt.Errorf("find scenarios: %w", err)
	}

	if cfg.Interactive {
		if err := runMenuLoop(ctx, sess, scenarioFiles); err != nil {
			return nil, err
		}
		// Menu loop persists reports per-pick; nothing additional to
		// emit at exit.
		return sess.Report, nil
	}

	if err := runSweep(ctx, sess, scenarioFiles); err != nil {
		return nil, err
	}
	return sess.Report, nil
}

// buildSession opens all long-lived resources the runner needs and
// returns a populated *session. Extracted from the pre-Phase-4.6
// Run() body verbatim — same warn-and-degrade discipline for
// nil-tolerant deps (NATS creds file, Cassandra hosts, Valkey).
func buildSession(ctx context.Context, cfg *Config) (*session, error) {
	runID := newRunID()
	SetRunID(runID)
	report := &RunReport{
		RunID:    runID,
		StartISO: time.Now().UTC().Format(time.RFC3339),
	}

	cat, err := catalog.Load(cfg.CatalogsDir)
	if err != nil {
		return nil, fmt.Errorf("load catalogs: %w", err)
	}

	mongoClient, err := mongo.Connect(options.Client().ApplyURI(cfg.MongoURI))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}

	// Optional Valkey key seeding for pure room-worker scenarios. When
	// VALKEY_ADDR is set the runner connects once at startup and writes
	// a fresh keypair for every roomID listed in seed/room-keys.json.
	// Keys are stable across scenarios (Mongo wipe doesn't affect them);
	// pure-room-worker scenarios publish canonical events with one of
	// these literal roomIDs so room-worker's Valkey gate passes.
	if cfg.ValkeyAddr != "" {
		keyStore, err := buildValkeyKeyStore(cfg.ValkeyAddr)
		if err != nil {
			_ = mongoClient.Disconnect(ctx)
			return nil, fmt.Errorf("valkey connect: %w", err)
		}
		if err := seed.LoadRoomKeys(ctx, keyStore); err != nil {
			_ = mongoClient.Disconnect(ctx)
			return nil, fmt.Errorf("seed room keys: %w", err)
		}
	} else {
		slog.Warn("VALKEY_ADDR not set; pure-room-worker scenarios that need a pre-seeded room key will fail at room-worker's Valkey gate")
	}

	verbReg := verbs.NewRegistry()
	verbReg.Register("NATSRequestExecutor", verbs.NewNATSRequest(cfg.NATSURL))
	verbReg.Register("JetStreamPublishExecutor", verbs.NewJetStreamPublish(cfg.NATSURL))

	matcherReg := matchers.NewRegistry()

	// Phase 4.0: only the dispatcher-fed reply reader is built here.
	// All other pollers are universal primitives (mongo_find /
	// cassandra_select / jetstream_consume / logs_tail) — they don't
	// need per-location reader handles, just the backend connections.
	replyReader := readers.NewNATSReplyReader()

	// Optional admin NATS connection — used by the `jetstream_consume`
	// primitive to open per-args ephemeral consumers at scenario time.
	// Disabled when NATSCredsFile is unset; jetstream_consume still
	// registers but warns at PollFn time.
	var adminConn *nats.Conn
	if cfg.NATSCredsFile != "" {
		conn, err := nats.Connect(cfg.NATSURL, nats.UserCredentials(cfg.NATSCredsFile), nats.Name("integration-suite/runner"))
		if err != nil {
			_ = mongoClient.Disconnect(ctx)
			return nil, fmt.Errorf("nats admin connect: %w", err)
		}
		adminConn = conn
	} else {
		slog.Warn("NATS_CREDS_FILE not set; jetstream_consume disabled (scenarios that reference it will warn + time out at assertion time)")
	}

	// Optional Cassandra session for the `cassandra_select` primitive.
	// Same warn-and-timeout pattern.
	var cassSess *gocql.Session
	if cfg.CassandraHosts != "" {
		keyspace := cfg.CassandraKeyspace
		if keyspace == "" {
			keyspace = "chat"
		}
		s, err := cassutil.Connect(cassutil.Config{
			Hosts:    cfg.CassandraHosts,
			Keyspace: keyspace,
		})
		if err != nil {
			_ = mongoClient.Disconnect(ctx)
			if adminConn != nil {
				_ = adminConn.Drain()
			}
			return nil, fmt.Errorf("cassandra connect: %w", err)
		}
		cassSess = s
	} else {
		slog.Warn("CASSANDRA_HOSTS not set; cassandra_select disabled (scenarios that reference it will warn + time out at assertion time)")
	}

	// Seed-effect registry — closed catalog the sandbox materializes
	// users against. Built-ins ship with VerifiedEffect; future effects
	// register here.
	seedEffectReg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(seedEffectReg)

	dispatcher := &Dispatcher{VerbReg: verbReg, ReplyRdr: replyReader}

	// Phased runner (§4.3.1): load performance history up-front so each
	// scenario's case expansion can consult it for history-aware ignore
	// (§4.3.3). When PerformancePath is unset, an empty in-memory store
	// is used and nothing is persisted at the end.
	perf := NewPerformanceStore()
	if cfg.PerformancePath != "" {
		loaded, err := LoadPerformance(cfg.PerformancePath)
		if err != nil {
			_ = mongoClient.Disconnect(ctx)
			if adminConn != nil {
				_ = adminConn.Drain()
			}
			if cassSess != nil {
				cassutil.Close(cassSess)
			}
			return nil, fmt.Errorf("load performance: %w", err)
		}
		perf = loaded
	}
	perf.LastRunAt = report.StartISO

	// Map kind → factory name from the loaded mishap catalog. RunCase
	// consults this when c.Mishap is set.
	factoryByKind := buildFactoryByKind(cat)

	// deps bundle — built once, reused per scenario.
	// Phase 4.0 universal-primitive design: only the dispatcher's
	// reply reader + the three backend connections (Mongo, Cassandra,
	// admin NATS) need wiring; per-stream / per-collection / per-
	// container handles are gone.
	messageBucketWindow := time.Duration(cfg.MessageBucketHours) * time.Hour
	deps := &runnerDeps{
		Cfg:                 cfg,
		Mongo:               mongoClient.Database(cfg.MongoDB),
		Cassandra:           cassSess,
		AdminConn:           adminConn,
		MessageBucketWindow: messageBucketWindow,
		Dispatcher:          dispatcher,
		SeedEffectReg:       seedEffectReg,
		MatcherReg:          matcherReg,
		MishapRegistry:      cfg.MishapRegistry,
		FactoryByKind:       factoryByKind,
		DockerCLI:           cfg.DockerCLI,
		ChaosEngine:         cfg.ChaosEngine,
		ReplyReader:         replyReader,
		Services:            buildServiceCreds(cfg),
		Perf:                perf,
		Report:              report,
	}

	return &session{
		Cfg:       cfg,
		Catalog:   cat,
		Mongo:     mongoClient,
		AdminConn: adminConn,
		Cassandra: cassSess,
		Perf:      perf,
		Report:    report,
		Deps:      deps,
	}, nil
}

// drainSession closes every long-lived resource buildSession opened.
// Safe to call on a partially-built session — guards individual
// nil-tolerant fields. Both batch and menu paths defer this exactly
// once per Run invocation.
func drainSession(ctx context.Context, sess *session) {
	if sess == nil {
		return
	}
	if sess.Mongo != nil {
		_ = sess.Mongo.Disconnect(ctx)
	}
	if sess.AdminConn != nil {
		_ = sess.AdminConn.Drain()
	}
	if sess.Cassandra != nil {
		cassutil.Close(sess.Cassandra)
	}
}

// runSweep executes the today's-behavior batch sweep — load every
// scenario, run it, append to the report, write reports at the end.
// Returns nil on success or the first scenario-load / run error.
func runSweep(ctx context.Context, sess *session, scenarioFiles []string) error {
	startTime := time.Now()

	for _, sf := range scenarioFiles {
		item, err := scenario.LoadFile(sf)
		if err != nil {
			return fmt.Errorf("load scenario %s: %w", sf, err)
		}
		s, ok := item.(*scenario.Scenario)
		if !ok {
			slog.Warn("skipping unknown scenario type",
				"path", sf, "type", fmt.Sprintf("%T", item))
			continue
		}
		if err := runScenario(ctx, s, sess.Deps); err != nil {
			return err
		}
	}

	sess.Report.Duration = time.Since(startTime).Round(time.Millisecond).String()

	// Git info for the report heading.
	if sess.Cfg.RepoRoot != "" {
		sess.Report.Git = CollectGitInfo(sess.Cfg.RepoRoot)
	}

	// Persist performance history if a path was configured.
	if sess.Cfg.PerformancePath != "" {
		if err := SavePerformance(sess.Cfg.PerformancePath, sess.Perf); err != nil {
			return fmt.Errorf("save performance: %w", err)
		}
		sess.Report.Perf = sess.Perf
	}

	return writeReports(sess)
}

// writeReports flushes sess.Report to disk per Cfg.OutputPath and
// Cfg.ApprovedOutputPath. Used by the batch sweep (once at end).
// The interactive menu does NOT call this — see
// writeInteractiveReports.
func writeReports(sess *session) error {
	if sess.Cfg.OutputPath != "" {
		if err := Write(sess.Cfg.OutputPath, sess.Report); err != nil {
			return fmt.Errorf("write report: %w", err)
		}
	}
	if sess.Cfg.ApprovedOutputPath != "" {
		if err := WriteApproved(sess.Cfg.ApprovedOutputPath, sess.Report); err != nil {
			return fmt.Errorf("write approved report: %w", err)
		}
	}
	return nil
}

// writeInteractiveReports flushes sess.Report ONLY to
// Cfg.InteractiveOutputPath. Called by the menu loop after every
// pick. cfg.OutputPath + cfg.ApprovedOutputPath are NEVER touched —
// the canonical full-suite snapshot (consumed by CI + humans) stays
// byte-identical across an interactive session, even though the
// in-memory sess.Report only carries the picks. Empty
// InteractiveOutputPath is a no-op (no fallback to OutputPath, by
// design — falling back would re-introduce the clobber).
func writeInteractiveReports(sess *session) error {
	if sess.Cfg.InteractiveOutputPath == "" {
		return nil
	}
	if err := Write(sess.Cfg.InteractiveOutputPath, sess.Report); err != nil {
		return fmt.Errorf("write interactive report: %w", err)
	}
	return nil
}

// buildFactoryByKind builds a kind-name → factory-name map from the
// loaded mishap catalog. RunCase consults this when c.Mishap is set
// to resolve which Factory to invoke.
func buildFactoryByKind(cat *catalog.Catalog) map[string]string {
	out := map[string]string{}
	for _, m := range cat.Mishaps {
		if m.Name == "" || m.Factory == "" {
			continue
		}
		out[m.Name] = m.Factory
	}
	return out
}

// buildServiceCreds returns the service-credential map exposed via
// ${service.<name>.credential} in scenarios. Today: only the chatapp
// "backend" service-level NATS creds file (cfg.NATSCredsFile). When
// that env var is unset, the map is empty and scenarios referencing
// ${service.*} fail at credential resolution — the documented loud-
// failure path.
func buildServiceCreds(cfg *Config) map[string]Credential {
	out := map[string]Credential{}
	if cfg.NATSCredsFile != "" {
		out["backend"] = Credential{
			Account:   "backend",
			CredsFile: cfg.NATSCredsFile,
		}
	}
	return out
}

func newRunID() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func findScenarios(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ".yaml" {
			out = append(out, path)
		}
		return nil
	})
	if err != nil && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	return out, nil
}
