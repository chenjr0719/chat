package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/msgbucket"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
)

// recordedExec captures one Exec call's CQL + binds for assertion.
type recordedExec struct {
	stmt  string
	binds []any
}

// recordingExecutor implements cassandraExecutor by appending every
// (stmt, binds) tuple to an internal slice. Test code reads from
// .calls to assert exact CQL + bind order.
type recordingExecutor struct {
	calls []recordedExec
	// errOn maps a 1-indexed call number → error to return. Empty
	// map means every Exec succeeds.
	errOn map[int]error
}

func (r *recordingExecutor) Exec(_ context.Context, stmt string, binds ...any) error {
	r.calls = append(r.calls, recordedExec{stmt: stmt, binds: binds})
	if err, ok := r.errOn[len(r.calls)]; ok {
		return err
	}
	return nil
}

// fixedT is a deterministic T_open the row-substitution tests anchor
// against. Aligned with the spec's bucket-math examples (midnight UTC
// of a 24h-window day).
var fixedT = time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

// ─── truncate path ─────────────────────────────────────────────────

func TestRunTruncateCassandra_RunsAllFourTablesInOrder(t *testing.T) {
	exec := &recordingExecutor{}
	require.NoError(t, runTruncateCassandra(context.Background(), exec))
	require.Len(t, exec.calls, len(sandboxOwnedCassandraTables))
	for i, table := range sandboxOwnedCassandraTables {
		assert.Equal(t, "TRUNCATE "+table, exec.calls[i].stmt,
			"call %d must truncate %s in declared order", i, table)
		assert.Empty(t, exec.calls[i].binds, "TRUNCATE takes no binds")
	}
}

func TestRunTruncateCassandra_StopsAtFirstFailure(t *testing.T) {
	exec := &recordingExecutor{errOn: map[int]error{2: assert.AnError}}
	err := runTruncateCassandra(context.Background(), exec)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "truncate cassandra "+sandboxOwnedCassandraTables[1],
		"error must localize the failing table")
	assert.Len(t, exec.calls, 2,
		"loop must exit after the second call's error, not attempt the remaining tables")
}

// ─── insert path: empty / nil-tolerant ─────────────────────────────

func TestRunInsertCassandraSeed_NoOpForEmptyCassandraData(t *testing.T) {
	exec := &recordingExecutor{}
	sb := newCassandraTestSandbox(t, scenario.SeedBlock{}) // no cassandra_data
	require.NoError(t, runInsertCassandraSeed(context.Background(), sb, exec, nil, msgbucket.New(24*time.Hour)))
	assert.Empty(t, exec.calls, "empty cassandra_data must produce zero Exec calls")
}

// ─── insert path: column ordering + bind alignment ────────────────

func TestRunInsertCassandraSeed_ColumnsSortedAlphabeticallyAndBindsAligned(t *testing.T) {
	exec := &recordingExecutor{}
	sb := newCassandraTestSandbox(t, scenario.SeedBlock{
		CassandraData: []scenario.SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []scenario.SeedCassandraRow{
					// Intentionally out-of-order declaration.
					{
						"room_id":    "r-history-test",
						"message_id": "mPastOne00000000000000",
						"msg":        "msg one",
						"site_id":    "site-local",
						"created_at": "${now - 2m}",
					},
				},
			},
		},
	})
	require.NoError(t, runInsertCassandraSeed(context.Background(), sb, exec, nil, msgbucket.New(24*time.Hour)))
	require.Len(t, exec.calls, 1)
	// Sorted: created_at, message_id, msg, room_id, site_id.
	expectedStmt := "INSERT INTO messages_by_room (created_at, message_id, msg, room_id, site_id) VALUES (?, ?, ?, ?, ?)"
	assert.Equal(t, expectedStmt, exec.calls[0].stmt)
	require.Len(t, exec.calls[0].binds, 5)
	assert.Equal(t, fixedT.Add(-2*time.Minute), exec.calls[0].binds[0],
		"binds[0] must be created_at, resolved to T_open - 2m")
	assert.Equal(t, "mPastOne00000000000000", exec.calls[0].binds[1])
	assert.Equal(t, "msg one", exec.calls[0].binds[2])
	assert.Equal(t, "r-history-test", exec.calls[0].binds[3])
	assert.Equal(t, "site-local", exec.calls[0].binds[4])
}

