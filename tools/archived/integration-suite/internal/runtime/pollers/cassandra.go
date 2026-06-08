package pollers

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/msgbucket"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// CassandraSelectPoller is the universal `cassandra_select` primitive.
// Per-poll args (parsed each call):
//
//	args.query   string   required — CQL string to execute
//	args.params  []any    optional — bound parameters; defaults to
//	                       [startTime] when the query contains exactly
//	                       one `?` placeholder, otherwise no binds
//
// `ALLOW FILTERING` is the operator's call to make — include it
// explicitly in args.query if the WHERE clause walks past the
// partition key. For test-scale data (handful of rows per run),
// filtering is cheap; for production-shape datasets, partition-aware
// queries are mandatory.
//
// Phase 4.0 universal-primitive design: no table-specific knowledge
// in Go. Authors write the full CQL in YAML. The poller carries a
// pkg/msgbucket.Sizer so a future `${bucket}` substitution token (or
// scenario-side helper) can compute partition keys without
// re-deriving the math; expose via BucketAt.
type CassandraSelectPoller struct {
	sess      *gocql.Session
	startTime time.Time
	sizer     msgbucket.Sizer
}

// NewCassandraSelectPoller builds the singleton primitive. Register
// under "cassandra_select".
func NewCassandraSelectPoller(sess *gocql.Session, startTime time.Time, sizer msgbucket.Sizer) *CassandraSelectPoller {
	return &CassandraSelectPoller{sess: sess, startTime: startTime, sizer: sizer}
}

// PollFn returns a closure that runs one SELECT per call.
//
// Rows are returned via `SELECT JSON *` and decoded into
// map[string]any so the matches_shape semantic applies unchanged.
// traceparent is ignored — Cassandra rows carry no trace context.
func (p *CassandraSelectPoller) PollFn(args map[string]any, _ string) func() []readers.Event {
	query, _ := args["query"].(string)
	if query == "" {
		return func() []readers.Event {
			slog.Warn("cassandra_select: args.query is required and must be a string", "got", args["query"])
			return nil
		}
	}

	params := buildCassandraParams(args, p.startTime, query)

	return func() []readers.Event {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		iter := p.sess.Query(query, params...).WithContext(ctx).Iter()
		var out []readers.Event
		var jsonRow string
		for iter.Scan(&jsonRow) {
			doc, err := decodeJSONRow(jsonRow)
			if err != nil {
				slog.Warn("CassandraSelectPoller: decode", "err", err)
				continue
			}
			out = append(out, readers.Event{
				Location:  "cassandra_select",
				Timestamp: time.Now(),
				Payload:   doc,
				Type:      readers.EventCascade,
			})
		}
		if err := iter.Close(); err != nil {
			slog.Warn("CassandraSelectPoller: iter", "err", err)
		}
		return out
	}
}

// buildCassandraParams returns the parameter list to bind into the
// query. Explicit args.params wins; if absent AND the query has
// exactly one `?` placeholder, we bind [startTime] (the common
// "rows since T_open" pattern). Otherwise no parameters are bound.
func buildCassandraParams(args map[string]any, startTime time.Time, query string) []any {
	if raw, ok := args["params"].([]any); ok {
		return raw
	}
	if strings.Count(query, "?") == 1 {
		return []any{startTime}
	}
	return nil
}

// BucketAt exposes the configured Sizer so future scenario
// substitution (e.g. a `${bucket}` token, or an args helper) can
// compute partition keys consistently with the production
// MESSAGE_BUCKET_HOURS without re-importing pkg/msgbucket in the
// caller.
func (p *CassandraSelectPoller) BucketAt(t time.Time) int64 {
	return p.sizer.Of(t)
}

// Compile-time interface check.
var _ Poller = (*CassandraSelectPoller)(nil)
