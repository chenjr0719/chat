package scenario

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/msgbucket"
)

// fixedTOpen is the deterministic anchor every now-token test resolves
// against. Chosen as a round midnight UTC so the bucket-math tests
// don't accidentally land near a window boundary.
var fixedTOpen = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// ─── ResolveNowExpr — successful resolutions ───────────────────────

func TestResolveNowExpr_BareNowReturnsTOpen(t *testing.T) {
	got, err := ResolveNowExpr("now", fixedTOpen)
	require.NoError(t, err)
	assert.Equal(t, fixedTOpen, got)
}

func TestResolveNowExpr_NegativeOffsetShiftsBackward(t *testing.T) {
	got, err := ResolveNowExpr("now - 2m", fixedTOpen)
	require.NoError(t, err)
	assert.Equal(t, fixedTOpen.Add(-2*time.Minute), got)
}

func TestResolveNowExpr_PositiveOffsetShiftsForward(t *testing.T) {
	got, err := ResolveNowExpr("now + 1h", fixedTOpen)
	require.NoError(t, err)
	assert.Equal(t, fixedTOpen.Add(1*time.Hour), got)
}

func TestResolveNowExpr_AcceptsMillisecondPrecision(t *testing.T) {
	got, err := ResolveNowExpr("now - 500ms", fixedTOpen)
	require.NoError(t, err)
	assert.Equal(t, fixedTOpen.Add(-500*time.Millisecond), got)
}

// ─── ResolveNowExpr — whitespace tolerance ─────────────────────────

func TestResolveNowExpr_WhitespacePermutationsAllResolveEqually(t *testing.T) {
	want := fixedTOpen.Add(-2 * time.Minute)
	cases := []string{
		"now-2m",
		"now -2m",
		"now- 2m",
		"now - 2m",
		"now   -   2m",
		"  now - 2m  ",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := ResolveNowExpr(in, fixedTOpen)
			require.NoError(t, err)
			assert.Equal(t, want, got, "whitespace must not change resolution")
		})
	}
}

// ─── ResolveNowExpr — error paths ──────────────────────────────────

