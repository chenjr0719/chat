package infra

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
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
	natsBySite   map[string]testcontainers.Container
	mongoBySite  map[string]testcontainers.Container
	valkeyBySite map[string]testcontainers.Container

	cassandra testcontainers.Container
	toxiproxy testcontainers.Container

	natsURLBySite    map[string]string
	mongoURIBySite   map[string]string
	valkeyAddrBySite map[string]string

	cassandraHostPort string
	toxiproxyAdmin    string
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
	// For multi-site, we need each service image once (images are shared).
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
		deps: depHandles{
			natsBySite:       make(map[string]testcontainers.Container),
			mongoBySite:      make(map[string]testcontainers.Container),
			valkeyBySite:     make(map[string]testcontainers.Container),
			natsURLBySite:    make(map[string]string),
			mongoURIBySite:   make(map[string]string),
			valkeyAddrBySite: make(map[string]string),
		},
	}

	// Step 3: deps in parallel — 2× NATS, 2× Mongo, 2× Valkey, 1 Cassandra.
	g, gctx := errgroup.WithContext(ctx)
	var depMu sync.Mutex
	for _, site := range []string{"site-a", "site-b"} {
		site := site
		g.Go(func() error {
			c, url, err := startNATS(gctx, nw.Name, repoRoot, site)
			if err != nil {
				return err
			}
			depMu.Lock()
			s.deps.natsBySite[site] = c
			s.deps.natsURLBySite[site] = url
			depMu.Unlock()
			return nil
		})
		g.Go(func() error {
			c, uri, err := startMongo(gctx, nw.Name, site)
			if err != nil {
				return err
			}
			depMu.Lock()
			s.deps.mongoBySite[site] = c
			s.deps.mongoURIBySite[site] = uri
			depMu.Unlock()
			return nil
		})
		g.Go(func() error {
			c, addr, err := startValkey(gctx, nw.Name, site)
			if err != nil {
				return err
			}
			depMu.Lock()
			s.deps.valkeyBySite[site] = c
			s.deps.valkeyAddrBySite[site] = addr
			depMu.Unlock()
			return nil
		})
	}
	g.Go(func() error {
		c, hp, err := startCassandra(gctx, nw.Name)
		if err != nil {
			return err
		}
		depMu.Lock()
		s.deps.cassandra = c
		s.deps.cassandraHostPort = hp
		depMu.Unlock()
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

	// Step 2.5: create the 6 site-named Toxiproxy proxies via admin API.
	if err := CreateSiteNamedProxies(ctx, admin); err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: toxiproxy site proxies: %w", err)
	}

	// Step 6: services in parallel — 18 total (9 per site).
	g2, g2ctx := errgroup.WithContext(ctx)
	var svcMu sync.Mutex
	for _, site := range []string{"site-a", "site-b"} {
		site := site
		natsURL := s.deps.natsURLBySite[site]
		mongoURI := s.deps.mongoURIBySite[site]
		valkeyAddr := s.deps.valkeyAddrBySite[site]
		for _, svc := range services {
			svc := svc
			g2.Go(func() error {
				svcName := svc + "-" + site
				c, err := startService(g2ctx, nw.Name, svc, tag, site, repoRoot, authSigningKey, cfg.MessageBucketHours, natsURL, mongoURI, valkeyAddr)
				if err != nil {
					return fmt.Errorf("start %s: %w", svcName, err)
				}
				svcMu.Lock()
				s.services[svcName] = c
				svcMu.Unlock()
				return nil
			})
		}
	}
	if err := g2.Wait(); err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: services: %w", err)
	}

	// Step 6.5: wait for both INBOX streams, then apply federation sources.
	federationCatalog := repoRoot + "/tools/integration-suite-multisite/catalogs/federation.yaml"
	if err := applyFederation(ctx, s.deps.natsURLBySite["site-a"], repoRoot, federationCatalog); err != nil {
		s.TerminateAll(context.Background())
		return nil, fmt.Errorf("infra.Up: federation: %w", err)
	}

	logger.Info("infra: stack ready",
		"run_id", runID,
		"services", len(services)*2,
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	return s, nil
}

// applyFederation waits for INBOX streams to be created by inbox-worker
// on both sites and then applies the federation source specs.
func applyFederation(ctx context.Context, natsURL, repoRoot, catalogPath string) error {
	admin, err := nats.Connect(natsURL, nats.Name("integration-suite/federation-admin"))
	if err != nil {
		return fmt.Errorf("federation: connect nats: %w", err)
	}
	defer admin.Drain() //nolint:errcheck

	if err := WaitForStream(ctx, admin, "site-a", "INBOX_site-a"); err != nil {
		return fmt.Errorf("federation: wait INBOX_site-a: %w", err)
	}
	if err := WaitForStream(ctx, admin, "site-b", "INBOX_site-b"); err != nil {
		return fmt.Errorf("federation: wait INBOX_site-b: %w", err)
	}

	specs, err := LoadFederationSources(catalogPath)
	if err != nil {
		return fmt.Errorf("federation: load catalog: %w", err)
	}
	if err := Apply(ctx, specs, admin); err != nil {
		return fmt.Errorf("federation: apply: %w", err)
	}
	return nil
}

// Accessors — per-site

func (s *Stack) NATSURL(site string) string {
	return s.deps.natsURLBySite[site]
}

func (s *Stack) MongoURI(site string) string {
	return s.deps.mongoURIBySite[site]
}

func (s *Stack) CassandraHostPort() string { return s.deps.cassandraHostPort }

func (s *Stack) ValkeyAddrs(site string) string {
	return s.deps.valkeyAddrBySite[site]
}

func (s *Stack) ToxiproxyAdminURL() string { return s.deps.toxiproxyAdmin }
func (s *Stack) NetworkName() string       { return s.network.Name }
func (s *Stack) RunID() string             { return s.runID }

// Sites returns the two site identifiers the stack manages.
func (s *Stack) Sites() []string { return []string{"site-a", "site-b"} }

// AuthURL returns http://host:port for auth-service on the given site.
// Empty if auth-service wasn't started for that site.
func (s *Stack) AuthURL(site string) string {
	c, ok := s.services["auth-service-"+site]
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
// For multi-site services, use the "<svc>-<site>" form (e.g.,
// "auth-service-site-a").
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
	// Per-site Valkey, Mongo, NATS.
	for site, c := range s.deps.valkeyBySite {
		if c == nil {
			continue
		}
		site, c := site, c
		depsWG.Add(1)
		go func() {
			defer depsWG.Done()
			terminateOne(ctx, s.logger, "valkey-"+site, c)
		}()
	}
	for site, c := range s.deps.mongoBySite {
		if c == nil {
			continue
		}
		site, c := site, c
		depsWG.Add(1)
		go func() {
			defer depsWG.Done()
			terminateOne(ctx, s.logger, "mongo-"+site, c)
		}()
	}
	for site, c := range s.deps.natsBySite {
		if c == nil {
			continue
		}
		site, c := site, c
		depsWG.Add(1)
		go func() {
			defer depsWG.Done()
			terminateOne(ctx, s.logger, "nats-"+site, c)
		}()
	}
	// Single shared Cassandra.
	if s.deps.cassandra != nil {
		depsWG.Add(1)
		go func() {
			defer depsWG.Done()
			terminateOne(ctx, s.logger, "cassandra", s.deps.cassandra)
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
