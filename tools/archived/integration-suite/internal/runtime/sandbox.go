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

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/matchers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/runtime/pollers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
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
	Mongo         *mongo.Database
	AuthURL       string
	SiteID        string
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
	// Nil tolerated — scenarios that don't reference jetstream_consume
	// still run; ones that do get a slog warning + assertion timeout.
	AdminConn *nats.Conn

	// Cassandra is the chat keyspace session used by `cassandra_select`
	// (read) and Phase 4.3's seed engine (write). Nil tolerated — both
	// the truncate + insert hooks short-circuit cleanly.
	Cassandra *gocql.Session

	// CassandraKeyspace is the keyspace name Phase 4.3's seed engine
	// queries for cluster metadata when deciding whether to auto-inject
	// `bucket`. Empty falls back to "chat" (production default).
	CassandraKeyspace string

	// MessageBucketWindow tunes the cassandra_select primitive's
	// bucket-math helper (BucketAt). Zero falls back to 24h
	// (production MESSAGE_BUCKET_HOURS).
	MessageBucketWindow time.Duration

	// MatcherReg is the matchers registry MatchShape inherits when
	// RunCase wraps each expected[].match into a Gomega matcher. Nil
	// is tolerated: MatchShape falls back to matchers.NewRegistry().
	MatcherReg *matchers.Registry

	// MishapRegistry holds the per-kind Factory functions registered at
	// runner startup (Phase 1's catalog). RunCase looks up
	// `factoryByKind[c.Mishap]` here when c.Mishap is set.
	MishapRegistry *mishap.Registry

	// FactoryByKind maps a case's mishap kind (e.g. "mongo-partition-500ms")
	// to the Factory name registered in MishapRegistry (e.g.
	// "MongoPartitionFactory"). Built from the catalog at runner startup
	// via the same buildFactoryByKind helper the v2 path uses.
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
	userFlags := map[string]map[string]bool{}
	for alias, flags := range sb.Scenario.Seed.Users {
		userFlags[alias] = map[string]bool(flags)
	}
	if err := seedeffect.ValidateFlags(sb.Deps.SeedEffectReg, userFlags); err != nil {
		return fmt.Errorf("sandbox.Setup: validate flags: %w", err)
	}

	// Step 2: validate the room-subscription seed block (closed enums
	// for room types + roles, unique room ids, DM arity, membership
	// refs resolve). Returns nil cleanly when seed.Rooms +
	// seed.Memberships are both empty. See
	// docs/spec-room-subscription-seed.md §2.2.
	if err := scenario.ValidateSeedBlock(sb.Scenario.Seed); err != nil {
		return fmt.Errorf("sandbox.Setup: validate seed block: %w", err)
	}

	// Step 3: validate the Cassandra-data seed block (table-name +
	// column-name CQL identifiers, non-empty rows, well-formed
	// ${now ± d} grammar, ${bucket(<col>)} referents declared in the
	// same row). Returns nil cleanly when seed.CassandraData is
	// empty. See docs/spec-cassandra-seeding-engine.md §2.2.
	if err := scenario.ValidateCassandraData(sb.Scenario.Seed); err != nil {
		return fmt.Errorf("sandbox.Setup: validate cassandra_data: %w", err)
	}

	// Step 4: capture T_open once so every downstream timestamp
	// (poller filter boundary, seeded room/subscription createdAt,
	// etc.) agrees on a single session anchor — picking time.Now()
	// at multiple call sites would tear the boundary across
	// milliseconds.
	sb.StartTime = time.Now()

	// Step 5: reset chaos engine.
	if sb.Deps.Chaos != nil {
		if err := sb.Deps.Chaos.Reset(ctx); err != nil {
			return fmt.Errorf("sandbox.Setup: chaos reset: %w", err)
		}
	}

	// Step 6: drop sandbox-owned collections.
	if sb.Deps.Mongo != nil {
		for _, name := range sandboxOwnedCollections {
			if err := sb.Deps.Mongo.Collection(name).Drop(ctx); err != nil {
				return fmt.Errorf("sandbox.Setup: drop %s: %w", name, err)
			}
		}
	}

	// Step 7: truncate sandbox-owned Cassandra tables. Mirrors Step 6's
	// drop discipline so each scenario starts from byte-identical state
	// across both stores. Mongo runs first (faster) so the Cassandra
	// wipe doesn't bottleneck the case loop's first scenario. See
	// docs/spec-cassandra-seeding-engine.md §4.2.
	if sb.Deps.Cassandra != nil {
		if err := truncateSandboxCassandraTables(ctx, sb); err != nil {
			return fmt.Errorf("sandbox.Setup: %w", err)
		}
	}

	// Step 8: materialize seed users + apply effects.
	//
	// NATS credential minting is unconditional. Every seeded user gets
	// a JWT + nkey via auth-service /auth — withholding it would only
	// produce a connection error, not a real authorization gate. The
	// `verified` semantic rides on the SeedUser.Verified flag (set by
	// VerifiedEffect) and is persisted into the Mongo user-profile
	// doc downstream.
	for alias, flags := range sb.Scenario.Seed.Users {
		u := &seedeffect.SeedUser{
			Alias:   alias,
			Account: alias,
			ID:      "u-" + alias,
		}

		// Mint a NATS identity FIRST so the loop below can layer
		// effect-driven attributes on top. AuthURL=="" is operator-
		// detectable at runner startup (cfg validation) — we surface the
		// error here loudly rather than silently issuing empty creds.
		if sb.Deps.AuthURL != "" {
			jwt, nkey, err := seedeffect.MintNATSIdentity(ctx, u.Account, sb.Deps.AuthURL)
			if err != nil {
				return fmt.Errorf("sandbox.Setup: mint creds for %q: %w", alias, err)
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
				// Should be impossible after step 1's validation but
				// guard against registry mutation between validate + apply.
				return fmt.Errorf("sandbox.Setup: get effect %q for %q: %w", name, alias, err)
			}
			deps := seedeffect.Deps{
				AuthURL: sb.Deps.AuthURL,
				Mongo:   sb.Deps.Mongo,
			}
			if err := effect.Apply(ctx, u, deps); err != nil {
				return fmt.Errorf("sandbox.Setup: apply %s to %s: %w", name, alias, err)
			}
		}
		sb.Users[alias] = u
	}

	// Step 9: insert a minimal profile doc for every materialized user.
	// VerifiedEffect mints a NATS identity (JWT + nkey) but the chat
	// backend separately reads pkg/model.User from MongoDB to enforce
	// the room-service create-room guard at room-service/handler.go:186-188
	// (non-empty EngName + ChineseName). Without these documents, every
	// positive case fails with "user not found" / "invalid user data".
	if sb.Deps.Mongo != nil {
		if err := insertSeedUserProfiles(ctx, sb.Deps.Mongo, sb.Users, sb.Deps.SiteID); err != nil {
			return fmt.Errorf("sandbox.Setup: insert user profiles: %w", err)
		}
	}

	// Step 10: build placeholders for substitution.
	//
	// Cassandra-seed values can reference ${alice.id} / ${bob.account}
	// / ${site}, and the Cassandra-seed engine (Step 12) walks the
	// runtime substitution pipeline — so Placeholders MUST be
	// populated before it runs. Room insertion (Step 11) doesn't
	// currently consume them but gains the same capability as a bonus
	// (a future scenario could declare `name: "${alice.account}'s
	// room"`).
	for alias, u := range sb.Users {
		sb.Placeholders[alias] = map[string]any{
			"account": u.Account,
			"id":      u.ID,
			"jwt":     u.JWT,
			"nkey":    u.NkeySeed,
		}
	}

	// Step 11: insert seeded rooms + subscriptions + room_members.
	// Feeds the message-gatekeeper's subscription lookup so downstream
	// messaging tests can fire msg.send without the rejection path
	// swallowing the request. Document shapes mirror pkg/model.Room /
	// .Subscription / .RoomMember (see
	// docs/spec-room-subscription-seed.md §3); inserts are written via
	// bson.M to keep pkg/model untouched (black-box principle).
	//
	// Gated on Mongo non-nil + at least one declared room so scenarios
	// that don't declare seed.Rooms keep running unchanged, and unit
	// tests that wire Deps.Mongo=nil short-circuit cleanly.
	if sb.Deps.Mongo != nil && len(sb.Scenario.Seed.Rooms) > 0 {
		if err := insertSeededRooms(ctx, sb); err != nil {
			return fmt.Errorf("sandbox.Setup: seed rooms: %w", err)
		}
	}

	// Step 12: insert seeded Cassandra rows. Unblocks the drafted
	// history scenarios (messages_by_room / messages_by_id /
	// thread_messages_by_room pagination) by pre-populating rows
	// stamped with relative-time tokens (${now - 2m}) and optional
	// explicit / auto-computed bucket values. See
	// docs/spec-cassandra-seeding-engine.md §3 + §5.
	//
	// Gated on Cassandra non-nil + at least one declared table so
	// scenarios that don't declare cassandra_data keep running
	// unchanged.
	if sb.Deps.Cassandra != nil && len(sb.Scenario.Seed.CassandraData) > 0 {
		if err := insertSeededCassandraRows(ctx, sb); err != nil {
			return fmt.Errorf("sandbox.Setup: %w", err)
		}
	}

	// Step 13: build the per-scenario poller registry. StartTime captures
	// T_open so MongoPoller filters out pre-existing rows and
	// StreamPoller's Watch sees only events from this point forward.
	// Missing readers/Mongo skip their location (RegisterBuiltinPollers
	// is nil-tolerant); RunCase's Get call surfaces the unavailable
	// location at assertion time with the catalog of what IS available.
	sb.PollerReg = pollers.NewRegistry()
	cleanup, err := pollers.RegisterBuiltinPollers(sb.PollerReg, &pollers.BuiltinDeps{
		MongoDB:             sb.Deps.Mongo,
		Cassandra:           sb.Deps.Cassandra,
		MessageBucketWindow: sb.Deps.MessageBucketWindow,
		AdminConn:           sb.Deps.AdminConn,
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

// defaultSiteID falls back to the network-alias literal every Phase 2
// deps.go container uses ("site-local") when SandboxDeps.SiteID is
// empty. Keeps unit tests (which often leave SiteID unset) lined up
// with the same value the runner injects under USE_INFRA=true.
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
//	siteId       == sb.Deps.SiteID (or "site-local" default)
//	engName      == alias (stand-in — only required to be non-empty
//	chineseName  == alias    for the room-service guard)
//	verified     == u.Verified (Phase 3.8 — VerifiedEffect signal)
//
// Other model.User fields (sectId, deptId, employeeId) stay empty —
// no current handler reads them.
func insertSeedUserProfiles(ctx context.Context, db *mongo.Database, users map[string]*seedeffect.SeedUser, siteID string) error {
	if len(users) == 0 {
		return nil
	}
	if siteID == "" {
		siteID = defaultSiteID
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
			"siteId":      siteID,
			"engName":     u.Account,
			"chineseName": u.Account,
			"verified":    u.Verified,
		})
	}
	_, err := db.Collection("users").InsertMany(ctx, docs)
	return err
}