func TestResolveNowExpr_EmptyExprRejected(t *testing.T) {
	_, err := ResolveNowExpr("", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty now expression")
}

func TestResolveNowExpr_ExprWithoutNowKeywordRejected(t *testing.T) {
	// Defensive — the dispatcher should never call ResolveNowExpr
	// on a non-now expression, but a hard error keeps the contract
	// explicit if it ever does.
	_, err := ResolveNowExpr("foo - 2m", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "now")
}

func TestResolveNowExpr_UnknownOperatorRejected(t *testing.T) {
	_, err := ResolveNowExpr("now * 2m", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `operator "*"`)
	assert.Contains(t, err.Error(), `expected "+" or "-"`,
		"error must name the closed operator set so authors see the fix")
}

func TestResolveNowExpr_MalformedDurationRejected(t *testing.T) {
	_, err := ResolveNowExpr("now - 2 minutes", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `invalid duration "2 minutes"`,
		"error must quote the offending duration literal")
}

func TestResolveNowExpr_MissingDurationAfterOperatorRejected(t *testing.T) {
	_, err := ResolveNowExpr("now -", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing duration after operator")
}

// ─── IsNowToken / ParseNowToken — wrapper detection ────────────────

func TestIsNowToken_RecognisesWrappedNowForms(t *testing.T) {
	for _, s := range []string{
		"${now}",
		"${ now }",
		"${now - 2m}",
		"${now+1h}",
	} {
		assert.True(t, IsNowToken(s), "must recognise %q as a now-token", s)
	}
}

func TestIsNowToken_RejectsNonNowOrUnwrapped(t *testing.T) {
	for _, s := range []string{
		"now",                   // unwrapped scalar
		"${alice.id}",           // user placeholder, not now
		"${bucket(created_at)}", // bucket token, not now
		"${nope}",
		"",
	} {
		assert.False(t, IsNowToken(s), "must NOT recognise %q as a now-token", s)
	}
}

// ─── ResolveCreatedAtToken — wrapped-form convenience ─────────────

func TestResolveCreatedAtToken_BareNowReturnsTOpen(t *testing.T) {
	got, err := ResolveCreatedAtToken("${now}", fixedTOpen)
	require.NoError(t, err)
	assert.Equal(t, fixedTOpen, got)
}

func TestResolveCreatedAtToken_NegativeOffsetShiftsBackward(t *testing.T) {
	got, err := ResolveCreatedAtToken("${now - 1h}", fixedTOpen)
	require.NoError(t, err)
	assert.Equal(t, fixedTOpen.Add(-time.Hour), got)
}

func TestResolveCreatedAtToken_NonTokenRejected(t *testing.T) {
	_, err := ResolveCreatedAtToken("2024-10-27T00:00:00Z", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not a now token")
}

func TestResolveCreatedAtToken_MalformedInnerExprBubblesParseError(t *testing.T) {
	_, err := ResolveCreatedAtToken("${now - 1 hour}", fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `"1 hour"`)
}

// ─── ResolveBucketToken / IsBucketToken ────────────────────────────

func TestIsBucketToken_RecognisesWrappedBucketForms(t *testing.T) {
	for _, s := range []string{
		"${bucket(created_at)}",
		"${ bucket( created_at ) }",
		"${bucket(ts)}",
	} {
		assert.True(t, IsBucketToken(s), "must recognise %q", s)
	}
}

func TestIsBucketToken_RejectsNonBucketOrUnwrapped(t *testing.T) {
	for _, s := range []string{
		"bucket(created_at)", // unwrapped
		"${bucket}",          // no parens
		"${bucket()}",        // empty column
		"${now}",
		"${alice.id}",
		"",
	} {
		assert.False(t, IsBucketToken(s), "must NOT recognise %q", s)
	}
}

func TestParseBucketToken_ExtractsColumnName(t *testing.T) {
	col, err := ParseBucketToken("${bucket(created_at)}")
	require.NoError(t, err)
	assert.Equal(t, "created_at", col)
}

func TestParseBucketToken_TolerantOfInternalAndExternalWhitespace(t *testing.T) {
	col, err := ParseBucketToken("${ bucket(  created_at  ) }")
	require.NoError(t, err)
	assert.Equal(t, "created_at", col)
}

func TestParseBucketToken_RejectsMalformedTokens(t *testing.T) {
	for _, in := range []string{
		"bucket(created_at)", // unwrapped
		"${bucket}",          // no parens
		"${bucket()}",        // empty column
		"${bucket(Foo)}",     // uppercase column
		"${bucket(1col)}",    // leading digit
	} {
		_, err := ParseBucketToken(in)
		assert.Error(t, err, "must reject %q", in)
	}
}

// ─── ResolveRowNowTokens — pass 1 over a row map ───────────────────

func TestResolveRowNowTokens_ReplacesEveryNowTokenInRow(t *testing.T) {
	row := SeedCassandraRow{
		"room_id":    "r-history-test",
		"message_id": "m-one",
		"created_at": "${now - 2m}",
		"updated_at": "${now + 1h}",
	}
	require.NoError(t, ResolveRowNowTokens(row, fixedTOpen))
	assert.Equal(t, "r-history-test", row["room_id"], "non-token strings must pass through unchanged")
	assert.Equal(t, "m-one", row["message_id"])
	assert.Equal(t, fixedTOpen.Add(-2*time.Minute), row["created_at"])
	assert.Equal(t, fixedTOpen.Add(1*time.Hour), row["updated_at"])
}

func TestResolveRowNowTokens_NonStringValuesUntouched(t *testing.T) {
	row := SeedCassandraRow{
		"count":      int64(42),
		"ratio":      1.5,
		"active":     true,
		"created_at": "${now}",
	}
	require.NoError(t, ResolveRowNowTokens(row, fixedTOpen))
	assert.Equal(t, int64(42), row["count"])
	assert.Equal(t, 1.5, row["ratio"])
	assert.Equal(t, true, row["active"])
	assert.Equal(t, fixedTOpen, row["created_at"])
}

func TestResolveRowNowTokens_MalformedTokenReturnsColumnCoordinate(t *testing.T) {
	row := SeedCassandraRow{
		"room_id":    "r-x",
		"created_at": "${now - 2 minutes}",
	}
	err := ResolveRowNowTokens(row, fixedTOpen)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `column "created_at"`,
		"error must name the column so the author knows where to look")
	assert.Contains(t, err.Error(), `"2 minutes"`,
		"error must quote the offending duration")
}

func TestResolveRowNowTokens_DeterministicErrorOrderAcrossRuns(t *testing.T) {
	// Two columns with malformed tokens; sorted iteration ensures the
	// alphabetically-first column ("alpha") is the one surfaced every
	// time, regardless of Go's random map iteration.
	row := SeedCassandraRow{
		"zulu":  "${now * 2m}",
		"alpha": "${now / 2m}",
	}
	first := ResolveRowNowTokens(copyRow(row), fixedTOpen).Error()
	for i := 0; i < 10; i++ {
		assert.Equal(t, first, ResolveRowNowTokens(copyRow(row), fixedTOpen).Error(),
			"sorted iteration must produce stable errors")
	}
	assert.Contains(t, first, `column "alpha"`,
		"the lexically-first column must be the named one")
}

// ─── ResolveRowBucketTokens — pass 2 ───────────────────────────────

func TestResolveRowBucketTokens_ComputesBucketAgainstResolvedColumn(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := SeedCassandraRow{
		"room_id":    "r-x",
		"created_at": fixedTOpen, // already resolved by pass 1
		"bucket":     "${bucket(created_at)}",
	}
	require.NoError(t, ResolveRowBucketTokens(row, sizer))
	assert.Equal(t, sizer.Of(fixedTOpen), row["bucket"],
		"bucket must equal sizer.Of(created_at)")
}

func TestResolveRowBucketTokens_MissingColumnRejected(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := SeedCassandraRow{
		"room_id": "r-x",
		"bucket":  "${bucket(created_at)}",
	}
	err := ResolveRowBucketTokens(row, sizer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `column "bucket"`,
		"row coordinate must name the column carrying the token")
	assert.Contains(t, err.Error(), `bucket(created_at)`,
		"error must echo the bucket reference so the author can fix it")
	assert.Contains(t, err.Error(), "no created_at column")
}

func TestResolveRowBucketTokens_NonTimeReferentRejected(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := SeedCassandraRow{
		"name":   "alice",
		"bucket": "${bucket(name)}",
	}
	err := ResolveRowBucketTokens(row, sizer)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected time.Time")
	assert.Contains(t, err.Error(), "got string",
		"error must name the actual type so the author sees the mismatch")
}

func TestResolveRowBucketTokens_NoBucketTokensIsNoOp(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := SeedCassandraRow{
		"room_id":    "r-x",
		"created_at": fixedTOpen,
		"msg":        "msg one",
	}
	require.NoError(t, ResolveRowBucketTokens(row, sizer))
	// Row unchanged.
	assert.Equal(t, "r-x", row["room_id"])
	assert.Equal(t, fixedTOpen, row["created_at"])
	assert.Equal(t, "msg one", row["msg"])
}

func TestResolveRowBucketTokens_DeterministicErrorOrderAcrossRuns(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := SeedCassandraRow{
		"zulu_bucket":  "${bucket(missing_z)}",
		"alpha_bucket": "${bucket(missing_a)}",
	}
	first := ResolveRowBucketTokens(copyRow(row), sizer).Error()
	for i := 0; i < 10; i++ {
		assert.Equal(t, first, ResolveRowBucketTokens(copyRow(row), sizer).Error())
	}
	assert.Contains(t, first, `column "alpha_bucket"`)
}

// copyRow returns a shallow copy so a test can re-run resolution
// against the same starting state without one iteration mutating the
// next.
func copyRow(in SeedCassandraRow) SeedCassandraRow {
	out := make(SeedCassandraRow, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
