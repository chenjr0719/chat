package pollers

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/msgbucket"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
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
// site is accepted to satisfy the Poller interface but is ignored —
// Cassandra is a shared cluster across sites (single-cluster design).
// traceparent is ignored — Cassandra rows carry no trace context.
//
// Substrate-error discipline (plan-ahead §2.9): cluster-side
// failures (down node, missing keyspace/table, schema drift,
// malformed CQL) MUST surface as loud `slog.Warn` lines naming the
// query — never collapse to "zero rows", which is indistinguishable
// from the legitimate "row genuinely absent" state and would
// silently green `not: true` assertions. Mirrors the logs_tail
// loudness pass (commit 5ee1a74).
func (p *CassandraSelectPoller) PollFn(_ string, args map[string]any, _ string) func() []readers.Event {
	query, _ := args["query"].(string)
	if query == "" {
		return func() []readers.Event {
			slog.Warn("cassandra_select: args.query is required and must be a string — substrate not exercised this poll",
				"got", args["query"])
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
				// A decode error usually means the SELECT shape
				// drifted out of sync with the bucketed schema —
				// e.g. a column rename in the migration that the
				// scenario YAML didn't pick up. Include the raw row
				// prefix so the operator can grep against the live
				// table.
				slog.Warn("cassandra_select: row decode failed — likely SELECT JSON shape vs schema drift; row skipped",
					"err", err, "query", query, "row_prefix", truncateForLog(jsonRow, 200))
				continue
			}
			out = append(out, readers.Event{
				Location:  "cassandra_select",
				Timestamp: time.Now(),
				Payload:   doc,
				Type:      readers.EventCascade,
			})
		}
		// gocql surfaces cluster-side errors (unavailable node, bad
		// CQL, missing keyspace/table, bind arity mismatch) only via
		// iter.Close(); the Scan loop just returns false. Drop the
		// previous generic "iter" message in favour of one that
		// names the query so an operator scanning the report can
		// correlate without grepping. Never propagated as an Event
		// — that would change matcher semantics — but loud enough
		// that no operator misreads it as "no rows".
		if err := iter.Close(); err != nil {
			slog.Warn("cassandra_select: gocql iter close — substrate error (cluster unavailable, bad CQL, missing keyspace/table, or bind arity); zero events are NOT 'absent', they are 'never observed'",
				"err", err, "query", query, "params", params)
		}
		return out
	}
}

// truncateForLog returns at most max bytes of s, with a "…(N more)"
// tail when truncated. Used to keep the row_prefix log field bounded
// so a giant malformed row doesn't flood structured logging.
func truncateForLog(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(" + strconv.Itoa(len(s)-max) + " more bytes)"
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