func TestRunInsertCassandraSeed_TwoTablesThreeRowsPreserveYAMLOrder(t *testing.T) {
	exec := &recordingExecutor{}
	sb := newCassandraTestSandbox(t, scenario.SeedBlock{
		CassandraData: []scenario.SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []scenario.SeedCassandraRow{
					{"room_id": "r-x", "created_at": "${now}"},
					{"room_id": "r-x", "created_at": "${now - 1m}"},
				},
			},
			{
				Table: "messages_by_id",
				Rows: []scenario.SeedCassandraRow{
					{"message_id": "m1", "created_at": "${now}"},
				},
			},
		},
	})
	require.NoError(t, runInsertCassandraSeed(context.Background(), sb, exec, nil, msgbucket.New(24*time.Hour)))
	require.Len(t, exec.calls, 3)
	assert.Contains(t, exec.calls[0].stmt, "messages_by_room", "first table first")
	assert.Contains(t, exec.calls[1].stmt, "messages_by_room", "second row of first table")
	assert.Contains(t, exec.calls[2].stmt, "messages_by_id", "second table last")
}

// ─── pre-pass: placeholder + site substitution ─────────────────────

func TestRunInsertCassandraSeed_PlaceholderRefResolvesToUserField(t *testing.T) {
	exec := &recordingExecutor{}
	sb := newCassandraTestSandbox(t, scenario.SeedBlock{
		CassandraData: []scenario.SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []scenario.SeedCassandraRow{
					{
						"room_id":    "r-x",
						"user_id":    "${alice.id}",
						"user_acct":  "${alice.account}",
						"site_field": "${site}",
						"created_at": "${now}",
					},
				},
			},
		},
	})
	// Plant alice in Placeholders so Substitute can resolve.
	sb.Placeholders["alice"] = map[string]any{
		"account": "alice",
		"id":      "u-alice",
	}
	require.NoError(t, runInsertCassandraSeed(context.Background(), sb, exec, nil, msgbucket.New(24*time.Hour)))
	require.Len(t, exec.calls, 1)
	// Sorted column order: created_at, room_id, site_field, user_acct, user_id.
	assert.Equal(t, fixedT, exec.calls[0].binds[0], "created_at")
	assert.Equal(t, "r-x", exec.calls[0].binds[1], "room_id")
	assert.Equal(t, "site-local", exec.calls[0].binds[2], "site_field resolves ${site} to the sandbox siteID")
	assert.Equal(t, "alice", exec.calls[0].binds[3], "user_acct resolves ${alice.account}")
	assert.Equal(t, "u-alice", exec.calls[0].binds[4], "user_id resolves ${alice.id}")
}

func TestRunInsertCassandraSeed_UnknownPlaceholderSurfacesRowCoordinate(t *testing.T) {
	exec := &recordingExecutor{}
	sb := newCassandraTestSandbox(t, scenario.SeedBlock{
		CassandraData: []scenario.SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []scenario.SeedCassandraRow{
					{
						"room_id": "r-x",
						"author":  "${nobody.account}", // unresolvable
					},
				},
			},
		},
	})
	err := runInsertCassandraSeed(context.Background(), sb, exec, nil, msgbucket.New(24*time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cassandra_data[0 (messages_by_room)][0]",
		"error must name the (table-index, table-name, row-index) coordinate")
	assert.Contains(t, err.Error(), `column "author"`,
		"error must localize the offending column")
	assert.Empty(t, exec.calls, "no Exec call should fire after substitution failure")
}

