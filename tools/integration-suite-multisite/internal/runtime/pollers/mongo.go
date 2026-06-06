package pollers

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// MongoFindPoller is the universal `mongo_find` primitive. Per-poll
// args (parsed each call):
//
//	args.collection  string                  required — Mongo collection name
//	args.filter      map[string]any          optional — BSON filter applied AS-IS
//
// The poller auto-ANDs `createdAt >= startTime` into the filter so
// rows from prior scenarios are excluded. If args.filter already
// supplies a `createdAt` constraint, the author's wins (we don't
// re-stitch). Stateless: every call is a fresh Find.
//
// Phase 4.0 universal-primitive design: the Go code carries no
// table-specific knowledge; the YAML scenario tells the poller
// where to look. Replaces the legacy MongoPoller bound to "rooms".
//
// Multi-site: Sites maps siteID → *mongo.Database. PollFn receives
// the site name and picks the matching database. An unknown or nil
// site logs a warning and returns no events.
type MongoFindPoller struct {
	Sites     map[string]*mongo.Database
	StartTime time.Time
}

// NewMongoFindPoller builds the singleton primitive with a per-site
// database map. Register it under "mongo_find" in the runtime registry.
func NewMongoFindPoller(sites map[string]*mongo.Database, startTime time.Time) *MongoFindPoller {
	return &MongoFindPoller{Sites: sites, StartTime: startTime}
}

// PollFn returns a closure that runs one Find per call.
//
// site selects the database from the Sites map. Unknown or nil site
// logs a warning and returns nil events.
// traceparent is accepted to satisfy the Poller interface but is
// ignored — Mongo documents carry no trace context. Per-case trace
// attribution falls out of the timestamp filter + per-scenario
// sandbox isolation.
func (p *MongoFindPoller) PollFn(site string, args map[string]any, _ string) func() []readers.Event {
	db, ok := p.Sites[site]
	if !ok || db == nil {
		return func() []readers.Event {
			slog.Warn("mongo_find: no database for site",
				"site", site, "available", siteKeys(p.Sites))
			return nil
		}
	}

	collection, _ := args["collection"].(string)
	if collection == "" {
		return func() []readers.Event {
			slog.Warn("mongo_find: args.collection is required and must be a string", "got", args["collection"])
			return nil
		}
	}

	filter := buildMongoFilter(args, p.StartTime)
	coll := db.Collection(collection)
	owner := mongoOwnerHint(collection)

	return func() []readers.Event {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		cur, err := coll.Find(ctx, filter)
		if err != nil {
			slog.Warn("MongoFindPoller: find", "collection", collection, "err", err)
			return nil
		}
		defer cur.Close(ctx) //nolint:errcheck

		var out []readers.Event
		for cur.Next(ctx) {
			var doc map[string]any
			if err := cur.Decode(&doc); err != nil {
				slog.Warn("MongoFindPoller: decode", "collection", collection, "err", err)
				continue
			}
			out = append(out, readers.Event{
				Location:  "mongo_find",
				Timestamp: time.Now(),
				OwnerSvc:  owner,
				Payload:   doc,
				Type:      readers.EventCascade,
			})
		}
		if err := cur.Err(); err != nil && !strings.Contains(err.Error(), "context") {
			slog.Warn("MongoFindPoller: cursor", "collection", collection, "err", err)
		}
		return out
	}
}

// siteKeys returns the keys of the Sites map for use in log messages.
func siteKeys(m map[string]*mongo.Database) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// buildMongoFilter merges the author's args.filter with the startTime
// guard. Author-supplied `createdAt` always wins so the rare scenario
// that wants to look at older rows (e.g. a seed-doc assertion) can opt
// out by setting its own createdAt clause.
func buildMongoFilter(args map[string]any, startTime time.Time) bson.M {
	out := bson.M{"createdAt": bson.M{"$gte": startTime}}
	raw, ok := args["filter"].(map[string]any)
	if !ok {
		return out
	}
	for k, v := range raw {
		out[k] = v
	}
	return out
}

// mongoOwnerHint guesses the canonical writer for well-known
// collections so failure-detail attributions stay informative.
// Unknown collections return "" — the test still works, only the
// diagnostic loses one breadcrumb.
func mongoOwnerHint(collection string) string {
	switch collection {
	case "rooms", "room_members", "subscriptions":
		return "room-worker"
	case "users":
		return "auth-service"
	case "thread_rooms", "thread_subscriptions":
		return "message-worker"
	}
	return ""
}

// Compile-time interface check.
var _ Poller = (*MongoFindPoller)(nil)
