package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gocql/gocql"

	"github.com/hmchangw/chat/pkg/msgbucket"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// sandboxOwnedCassandraTables is the production write-target set the
// sandbox truncates at Setup. Mirrors sandboxOwnedCollections — the
// invariant is "every scenario starts from byte-identical state across
// every store the harness owns". Same four tables a real chat keyspace
// has as message-write surfaces. See
// docs/spec-cassandra-seeding-engine.md §4.2 for the design rationale
// (why TRUNCATE all four vs. only-tables-referenced).
var sandboxOwnedCassandraTables = []string{
	"messages_by_room",
	"messages_by_id",
	"thread_messages_by_room",
	"pinned_messages_by_room",
}

// defaultCassandraKeyspace is the keyspace name the engine falls back
// to when SandboxDeps.CassandraKeyspace is empty. Matches the runner's
// own default at runner.go:142-144 so production wiring and unit tests
// agree on the same anchor.
const defaultCassandraKeyspace = "chat"

// defaultBucketWindow matches production MESSAGE_BUCKET_HOURS=24 — the
// same fallback the cassandra_select poller uses when MessageBucketWindow
// is unset. Pinning both surfaces on a single constant keeps the seed
// engine and the read poller agreeing on the partition key.
const defaultBucketWindow = 24 * time.Hour

// cassandraExecutor is the minimum surface insertSeededCassandraRows
// + truncateSandboxCassandraTables need. The recording-executor unit
// tests inject a fake; production wraps *gocql.Session.
type cassandraExecutor interface {
	Exec(ctx context.Context, stmt string, binds ...any) error
}

// gocqlExecutor wraps a gocql.Session so the orchestration code can
// remain executor-agnostic + test-friendly.
type gocqlExecutor struct {
	sess *gocql.Session
}

func (g *gocqlExecutor) Exec(ctx context.Context, stmt string, binds ...any) error {
	return g.sess.Query(stmt, binds...).WithContext(ctx).Exec()
}

// tableColumnsFn returns the set of column names present in a table,
// or nil if the table is absent from the keyspace metadata. Used by
// Pass 3 (auto-bucket) to decide whether a table has both `bucket`
// and `created_at` columns. Injected so unit tests can stub the
// schema lookup without a live Cassandra session.
type tableColumnsFn func(table string) map[string]struct{}

// gocqlTableColumns returns a lookup function backed by gocql's
// schema-describer cache. KeyspaceMetadata is cached per-session,
// so the cost is amortized across all rows in one Setup pass.
func gocqlTableColumns(sess *gocql.Session, keyspace string) tableColumnsFn {
	return func(table string) map[string]struct{} {
		km, err := sess.KeyspaceMetadata(keyspace)
		if err != nil {
			return nil
		}
		t, ok := km.Tables[table]
		if !ok {
			return nil
		}
		out := make(map[string]struct{}, len(t.Columns))
		for name := range t.Columns {
			out[name] = struct{}{}
		}
		return out
	}
}

// truncateSandboxCassandraTables wipes the four production write
// targets so the scenario starts from byte-identical Cassandra state.
// Caller (Sandbox.Setup at Step 4b) guarantees sb.Deps.Cassandra is
// non-nil.
func truncateSandboxCassandraTables(ctx context.Context, sb *Sandbox) error {
	return runTruncateCassandra(ctx, &gocqlExecutor{sess: sb.Deps.Cassandra})
}

// runTruncateCassandra is the testable core. Wraps each TRUNCATE with
// the failing table name so a missing table or permissions issue is
// localized in the error chain.
func runTruncateCassandra(ctx context.Context, exec cassandraExecutor) error {
	for _, table := range sandboxOwnedCassandraTables {
		stmt := "TRUNCATE " + table
		if err := exec.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("truncate cassandra %s: %w", table, err)
		}
	}
	return nil
}