// ─── pass 2 + 3: bucket tokens ─────────────────────────────────────

func TestRunInsertCassandraSeed_ExplicitBucketTokenOverridesAutoBucket(t *testing.T) {
	exec := &recordingExecutor{}
	// schema mentions both bucket + created_at, so auto-bucket WOULD
	// fire if the explicit token wasn't there.
	lookup := stubSchema(map[string]map[string]struct{}{
		"messages_by_room": {"bucket": {}, "created_at": {}, "room_id": {}, "message_id": {}},
	})
	sb := newCassandraTestSandbox(t, scenario.SeedBlock{
		CassandraData: []scenario.SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []scenario.SeedCassandraRow{
					{
						"room_id":    "r-x",
						"created_at": "${now - 2m}",
						"bucket":     "${bucket(created_at)}",
						"message_id": "m1",
					},
				},
			},
		},
	})
	sizer := msgbucket.New(24 * time.Hour)
	require.NoError(t, runInsertCassandraSeed(context.Background(), sb, exec, lookup, sizer))
	require.Len(t, exec.calls, 1)
	// Sorted: bucket, created_at, message_id, room_id.
	wantTime := fixedT.Add(-2 * time.Minute)
	assert.Equal(t, sizer.Of(wantTime), exec.calls[0].binds[0],
		"explicit ${bucket(created_at)} resolves to the same value auto-bucket would have")
	assert.Equal(t, wantTime, exec.calls[0].binds[1])
}

// ─── pass 3: auto-bucket ───────────────────────────────────────────

func TestApplyAutoBucket_FiresWhenSchemaHasBothColumnsAndRowOmitsBucket(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := scenario.SeedCassandraRow{
		"room_id":    "r-x",
		"created_at": fixedT, // pass-1-resolved
	}
	schema := map[string]struct{}{"bucket": {}, "created_at": {}, "room_id": {}}
	applyAutoBucket(row, schema, sizer)
	assert.Equal(t, sizer.Of(fixedT), row["bucket"],
		"auto-bucket must inject bucket = sizer.Of(created_at)")
}

func TestApplyAutoBucket_SkipsWhenSchemaLacksBucket(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := scenario.SeedCassandraRow{"room_id": "r-x", "created_at": fixedT}
	// messages_by_id has no bucket column in production.
	schema := map[string]struct{}{"created_at": {}, "room_id": {}, "message_id": {}}
	applyAutoBucket(row, schema, sizer)
	_, present := row["bucket"]
	assert.False(t, present, "tables without a bucket column must not gain one")
}

func TestApplyAutoBucket_SkipsWhenSchemaLacksCreatedAt(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := scenario.SeedCassandraRow{"room_id": "r-x"}
	schema := map[string]struct{}{"bucket": {}, "room_id": {}}
	applyAutoBucket(row, schema, sizer)
	_, present := row["bucket"]
	assert.False(t, present)
}

func TestApplyAutoBucket_SkipsWhenRowAlreadySuppliesBucket(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := scenario.SeedCassandraRow{
		"room_id":    "r-x",
		"created_at": fixedT,
		"bucket":     int64(99), // author-supplied
	}
	schema := map[string]struct{}{"bucket": {}, "created_at": {}, "room_id": {}}
	applyAutoBucket(row, schema, sizer)
	assert.Equal(t, int64(99), row["bucket"],
		"author-supplied bucket value must win over auto")
}

func TestApplyAutoBucket_SkipsWhenSchemaIsNil(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := scenario.SeedCassandraRow{"room_id": "r-x", "created_at": fixedT}
	applyAutoBucket(row, nil, sizer)
	_, present := row["bucket"]
	assert.False(t, present, "nil schema → silent no-op (production metadata-load failure path)")
}

