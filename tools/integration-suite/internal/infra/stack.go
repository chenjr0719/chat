package infra

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"golang.org/x/sync/errgroup"
)

// Stack is the live infrastructure handle returned by Up.
type Stack struct {
	cfg        *Config
	logger     *slog.Logger
	runID      string
	network    *testcontainers.DockerNetwork
	deps       depHandles
	services   map[string]testcontainers.Container
	terminated sync.Once
}

type depHandles struct {
	nats      testcontainers.Container
	mongo     testcontainers.Container
	cassandra testcontainers.Container
	toxiproxy testcontainers.Container
	valkey    testcontainers.Container

	natsURL           string
	mongoURI          string
	cassandraHostPort string
	toxiproxyAdmin    string
	valkeyAddr        string
}

// Up brings the full stack online and returns a Stack the runner can
// query for URLs.
//
// Config is passed by pointer (gocritic hugeParam: 88 bytes). Zero
// value &Config{} boots the full stack with the 9 default
// microservices + Valkey (Cassandra, Mongo, NATS, Toxiproxy are
// always-on).
//
// On failure between any step, partial state is torn down (best-effort)
// and the error is returned. The caller should also defer
// stack.TerminateAll(ctx) to cover late panics.
func Up(ctx context.Context, cfg *Config) (*Stack, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	services := resolveServices(cfg)

	tag := resolveImageTag(cfg)
	repoRoot, err := resolveRepoRoot(cfg)
	if err != nil {
		return nil, err
	}
	logger := resolveLogger(cfg)

	// Fast-fail: verify the NATS trust-chain files exist BEFORE booting
	// any container. Without them auth-service panics on startup and
	// every other service fails its NATS handshake — saves the operator
	// ~90s of Cassandra cold-start before the real error surfaces.
	if err := verifyNATSTrustChain(repoRoot); err != nil {
		return nil, err
	}
	authSigningKey, err := loadAuthSigningKey(repoRoot)
	if err != nil {
		return nil, err
	}

	// Pre-flight: every service image must be on the daemon already.
	missing, err := inspectImages(ctx, requiredImages(services, tag))
	if err != nil {
		return nil, err
	}
	if err := reportMissingImages(missing); err != nil {
		return nil, err
	}

	start := time.Now()
	nw, runID, err := createNetwork(ctx)
	if err != nil {
		return nil, err
	}
	logger.Info("infra: network created", "name", nw.Name, "run_id", runID)

	s := &Stack{
		cfg:      cfg,
		logger:   logger,
		runID:    runID,
		network:  nw,
		services: map[string]testcontainers.Container{},
	}

	// Step 3: deps in parallel.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		c, url, err := startNATS(gctx, nw.Name, repoRoot)
		if err != nil {
			return err
		}
		s.deps.nats = c
		s.deps.natsURL = url
		return nil
	})
	g.Go(func() error {
		c, uri, err := startMongo(gctx, nw.Name)
		if err != nil {
			return err
		}
		s.deps.mongo = c
		s.deps.mongoURI = uri
		return nil
	})
	g.Go(func() error {
		c, hp, err := startCassandra(gctx, nw.Name)
		if err != nil {
			return err
		}
		s.deps.cassandra = c
		s.deps.cassandraHostPort = hp
		return nil
	})
	// Valkey is always-on in Phase 3.5: room-service + room-worker both
	// require it for the key cache + key gating. Elasticsearch was
	// removed alongside the search subsystem.
	g.Go(func() error {
		c, addr, err := startValkey(gctx, nw.Name)
		if err != nil {
			return err
		}
		s.deps.valkey = c
		s.deps.valkeyAddr = addr
		return nil
	})
	if err := g.Wait(); err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: deps: %w", err)
	}

	// Step 4: Cassandra schema init (after Cassandra is up).
	if err := cassandraInit(ctx, s.deps.cassandra, repoRoot); err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: cassandra init: %w", err)
	}

	// Step 5: Toxiproxy (after Mongo + Cassandra so its upstreams resolve).
	tc, admin, err := startToxiproxy(ctx, nw.Name, repoRoot)
	if err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: toxiproxy: %w", err)
	}
	s.deps.toxiproxy = tc
	s.deps.toxiproxyAdmin = admin

	// Step 6: services in parallel.
	siteID := resolveSiteID(cfg)
	g2, g2ctx := errgroup.WithContext(ctx)
	var svcMu sync.Mutex
	for _, svc := range services {
		svc := svc
		g2.Go(func() error {
			c, err := startService(g2ctx, nw.Name, svc, tag, siteID, repoRoot, authSigningKey, cfg.MessageBucketHours)
			if err != nil {
				return fmt.Errorf("start %s: %w", svc, err)
			}
			svcMu.Lock()
			s.services[svc] = c
			svcMu.Unlock()
			return nil
		})
	}
	if err := g2.Wait(); err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: services: %w", err)
	}

	logger.Info("infra: stack ready",
		"run_id", runID,
		"services", len(services),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	return s, nil
}