// insertSeededCassandraRows materializes sb.Scenario.Seed.CassandraData
// into Cassandra. Caller (Sandbox.Setup at Step 9) guarantees
// sb.Deps.Cassandra is non-nil and the seed block has at least one
// entry.
//
// Substitution pipeline per row (see docs/spec-cassandra-seeding-engine.md
// §5.2):
//
//	pre-pass : ${alice.id} / ${site} via runtime.Substitute
//	pass 1   : ${now ± d}            via scenario.ResolveRowNowTokens
//	pass 2   : ${bucket(<col>)}      via scenario.ResolveRowBucketTokens
//	pass 3   : auto-bucket           if (bucket, created_at) ∈ schema
//	                                  AND row has no explicit bucket
//
// Statement assembly: sorted-by-column-name INSERT with parallel binds
// slice. One Session.Query per row (batching deferred to 4.3.1 if a
// scenario seeds enough rows that Setup latency is measurable).
func insertSeededCassandraRows(ctx context.Context, sb *Sandbox) error {
	keyspace := sb.Deps.CassandraKeyspace
	if keyspace == "" {
		keyspace = defaultCassandraKeyspace
	}
	sizer := msgbucket.New(effectiveBucketWindow(sb.Deps.MessageBucketWindow))
	exec := &gocqlExecutor{sess: sb.Deps.Cassandra}
	lookup := gocqlTableColumns(sb.Deps.Cassandra, keyspace)
	return runInsertCassandraSeed(ctx, sb, exec, lookup, sizer)
}

// effectiveBucketWindow returns d when positive, else defaultBucketWindow.
// Mirrors pollers/builtins.go's window resolution so the seed engine
// + read poller never disagree on partitioning.
func effectiveBucketWindow(d time.Duration) time.Duration {
	if d <= 0 {
		return defaultBucketWindow
	}
	return d
}

// runInsertCassandraSeed is the testable orchestration core. Recording-
// executor unit tests inject their own exec + lookupCols.
//
// Iteration follows YAML order (table list + row list) so a scenario's
// declared ordering is the wire ordering. Within a row, column order
// is sorted lexically by buildInsertStatement so the parallel binds
// slice is stable across runs.
//
// On error the chain names the (table-index, table-name, row-index)
// coordinate so the author can grep the YAML directly. Inner
// substitution errors carry their own column coordinate from
// scenario.ResolveRow* / Substitute.
func runInsertCassandraSeed(
	ctx context.Context,
	sb *Sandbox,
	exec cassandraExecutor,
	lookupCols tableColumnsFn,
	sizer msgbucket.Sizer,
) error {
	subCtx := Context{
		Placeholders: sb.Placeholders,
		Services:     sb.Deps.Services,
	}

	for tableIdx, entry := range sb.Scenario.CassandraData {
		var schemaCols map[string]struct{}
		if lookupCols != nil {
			schemaCols = lookupCols(entry.Table)
		}

		for rowIdx, src := range entry.Rows {
			// Shallow-copy so re-running Setup on the same scenario
			// instance (defensive — not currently in the runner loop)
			// would see the original token strings, not last-pass's
			// resolved time.Time values.
			row := copyCassandraRow(src)

			if err := substituteCassandraRowTokens(row, subCtx); err != nil {
				return fmt.Errorf("cassandra_data[%d (%s)][%d]: %w", tableIdx, entry.Table, rowIdx, err)
			}
			if err := scenario.ResolveRowNowTokens(row, sb.StartTime); err != nil {
				return fmt.Errorf("cassandra_data[%s][%d]: %w", entry.Table, rowIdx, err)
			}
			if err := scenario.ResolveRowBucketTokens(row, sizer); err != nil {
				return fmt.Errorf("cassandra_data[%s][%d]: %w", entry.Table, rowIdx, err)
			}
			applyAutoBucket(row, schemaCols, sizer)

			stmt, binds := buildInsertStatement(entry.Table, row)
			if err := exec.Exec(ctx, stmt, binds...); err != nil {
				return fmt.Errorf("cassandra_data[%s][%d]: insert: %w", entry.Table, rowIdx, err)
			}
		}
	}
	return nil
}

