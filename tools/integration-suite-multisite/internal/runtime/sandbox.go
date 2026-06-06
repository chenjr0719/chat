package runtime

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/mishap"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/runtime/pollers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/seedeffect"
)

// sandboxOwnedCollections is the set of Mongo collections the sandbox
// drops at Setup. Matches today's seed.LoadAll scope plus Phase 4.2's
// room_members (owned by the new insertSeededRooms helper). Future
// seed-effects that own new collections will extend this — per-effect
// declaration via SeedEffectDecl.writes_collections is the planned
// mechanism (Phase 4).
var sandboxOwnedCollections = []string{"users", "rooms", "subscriptions", "room_members"}

// Sandbox is the per-scenario shared state. One Sandbox per
// Scenario; cases run sequentially against it and accumulate effects
// in Mongo.
//
// Lifecycle (driven by runScenario in Task 19):
//
//	sb := NewSandbox(s, deps)
//	if err := sb.Setup(ctx); err != nil { ... }
//	defer sb.Teardown(context.Background())
//	for _, c := range s.Cases {
//	    caseRep := RunCase(ctx, sb, c)
//	    recordCase(perf, report, s, c, caseRep)
//	}
type Sandbox struct {
	Scenario *scenario.Scenario
	Deps     SandboxDeps

	// Populated by Setup, read by RunCase:
	Users        map[string]*seedeffect.SeedUser // alias → materialized user
	Placeholders map[string]map[string]any       // alias → {account, id, jwt, nkey} for substitution
	PollerReg    *pollers.Registry               // location → Poller (Phase 3 Task 15)

	// StartTime is T_open — the session boundary captured at Setup. The
	// poller registry reads it (so mongo_find / jetstream_consume etc.
	// filter to events after the sandbox booted), and Phase 4.2's
	// insertSeededRooms reads it to stamp seeded rooms / subscriptions
	// / room_members with createdAt = updatedAt = joinedAt = ts = T_open.
	StartTime time.Time

	pollerCleanup func() // closes StreamPollers' watcher goroutines; called at Teardown

	terminated sync.Once
}

// SandboxDeps is the resource bundle every Sandbox needs. Built once
// at runner startup and reused for every scenario.
type SandboxDeps struct {
	// Per-site backends: Mongo and Auth are site-scoped.
	MongoBySite   map[string]*mongo.Database
	AuthURLBySite map[string]string

	// Shared: Cassandra is a single cluster serving all sites.
	Cassandra         *gocql.Session
	CassandraKeyspace string

	// MessageBucketWindow tunes the cassandra_select primitive's
	// bucket-math helper (BucketAt). Zero falls back to 24h
	// (production MESSAGE_BUCKET_HOURS).
	MessageBucketWindow time.Duration

	// Chaos is unused per spec §5.3 (chaos disabled) but kept for
	// parity with the single-site shape.
	Chaos         mishap.ChaosEngine
	Dispatcher    *Dispatcher
	SeedEffectReg *seedeffect.Registry

	// ReplyReader is the dispatcher-fed singleton that backs the
	// `reply` poller. The dispatcher's Fire() injects per-fire
	// outcomes into this reader; the poller hands them to assertions.
	// Required (the dispatcher uses it directly).
	ReplyReader *readers.NATSReplyReader

	// AdminConn is the admin NATS connection (operator JWT) used by
	// the `jetstream_consume` primitive to open ephemeral consumers.
	// Single conn multiplexes both JS domains via WithDomain(site).
	// Nil tolerated — scenarios that don't reference jetstream_consume
	// still run; ones that do get a slog warning + assertion timeout.
	AdminConns map[string]*nats.Conn

	// MatcherReg is the matchers registry MatchShape inherits when
	// runScenario wraps each expected[].match into a Gomega matcher. Nil
	// is tolerated: MatchShape falls back to matchers.NewRegistry().
	MatcherReg *matchers.Registry

	// Kept for parity with single-site shape but unused (chaos disabled).
	MishapRegistry *mishap.Registry

	// FactoryByKind maps a mishap kind to the Factory name registered
	// in MishapRegistry. Built from the catalog at runner startup.
	FactoryByKind map[string]string

	// DockerCLI is the container ops handle the crash mishap consumes
	// via mishap.FactoryContext.DockerCLI. Nil tolerated for non-crash
	// scenarios.
	DockerCLI mishap.DockerCLI

	// Services exposes service-level NATS credentials referenced via
	// `${service.<name>.credential}` in scenarios. Populated by the
	// runner from cfg.NATSCredsFile (today's only service is "backend").
	// Empty map (or nil) means service-cred references fail at resolve
	// time, which is the documented "loud failure" path.
	Services map[string]Credential
}