// Accessors

func (s *Stack) NATSURL() string           { return s.deps.natsURL }
func (s *Stack) MongoURI() string          { return s.deps.mongoURI }
func (s *Stack) CassandraHostPort() string { return s.deps.cassandraHostPort }
func (s *Stack) ValkeyAddrs() string       { return s.deps.valkeyAddr }
func (s *Stack) ToxiproxyAdminURL() string { return s.deps.toxiproxyAdmin }
func (s *Stack) NetworkName() string       { return s.network.Name }
func (s *Stack) RunID() string             { return s.runID }

// AuthURL is http://host:port for auth-service. Empty if auth-service
// wasn't started.
func (s *Stack) AuthURL() string {
	c, ok := s.services["auth-service"]
	if !ok || c == nil {
		return ""
	}
	ctx := context.Background()
	host, err := c.Host(ctx)
	if err != nil {
		return ""
	}
	port, err := c.MappedPort(ctx, "8080")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("http://%s:%s", host, port.Port())
}

// ServiceContainer returns the testcontainers.Container handle for
// the named service. Returns nil if the service isn't in this stack.
func (s *Stack) ServiceContainer(name string) testcontainers.Container {
	return s.services[name]
}

// TerminateAll stops every container started by Up in reverse-dep
// order. Idempotent best-effort.
func (s *Stack) TerminateAll(ctx context.Context) {
	if s == nil {
		return
	}
	s.terminated.Do(func() {
		s.teardown(ctx)
	})
}

func (s *Stack) teardown(ctx context.Context) {
	var wg sync.WaitGroup
	for name, c := range s.services {
		if c == nil {
			continue
		}
		wg.Add(1)
		go func(name string, c testcontainers.Container) {
			defer wg.Done()
			tCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			if err := c.Terminate(tCtx); err != nil {
				s.logger.Warn("infra: terminate service", "service", name, "err", err)
			}
		}(name, c)
	}
	wg.Wait()

	if s.deps.toxiproxy != nil {
		terminateOne(ctx, s.logger, "toxiproxy", s.deps.toxiproxy)
	}

	var depsWG sync.WaitGroup
	for _, ent := range []struct {
		name string
		c    testcontainers.Container
	}{
		{"valkey", s.deps.valkey},
		{"cassandra", s.deps.cassandra},
		{"mongo", s.deps.mongo},
		{"nats", s.deps.nats},
	} {
		if ent.c == nil {
			continue
		}
		ent := ent
		depsWG.Add(1)
		go func() {
			defer depsWG.Done()
			terminateOne(ctx, s.logger, ent.name, ent.c)
		}()
	}
	depsWG.Wait()

	if s.network != nil {
		nCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.network.Remove(nCtx); err != nil {
			s.logger.Warn("infra: remove network", "name", s.network.Name, "err", err)
		}
	}
}

func terminateOne(ctx context.Context, logger *slog.Logger, name string, c testcontainers.Container) {
	tCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.Terminate(tCtx); err != nil {
		logger.Warn("infra: terminate dep", "name", name, "err", err)
	}
}
