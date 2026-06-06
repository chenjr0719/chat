package scenario

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/hmchangw/chat/pkg/msgbucket"
)

// Substitution layer for the Phase 4.3 Cassandra-seed engine. Two
// token families:
//
//	${now}, ${now ± <duration>}    Pass 1 — resolves to time.Time
//	                                anchored at the sandbox's T_open.
//	${bucket(<col>)}               Pass 2 — resolves to int64 via
//	                                msgbucket.Sizer.Of(row[<col>]).
//
// Pass 1 + Pass 2 run sequentially because the bucket token needs the
// referenced column to already be a time.Time. See
// docs/spec-cassandra-seeding-engine.md §3.
//
// Pure-Go, no gocql import — the runtime layer (PR 3) is the only
// caller and it owns the gocql binding.

// nowExprRegex matches the *interior* of a ${...} wrapper that begins
// with "now". Trailing characters are captured into group 1 when
// present and validated in code so the "unknown operator" branch of
// ResolveNowExpr can surface a specific error rather than falling
// back to a generic "not a now expression" message.
var nowExprRegex = regexp.MustCompile(`^\s*now\s*(.*?)\s*$`)

// nowWrapperRegex matches the entire "${...}" string when the
// interior parses as a now-expression.
var nowWrapperRegex = regexp.MustCompile(`^\$\{\s*now\b.*\}$`)

// bucketWrapperRegex matches "${bucket(<col>)}" with optional
// whitespace. Group 1 captures the column name. The CQL-identifier
// constraint on <col> is reinforced by ParseBucketToken's redundant
// check against cqlIdentifierRegex (validate_cassandra_seed.go) so
// the failure mode is uniform whether validation or substitution
// rejects first.
var bucketWrapperRegex = regexp.MustCompile(`^\$\{\s*bucket\(\s*([a-z][a-z0-9_]*)\s*\)\s*\}$`)

// ResolveNowExpr parses a now-expression (the interior of a "${...}"
// wrapper — caller has already stripped them) and returns the
// resolved time.Time anchored at tOpen.
//
// Accepted forms:
//
//	"now"
//	"now ± <duration>"  where ± ∈ {+, -}
//	                    and <duration> is anything time.ParseDuration accepts.
//
// Whitespace is tolerated everywhere inside the expression. Anything
// else returns an error naming the offending fragment.
func ResolveNowExpr(expr string, tOpen time.Time) (time.Time, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("empty now expression")
	}

	m := nowExprRegex.FindStringSubmatch(trimmed)
	if m == nil {
		// Caller passed something that wasn't a now-expression at all.
		return time.Time{}, fmt.Errorf(`expected now expression, got %q`, expr)
	}

	rest := strings.TrimSpace(m[1])
	if rest == "" {
		// Bare "now" — no offset to apply.
		return tOpen, nil
	}

	// rest begins with the operator (regex guarantees one of +/-).
	op := rest[0]
	durStr := strings.TrimSpace(rest[1:])
	if durStr == "" {
		return time.Time{}, fmt.Errorf("missing duration after operator %q", string(op))
	}

	switch op {
	case '+', '-':
		// Closed set; the regex already enforced one of these. The
		// explicit switch is the readable failure-surface for any
		// future grammar drift.
	default:
		return time.Time{}, fmt.Errorf(`unknown operator %q (expected "+" or "-")`, string(op))
	}

	d, err := time.ParseDuration(durStr)
	if err != nil {
		return time.Time{}, fmt.Errorf(`invalid duration %q: %w`, durStr, err)
	}

	if op == '-' {
		return tOpen.Add(-d), nil
	}
	return tOpen.Add(d), nil
}

// IsNowToken reports whether s is a "${now ...}" wrapper.
func IsNowToken(s string) bool {
	return nowWrapperRegex.MatchString(s)
}