// NewSandbox builds an empty Sandbox attached to the scenario. Returns
// nil if s is nil (defensive — supports defer sb.Teardown(...) after a
// failed Setup where the caller couldn't tell which branch ran).
//
// deps is taken by pointer to keep the struct cheap to pass (it carries
// reader handles, the chaos engine, dispatcher, etc. — currently 128
// bytes). A nil deps is treated as the zero value, matching the
// SandboxDeps{} literal used in nil-scenario probe tests.
func NewSandbox(s *scenario.Scenario, deps *SandboxDeps) *Sandbox {
	if s == nil {
		return nil
	}
	if deps == nil {
		deps = &SandboxDeps{}
	}
	return &Sandbox{
		Scenario:     s,
		Deps:         *deps,
		Users:        map[string]*seedeffect.SeedUser{},
		Placeholders: map[string]map[string]any{},
	}
}

// Setup applies the scenario's seed in this order:
//  1. Validate user-effect flags.
//  2. Validate the room-subscription seed block.
//  3. Validate the cassandra_data seed block.
//  4. Capture sb.StartTime — the single T_open anchor every
//     downstream timestamp (pollers, seeded createdAt, ${now ± d})
//     agrees on.
//  5. Reset the chaos engine.
//  6. Drop sandbox-owned Mongo collections.
//  7. Truncate sandbox-owned Cassandra tables.
//  8. Materialize SeedUsers + apply effects (mints NATS identity).
//  9. Insert minimal user-profile docs.
//  10. Build the Placeholders map — must precede Step 12 so the
//     Cassandra-seed engine can resolve ${alice.id} etc.
//  11. Insert seeded rooms + subscriptions + room_members.
//  12. Insert seeded Cassandra rows — substitution + two-pass token
//     resolution + optional auto-bucket.
//  13. Build the per-scenario poller registry.
//
// Setup is idempotent in shape but not in side-effects — re-running it
// against the same Mongo will re-drop + re-apply. The runner calls
// Setup exactly once per scenario (in runScenario, Task 19).
func (sb *Sandbox) Setup(ctx context.Context) error {
	if sb.Deps.SeedEffectReg == nil {
		return fmt.Errorf("sandbox.Setup: SeedEffectReg is required")
	}

	// Step 1: validate flags against the registry.
	// Collect users from all sites for flag validation; aliases must be
	// globally unique across sites (otherwise ${alice.account} is
	// ambiguous).
	userFlags := map[string]map[string]bool{}
	for _, siteBlock := range sb.Scenario.Sites {
		for alias, flags := range siteBlock.Seed.Users {
			if _, exists := userFlags[alias]; exists {
				return fmt.Errorf("sandbox.Setup: duplicate user alias %q across sites — aliases must be globally unique", alias)
			}
			userFlags[alias] = map[string]bool(flags)
		}
	}
	if err := seedeffect.ValidateFlags(sb.Deps.SeedEffectReg, userFlags); err != nil {
		return fmt.Errorf("sandbox.Setup: validate flags: %w", err)
	}

	// Step 2: validate the room-subscription seed block (per site).
	// Step 3: validate the Cassandra-data seed block (scenario level).
	// (These are validated structurally by the scenario loader; no additional
	// runtime validation needed here beyond what YAML parsing enforces.)

	// Step 4: capture T_open once so every downstream timestamp
	// (poller filter boundary, seeded room/subscription createdAt,
	// etc.) agrees on a single session anchor.
	sb.StartTime = time.Now()

	// Step 5: reset chaos engine (disabled per spec §5.3, but reset
	// if wired to clear any prior state from a previous scenario).
	if sb.Deps.Chaos != nil {
		if err := sb.Deps.Chaos.Reset(ctx); err != nil {
			return fmt.Errorf("sandbox.Setup: chaos reset: %w", err)
		}
	}

	// Step 6: drop sandbox-owned collections per site.
	for site, db := range sb.Deps.MongoBySite {
		if db == nil {
			continue
		}
		for _, name := range sandboxOwnedCollections {
			if err := db.Collection(name).Drop(ctx); err != nil {
				return fmt.Errorf("sandbox.Setup: drop %s on %s: %w", name, site, err)
			}
		}
	}

	// Step 7: truncate sandbox-owned Cassandra tables. Cassandra is shared;
	// one truncation covers all sites.
	if sb.Deps.Cassandra != nil {
		if err := truncateSandboxCassandraTables(ctx, sb); err != nil {
			return fmt.Errorf("sandbox.Setup: %w", err)
		}
	}

	// Step 8: materialize seed users + apply effects, per site.
	// Users are scoped to the site they're declared in; aliases are
	// unique across sites (validated in Step 1).
	for siteName, siteBlock := range sb.Scenario.Sites {
		authURL := sb.Deps.AuthURLBySite[siteName]
		db := sb.Deps.MongoBySite[siteName]

		for alias, flags := range siteBlock.Seed.Users {
			u := &seedeffect.SeedUser{
				Alias:   alias,
				Account: alias,
				ID:      "u-" + alias,
			}

			// Mint a NATS identity. AuthURL empty is surfaced loudly so the
			// operator sees the missing-config error rather than silent empty creds.
			if authURL != "" {
				jwt, nkey, err := seedeffect.MintNATSIdentity(ctx, u.Account, authURL)
				if err != nil {
					return fmt.Errorf("sandbox.Setup: mint creds for %q on %s: %w", alias, siteName, err)
				}
				u.JWT = jwt
				u.NkeySeed = nkey
			}

			for name, val := range flags {
				if !val {
					continue
				}
				effect, err := sb.Deps.SeedEffectReg.Get(name)
				if err != nil {
					return fmt.Errorf("sandbox.Setup: get effect %q for %q: %w", name, alias, err)
				}
				eDeps := seedeffect.Deps{
					AuthURL: authURL,
					Mongo:   db,
				}
				if err := effect.Apply(ctx, u, eDeps); err != nil {
					return fmt.Errorf("sandbox.Setup: apply %s to %s: %w", name, alias, err)
				}
			}
			sb.Users[alias] = u
		}
	}

	// Step 9: insert minimal user-profile docs per site.
	// Two passes per site:
	//   (a) local users — declared in seed.users, profile siteId = this site.
	//   (b) remote-user stubs — declared in seed.remote_users with an
	//       explicit home_site; profile siteId = the home_site value.
	// The author is explicit about both; the engine never infers a stub.
	for siteName, siteBlock := range sb.Scenario.Sites {
		db, ok := sb.Deps.MongoBySite[siteName]
		if !ok || db == nil {
			if len(siteBlock.Seed.Users) > 0 || len(siteBlock.Seed.RemoteUsers) > 0 {
				return fmt.Errorf("sandbox.Setup: no Mongo for site %q (scenario expects it)", siteName)
			}
			continue
		}
		// (a) local users — siteId = this site.
		siteUsers := make(map[string]*seedeffect.SeedUser, len(siteBlock.Seed.Users))
		for alias := range siteBlock.Seed.Users {
			if u, ok := sb.Users[alias]; ok {
				siteUsers[alias] = u
			}
		}
		if err := insertSeedUserProfiles(ctx, db, siteUsers, siteName); err != nil {
			return fmt.Errorf("sandbox.Setup: insert user profiles for %s: %w", siteName, err)
		}
		// (b) remote-user stubs — siteId = each spec's home_site.
		for alias, spec := range siteBlock.Seed.RemoteUsers {
			u, ok := sb.Users[alias]
			if !ok {
				return fmt.Errorf("sandbox.Setup: remote_users[%q] on %s references an alias not declared in any seed.users (it must be minted on its home_site first)", alias, siteName)
			}
			if spec.HomeSite == "" {
				return fmt.Errorf("sandbox.Setup: remote_users[%q] on %s missing required home_site", alias, siteName)
			}
			if spec.HomeSite == siteName {
				return fmt.Errorf("sandbox.Setup: remote_users[%q] on %s has home_site=%s; a remote-user stub's home_site must differ from the projecting site", alias, siteName, spec.HomeSite)
			}
			stubUsers := map[string]*seedeffect.SeedUser{alias: u}
			if err := insertSeedUserProfiles(ctx, db, stubUsers, spec.HomeSite); err != nil {
				return fmt.Errorf("sandbox.Setup: insert remote-user stub %s on %s: %w", alias, siteName, err)
			}
		}
	}

	// Step 10: build placeholders for substitution.
	// Placeholders MUST be populated before Cassandra seed (Step 12).
	for alias, u := range sb.Users {
		sb.Placeholders[alias] = map[string]any{
			"account": u.Account,
			"id":      u.ID,
			"jwt":     u.JWT,
			"nkey":    u.NkeySeed,
		}
	}

	// Step 11: insert seeded rooms + subscriptions + room_members, per site.
	if err := insertSeededRooms(ctx, sb); err != nil {
		return fmt.Errorf("sandbox.Setup: seed rooms: %w", err)
	}

	// Step 12: insert seeded Cassandra rows. Gated on Cassandra non-nil +
	// at least one declared table.
	if sb.Deps.Cassandra != nil && len(sb.Scenario.CassandraData) > 0 {
		if err := insertSeededCassandraRows(ctx, sb); err != nil {
			return fmt.Errorf("sandbox.Setup: %w", err)
		}
	}

	// Step 13: build the per-scenario poller registry. StartTime captures
	// T_open so MongoPoller filters out pre-existing rows and
	// StreamPoller's Watch sees only events from this point forward.
	sb.PollerReg = pollers.NewRegistry()
	cleanup, err := pollers.RegisterBuiltinPollers(sb.PollerReg, &pollers.BuiltinDeps{
		Sites:               sb.Deps.MongoBySite,
		Cassandra:           sb.Deps.Cassandra,
		MessageBucketWindow: sb.Deps.MessageBucketWindow,
		AdminConns:          sb.Deps.AdminConns,
		ReplyReader:         sb.Deps.ReplyReader,
		StartTime:           sb.StartTime,
	})
	if err != nil {
		return fmt.Errorf("sandbox.Setup: register builtin pollers: %w", err)
	}
	sb.pollerCleanup = cleanup

	return nil
}