// substituteCassandraRowTokens applies runtime.Substitute to every
// string value that is NOT a recognised ${now ...} or ${bucket(...)}
// token. This pre-pass resolves ${alice.id} / ${site} / etc. before
// the now+bucket passes run. Now-tokens are intentionally skipped
// because runtime.Substitute's `${now}` arm anchors at wall-clock
// time.Now(), whereas the seed engine pins everything to sb.StartTime
// (T_open) for deterministic bucket math. Bucket-tokens are skipped
// because runtime.Substitute would reject "bucket(<col>)" as an
// unknown placeholder path.
//
// Sorted column iteration for deterministic error ordering — same
// discipline as scenario.ResolveRowNowTokens.
func substituteCassandraRowTokens(row scenario.SeedCassandraRow, ctx Context) error {
	for _, col := range sortedCassandraRowKeys(row) {
		raw, ok := row[col].(string)
		if !ok {
			continue
		}
		if scenario.IsNowToken(raw) || scenario.IsBucketToken(raw) {
			continue
		}
		resolved, err := Substitute(raw, ctx)
		if err != nil {
			return fmt.Errorf("column %q: %w", col, err)
		}
		row[col] = resolved
	}
	return nil
}

// applyAutoBucket fires the convenience path from spec §3.3: if the
// table has both `bucket BIGINT` and `created_at TIMESTAMP` columns
// (schema-introspected), AND the row didn't supply an explicit
// `bucket` value, the engine computes one via sizer.Of(created_at).
//
// Silently no-ops when the schema lookup is unavailable (nil), when
// the table is missing either column, when the author supplied a
// bucket explicitly, or when `created_at` isn't a time.Time (e.g. the
// row didn't reach pass 1 — most likely because no ${now ...} token
// was used). All of these are valid "author knows what they want"
// states.
func applyAutoBucket(row scenario.SeedCassandraRow, schemaCols map[string]struct{}, sizer msgbucket.Sizer) {
	if schemaCols == nil {
		return
	}
	if _, ok := schemaCols["bucket"]; !ok {
		return
	}
	if _, ok := schemaCols["created_at"]; !ok {
		return
	}
	if _, ok := row["bucket"]; ok {
		return
	}
	t, ok := row["created_at"].(time.Time)
	if !ok {
		return
	}
	row["bucket"] = sizer.Of(t)
}

// buildInsertStatement assembles the CQL string + parallel binds slice
// for one row. Columns sorted lexically so:
//   - the same row materialised twice produces byte-identical CQL
//     (helpful in unit tests, in change-detection diffs, and in any
//     future statement-cache key);
//   - the binds slice index → column-name mapping is unambiguous when
//     surfacing bind failures.
//
// No keyspace prefix — the gocql.Session's pinned keyspace handles
// that, matching how the production cassandra_select primitive issues
// unqualified table names.
func buildInsertStatement(table string, row scenario.SeedCassandraRow) (string, []any) {
	cols := sortedCassandraRowKeys(row)
	binds := make([]any, 0, len(cols))
	placeholders := make([]string, 0, len(cols))
	for _, c := range cols {
		binds = append(binds, row[c])
		placeholders = append(placeholders, "?")
	}
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		table,
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "),
	)
	return stmt, binds
}

// sortedCassandraRowKeys returns the row's column names in lexical
// order. Local helper (not reusing sortedColumns from cassandra_subst.go
// because that lives in the scenario package and this is the runtime
// package).
func sortedCassandraRowKeys(row scenario.SeedCassandraRow) []string {
	out := make([]string, 0, len(row))
	for k := range row {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// copyCassandraRow returns a shallow copy of a row so token-resolving
// passes don't mutate the scenario's parsed seed block (defensive —
// the runner currently constructs Sandbox once per scenario, but the
// test surface frequently re-Setups against the same scenario AST).
func copyCassandraRow(in scenario.SeedCassandraRow) scenario.SeedCassandraRow {
	out := make(scenario.SeedCassandraRow, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
