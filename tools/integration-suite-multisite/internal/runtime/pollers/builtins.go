package pollers

import (
	"fmt"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/msgbucket"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// BuiltinDeps is the dependency bundle RegisterBuiltinPollers consumes.
// The runner builds these once per scenario (sandbox.Setup wires it).
//
// Phase 4.0 universal-primitive design: every backend dependency is
// nil-tolerant. Pollers whose backend is unavailable still register
// (so YAML-side validation passes) and surface a clear warning at
// PollFn time. The error path is "scenario runs, assertion times
// out, slog records the missing-dep reason in the failure detail".
type BuiltinDeps struct {
	// Sites is the per-site Mongo database map. Required for `mongo_find`.
	// Keys are site names (e.g. "site-a", "site-b").
	Sites map[string]*mongo.Database

	// Cassandra is the chat keyspace session. Required for
	// `cassandra_select`. Cassandra is shared across sites.
	Cassandra *gocql.Session

	// MessageBucketWindow is the bucketing window the production
	// message-worker uses (configured via MESSAGE_BUCKET_HOURS env,
	// default 24h). CassandraSelectPoller exposes BucketAt(t) using
	// a Sizer built from this value so future scenarios can compute
	// partition buckets without re-deriving the math. Zero falls back
	// to 24h.
	MessageBucketWindow time.Duration

	// AdminConns holds one admin NATS connection per site, keyed by
	// site name. jetstream_consume picks by site so JS API calls hit
	// the local domain — the supercluster gateway doesn't carry
	// $JS.<domain>.API across. nats_subscribe takes the site-a entry
	// (Core NATS subjects DO traverse the gateway, one conn is enough).
	// Empty when NATSCredsFile is unset; primitives that depend on it
	// warn at PollFn time.
	AdminConns map[string]*nats.Conn

	// ReplyReader is the dispatcher-fed singleton that backs `reply`.
	// The dispatcher injects per-fire outcomes into this reader; the
	// poller hands them to assertions.
	ReplyReader *readers.NATSReplyReader

	// StartTime is the sandbox's T_open. Stateless pollers
	// (mongo_find, cassandra_select) use it to scope their query
	// filters; stateful pollers (jetstream_consume, logs_tail) forward
	// it to their lazy-init StreamPollers as the Watch start time.
	StartTime time.Time
}

// RegisterBuiltinPollers wires the five universal Phase 4.0
// primitives into reg. Returns a cleanup func that the caller
// (Sandbox.Teardown) MUST defer — closes every cached StreamPoller
// (JetStream consumers + log tails) so the run doesn't leak goroutines
// across scenarios.
//
// Every primitive registers unconditionally so YAML-side resolution
// always succeeds; backend availability is checked at PollFn time
// where a missing dep degrades to a slog warning + empty event slice
// (the assertion times out, the failure detail names the missing dep).
func RegisterBuiltinPollers(reg *Registry, deps *BuiltinDeps) (cleanup func(), err error) {
	// Stateful primitives we need to clean up at teardown.
	jsPoller := NewJetStreamConsumePoller(deps.AdminConns, deps.StartTime)
	logsPoller := NewLogsTailPoller(deps.StartTime)
	// nats_subscribe uses one conn — Core NATS subjects route across
	// the gateway so site-a's admin conn observes site-b publishes too.
	// Pick site-a by convention; any non-nil conn works.
	natsSubPoller := NewNATSSubscribePoller(deps.AdminConns["site-a"])

	cleanup = func() {
		jsPoller.Close()
		logsPoller.Close()
		natsSubPoller.Close()
	}

	// mongo_find — stateless. Uses the real per-site database map.
	// PollFn nil-checks and warns when the site lookup misses.
	reg.Register("mongo_find", NewMongoFindPoller(deps.Sites, deps.StartTime))

	// cassandra_select — stateless. Sizer falls back to 24h.
	window := deps.MessageBucketWindow
	if window <= 0 {
		window = 24 * time.Hour
	}
	reg.Register("cassandra_select",
		NewCassandraSelectPoller(deps.Cassandra, deps.StartTime, msgbucket.New(window)))

	// jetstream_consume — stateful per-(stream, filter_subject).
	reg.Register("jetstream_consume", jsPoller)

	// logs_tail — stateful per-container.
	reg.Register("logs_tail", logsPoller)

	// nats_subscribe — Phase 4.5 stateful per-subject Core NATS
	// subscriber. Implements Warmer so case_runner Step 3b opens the
	// subscription BEFORE the verb fires (Core NATS has no replay).
	reg.Register("nats_subscribe", natsSubPoller)

	// reply — dispatcher-fed. Wrap the existing ReplyReader as a
	// StreamPoller so the buffer fills concurrently with the
	// dispatcher's Inject calls. ReplyReader-nil is a programming
	// error (dispatcher needs it) — guard but don't tolerate.
	if deps.ReplyReader != nil {
		sp, perr := NewStreamPoller(deps.ReplyReader, "", deps.StartTime)
		if perr != nil {
			cleanup()
			return nil, fmt.Errorf("RegisterBuiltinPollers: reply: %w", perr)
		}
		reg.Register("reply", sp)
		// Augment cleanup so the reply stream poller closes too.
		outerCleanup := cleanup
		cleanup = func() {
			sp.Close()
			outerCleanup()
		}
	}

	return cleanup, nil
}