// Teardown is the best-effort cleanup hook called via
// `defer sb.Teardown(context.Background())` from runScenario after
// the case loop finishes (or panics).
//
//  1. Reset the chaos engine — clears any partition the case loop left
//     behind so the next scenario's Setup starts from a known state.
//  2. Don't drop Mongo — the next scenario's Setup will (or `make
//     deps-down` clears everything).
//
// Idempotent via sync.Once: a second call is a no-op. Safe to call
// on a nil receiver (mirrors NewSandbox(nil) → nil).
func (sb *Sandbox) Teardown(ctx context.Context) {
	if sb == nil {
		return
	}
	sb.terminated.Do(func() {
		// Close StreamPoller watcher goroutines before chaos reset so
		// the readers don't see a torn-down toxiproxy mid-drain.
		if sb.pollerCleanup != nil {
			sb.pollerCleanup()
		}
		if sb.Deps.Chaos != nil {
			_ = sb.Deps.Chaos.Reset(ctx)
		}
	})
}

// defaultSiteID is the fallback when a site name is not explicitly provided
// (e.g. in insertSeedUserProfiles when siteName is empty).
const defaultSiteID = "site-local"

// insertSeedUserProfiles writes a minimal user profile document for
// each materialized seed user. Required because room-service's
// create-room handler validates the caller's user record by Account
// lookup against MongoDB (non-empty EngName + ChineseName guard at
// room-service/handler.go:186-188) — Sandbox.Setup separately mints
// the NATS identity but does NOT populate the profile collection.
//
// Document shape mirrors the deleted seed/users.json convention plus
// a Phase 3.8 `verified` boolean (NOT in pkg/model.User but written
// raw via bson.M so the chat application is unaffected — black-box):
//
//	_id          == "u-" + alias
//	account      == alias
//	siteId       == siteName (the site key from sb.Deps.MongoBySite)
//	engName      == alias (stand-in — only required to be non-empty
//	chineseName  == alias    for the room-service guard)
//	verified     == u.Verified (Phase 3.8 — VerifiedEffect signal)
//
// Other model.User fields (sectId, deptId, employeeId) stay empty —
// no current handler reads them.
func insertSeedUserProfiles(ctx context.Context, db *mongo.Database, users map[string]*seedeffect.SeedUser, siteName string) error {
	if len(users) == 0 {
		return nil
	}
	if siteName == "" {
		siteName = defaultSiteID
	}
	docs := make([]any, 0, len(users))
	for _, u := range users {
		// bson.M (not pkg/model.User) so the `verified` field can ride
		// alongside without modifying the production schema. The
		// chat-service code reads only the model.User fields it knows
		// about; extra fields are ignored. Phase 3 black-box principle:
		// we don't mutate pkg/model to accommodate the tests.
		docs = append(docs, bson.M{
			"_id":         u.ID,
			"account":     u.Account,
			"siteId":      siteName,
			"engName":     u.Account,
			"chineseName": u.Account,
			"verified":    u.Verified,
		})
	}
	_, err := db.Collection("users").InsertMany(ctx, docs)
	return err
}