func TestApplyAutoBucket_SkipsWhenCreatedAtIsNotTimeTime(t *testing.T) {
	sizer := msgbucket.New(24 * time.Hour)
	row := scenario.SeedCassandraRow{
		"room_id":    "r-x",
		"created_at": "not-a-time", // pass-1 didn't run
	}
	schema := map[string]struct{}{"bucket": {}, "created_at": {}}
	applyAutoBucket(row, schema, sizer)
	_, present := row["bucket"]
	assert.False(t, present, "non-time.Time created_at → silent no-op (defensive)")
}

// ─── statement assembly ───────────────────────────────────────────

func TestBuildInsertStatement_DeterministicAcrossRuns(t *testing.T) {
	row := scenario.SeedCassandraRow{"zulu": 1, "alpha": 2, "midpoint": 3}
	stmt1, binds1 := buildInsertStatement("t", row)
	for i := 0; i < 10; i++ {
		stmt2, binds2 := buildInsertStatement("t", row)
		assert.Equal(t, stmt1, stmt2, "stmt must be byte-identical across runs (sorted iteration)")
		assert.Equal(t, binds1, binds2, "binds must align with the sorted column order")
	}
	assert.Equal(t, "INSERT INTO t (alpha, midpoint, zulu) VALUES (?, ?, ?)", stmt1)
	assert.Equal(t, []any{2, 3, 1}, binds1)
}

// ─── effectiveBucketWindow defaults ────────────────────────────────

func TestEffectiveBucketWindow_ZeroFallsBackToDefault(t *testing.T) {
	assert.Equal(t, defaultBucketWindow, effectiveBucketWindow(0))
}

func TestEffectiveBucketWindow_NegativeFallsBackToDefault(t *testing.T) {
	assert.Equal(t, defaultBucketWindow, effectiveBucketWindow(-1*time.Hour))
}

func TestEffectiveBucketWindow_PositivePassesThrough(t *testing.T) {
	assert.Equal(t, 6*time.Hour, effectiveBucketWindow(6*time.Hour))
}

// ─── insertSeedTokens determinism ──────────────────────────────────

func TestSubstituteCassandraRowTokens_NowAndBucketTokensSkipped(t *testing.T) {
	row := scenario.SeedCassandraRow{
		"created_at": "${now - 2m}",           // skipped — pass 1 owns this
		"bucket":     "${bucket(created_at)}", // skipped — pass 2 owns this
		"site":       "${site}",               // resolved by pre-pass
		"plain":      "hello",                 // untouched
	}
	ctx := Context{Site: "site-local"}
	require.NoError(t, substituteCassandraRowTokens(row, ctx))
	assert.Equal(t, "${now - 2m}", row["created_at"],
		"now-tokens must survive the pre-pass for ResolveRowNowTokens")
	assert.Equal(t, "${bucket(created_at)}", row["bucket"],
		"bucket-tokens must survive the pre-pass for ResolveRowBucketTokens")
	assert.Equal(t, "site-local", row["site"], "${site} resolves at pre-pass")
	assert.Equal(t, "hello", row["plain"])
}

// ─── stub Sandbox helper ───────────────────────────────────────────

// newCassandraTestSandbox returns a Sandbox wired with the scenario,
// fixedT as StartTime, the "site-local" siteID, and the required
// non-nil Chaos + SeedEffectReg fields. Cassandra is intentionally
// nil — the runInsertCassandraSeed orchestration core accepts an
// injected executor so the session is never touched.
func newCassandraTestSandbox(t *testing.T, seed scenario.SeedBlock) *Sandbox {
	t.Helper()
	s := &scenario.Scenario{Name: "phase4.3-unit", Seed: seed}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
		SiteID:        "site-local",
	})
	sb.StartTime = fixedT
	return sb
}

// stubSchema returns a tableColumnsFn over an in-memory map.
func stubSchema(m map[string]map[string]struct{}) tableColumnsFn {
	return func(table string) map[string]struct{} {
		return m[table]
	}
}