// ResolveCreatedAtToken parses a WRAPPED "${now ± duration}" form
// and returns the resolved time.Time anchored at tOpen. Convenience
// wrapper around ResolveNowExpr that handles the "${...}" unwrap so
// callers in other packages (e.g. internal/runtime/sandbox_rooms.go's
// per-room CreatedAt resolution) don't have to import the unexported
// unwrapToken helper. Returns an error if s isn't a now-token or if
// the inner expression is malformed.
func ResolveCreatedAtToken(s string, tOpen time.Time) (time.Time, error) {
	if !IsNowToken(s) {
		return time.Time{}, fmt.Errorf("not a now token: %q", s)
	}
	return ResolveNowExpr(unwrapToken(s), tOpen)
}

// IsBucketToken reports whether s is a "${bucket(<col>)}" wrapper.
func IsBucketToken(s string) bool {
	return bucketWrapperRegex.MatchString(s)
}

// ParseBucketToken extracts the column-name argument from a
// "${bucket(<col>)}" wrapper.
func ParseBucketToken(s string) (string, error) {
	m := bucketWrapperRegex.FindStringSubmatch(s)
	if m == nil {
		return "", fmt.Errorf(`not a bucket token: %q`, s)
	}
	col := m[1]
	if !cqlIdentifierRegex.MatchString(col) {
		return "", fmt.Errorf(`bucket token references invalid column %q`, col)
	}
	return col, nil
}

// ResolveRowNowTokens walks the row and replaces every string value
// matching the "${now ...}" form with a resolved time.Time anchored
// at tOpen. Non-string values pass through unchanged. Non-token
// strings pass through unchanged.
//
// Iteration is sorted by column name so error messages are
// deterministic across runs (Go's map iteration would otherwise pick
// a different offending column each invocation when multiple
// columns are malformed).
//
// On error the error message names the column coordinate, the input
// expression, and the underlying parse failure — enough for the
// author to locate the typo without diff-reading the YAML.
func ResolveRowNowTokens(row SeedCassandraRow, tOpen time.Time) error {
	for _, col := range sortedColumns(row) {
		v, ok := row[col].(string)
		if !ok {
			continue
		}
		if !IsNowToken(v) {
			continue
		}
		expr := unwrapToken(v)
		resolved, err := ResolveNowExpr(expr, tOpen)
		if err != nil {
			return fmt.Errorf(`column %q: %w`, col, err)
		}
		row[col] = resolved
	}
	return nil
}

// ResolveRowBucketTokens walks the row (caller MUST have already
// passed it through ResolveRowNowTokens so referenced columns hold
// real time.Time values) and replaces every "${bucket(<col>)}" value
// with the int64 bucket via sizer.Of.
//
// Errors with row-coordinate diagnostics when the referenced column
// is absent or not a time.Time. Sorted iteration for deterministic
// error ordering, same as ResolveRowNowTokens.
func ResolveRowBucketTokens(row SeedCassandraRow, sizer msgbucket.Sizer) error {
	for _, col := range sortedColumns(row) {
		v, ok := row[col].(string)
		if !ok {
			continue
		}
		if !IsBucketToken(v) {
			continue
		}
		referent, err := ParseBucketToken(v)
		if err != nil {
			return fmt.Errorf(`column %q: %w`, col, err)
		}
		raw, present := row[referent]
		if !present {
			return fmt.Errorf(`column %q: bucket(%s): row has no %s column`, col, referent, referent)
		}
		t, ok := raw.(time.Time)
		if !ok {
			return fmt.Errorf(`column %q: bucket(%s): expected time.Time, got %T`, col, referent, raw)
		}
		row[col] = sizer.Of(t)
	}
	return nil
}

// unwrapToken strips the surrounding "${" + "}" from a token value.
// Caller guarantees s is a recognised token (IsNowToken / IsBucketToken
// returned true), so the trim is exact.
func unwrapToken(s string) string {
	s = strings.TrimPrefix(s, "${")
	s = strings.TrimSuffix(s, "}")
	return s
}

// sortedColumns returns the row's column names in lexical order so
// error messages — and any future change-detection diffs — are
// reproducible across runs.
func sortedColumns(row SeedCassandraRow) []string {
	out := make([]string, 0, len(row))
	for k := range row {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
