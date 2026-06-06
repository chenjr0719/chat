package scenario

import (
	"fmt"
	"regexp"
	"time"
)

// cqlIdentifierRegex enforces the lowercase-snake-case identifier shape
// (CQL convention). Used for table names AND column names — they share
// the same grammar.
var cqlIdentifierRegex = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateCassandraData enforces the six Phase 4.3 rules from
// docs/spec-cassandra-seeding-engine.md §2.2:
//
//  1. Every table name matches ^[a-z][a-z0-9_]*$.
//  2. Every entry has at least one row.
//  3. Every row is a non-empty column map.
//  4. Every column name matches the same CQL identifier regex.
//  5. Every ${bucket(<col>)} token references a column declared
//     in the same row.
//  6. Every ${now ± <duration>} token parses cleanly via
//     time.ParseDuration.
//
// Returns nil cleanly for empty / partial seed blocks so every
// pre-Phase-4.3 scenario keeps validating unchanged.
//
// Cluster-side rules (table exists in keyspace, column types match,
// gocql bind compatibility) fire at INSERT time via the runtime layer
// (PR 3). This validator is intentionally pure-Go so the scenario load
// path can reject malformed seed blocks before any I/O.
//
// Error messages name the (table, row-index, column) coordinate and
// the offending literal so the author can grep the YAML directly.
// Column iteration within a row is sorted so failure ordering is
// reproducible across runs (Go's random map iteration would otherwise
// pick a different offending column each invocation when multiple
// columns are malformed).
func ValidateCassandraData(seed SeedBlock) error {
	if len(seed.CassandraData) == 0 {
		return nil
	}

	for i, entry := range seed.CassandraData {
		if err := validateCassandraTable(i, entry); err != nil {
			return err
		}
	}
	return nil
}

func validateCassandraTable(idx int, entry SeedCassandraTable) error {
	// Rule 1 — table name shape.
	if !cqlIdentifierRegex.MatchString(entry.Table) {
		return fmt.Errorf(`cassandra_data[%d]: invalid table name %q (CQL identifiers must be lower-snake-case)`, idx, entry.Table)
	}

	// Rule 2 — at least one row.
	if len(entry.Rows) == 0 {
		return fmt.Errorf(`cassandra_data[%s]: rows: must have at least one entry`, entry.Table)
	}

	for rowIdx, row := range entry.Rows {
		if err := validateCassandraRow(entry.Table, rowIdx, row); err != nil {
			return err
		}
	}
	return nil
}

func validateCassandraRow(table string, rowIdx int, row SeedCassandraRow) error {
	// Rule 3 — non-empty row.
	if len(row) == 0 {
		return fmt.Errorf(`cassandra_data[%s][%d]: empty row`, table, rowIdx)
	}

	cols := sortedColumns(row)

	// Rule 4 — column-name shape. Sorted iteration → deterministic
	// "first invalid column" error across runs.
	for _, col := range cols {
		if !cqlIdentifierRegex.MatchString(col) {
			return fmt.Errorf(`cassandra_data[%s][%d]: invalid column %q (CQL identifiers must be lower-snake-case)`, table, rowIdx, col)
		}
	}

	// Rule 5 — bucket-token referents must be declared in the same row.
	for _, col := range cols {
		raw, ok := row[col].(string)
		if !ok || !IsBucketToken(raw) {
			continue
		}
		referent, err := ParseBucketToken(raw)
		if err != nil {
			return fmt.Errorf(`cassandra_data[%s][%d]: column %q: %w`, table, rowIdx, col, err)
		}
		if _, present := row[referent]; !present {
			return fmt.Errorf(`cassandra_data[%s][%d]: bucket(%s): row has no %s column`, table, rowIdx, referent, referent)
		}
	}

	// Rule 6 — ${now ± duration} parses cleanly. The anchor time is
	// irrelevant for validation (we discard the resolved time.Time);
	// we only care that the grammar is well-formed.
	probe := time.Unix(0, 0).UTC()
	for _, col := range cols {
		raw, ok := row[col].(string)
		if !ok || !IsNowToken(raw) {
			continue
		}
		expr := unwrapToken(raw)
		if _, err := ResolveNowExpr(expr, probe); err != nil {
			return fmt.Errorf(`cassandra_data[%s][%d]: column %q: %w`, table, rowIdx, col, err)
		}
	}

	return nil
}
