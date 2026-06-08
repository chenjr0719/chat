//go:build integration

package runtime

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
)

// setupKeyspaceWithMessageTables provisions an isolated keyspace and
// creates the four production message tables + the Participant UDT
// they reference. Returns a keyspace-scoped session so the test can
// SELECT against unqualified table names (matching how the production
// cassandra_select primitive issues queries).
func setupKeyspaceWithMessageTables(t *testing.T, prefix string) (keyspace string, sess *gocql.Session) {
	t.Helper()
	keyspace, adminSess, host := testutil.CassandraKeyspace(t, prefix)
	ddl := []string{
		fmt.Sprintf(`CREATE TYPE IF NOT EXISTS %s."Participant" (
			id TEXT, eng_name TEXT, company_name TEXT, app_id TEXT,
			app_name TEXT, is_bot BOOLEAN, account TEXT
		)`, keyspace),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.messages_by_room (
			room_id TEXT, bucket BIGINT, created_at TIMESTAMP, message_id TEXT,
			sender FROZEN<"Participant">, msg TEXT, site_id TEXT,
			updated_at TIMESTAMP,
			PRIMARY KEY ((room_id, bucket), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`, keyspace),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.messages_by_id (
			message_id TEXT, created_at TIMESTAMP, room_id TEXT,
			sender FROZEN<"Participant">, msg TEXT, site_id TEXT,
			updated_at TIMESTAMP,
			PRIMARY KEY (message_id, created_at)
		) WITH CLUSTERING ORDER BY (created_at DESC)`, keyspace),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.thread_messages_by_room (
			room_id TEXT, bucket BIGINT, thread_room_id TEXT,
			created_at TIMESTAMP, message_id TEXT, thread_parent_id TEXT,
			sender FROZEN<"Participant">, msg TEXT, site_id TEXT,
			updated_at TIMESTAMP,
			PRIMARY KEY ((room_id, bucket), thread_room_id, created_at, message_id)
		) WITH CLUSTERING ORDER BY (thread_room_id DESC, created_at DESC, message_id DESC)`, keyspace),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s.pinned_messages_by_room (
			room_id TEXT, created_at TIMESTAMP, message_id TEXT,
			sender FROZEN<"Participant">, msg TEXT, site_id TEXT,
			updated_at TIMESTAMP,
			PRIMARY KEY ((room_id), created_at, message_id)
		) WITH CLUSTERING ORDER BY (created_at DESC, message_id DESC)`, keyspace),
	}
	for _, stmt := range ddl {
		require.NoError(t, adminSess.Query(stmt).Exec(), "DDL must succeed: %s", stmt)
	}

	cluster := gocql.NewCluster(host)
	cluster.Consistency = gocql.One
	cluster.DisableInitialHostLookup = true
	cluster.Keyspace = keyspace
	ksSess, err := cluster.CreateSession()
	require.NoError(t, err)
	t.Cleanup(ksSess.Close)
	return keyspace, ksSess
}

// newCassandraIntegrationSandbox returns a Sandbox wired with the
// keyspace-scoped session + a fake chaos engine + the seed-effect
// registry, ready for sb.Setup() invocation. Mongo intentionally nil
// — the room-seed path is the previous phase's concern and unrelated
// to the Cassandra-seed surface under test.
func newCassandraIntegrationSandbox(t *testing.T, s *scenario.Scenario, keyspace string, sess *gocql.Session) *Sandbox {
	t.Helper()
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	return NewSandbox(s, &SandboxDeps{
		Cassandra:         sess,
		CassandraKeyspace: keyspace,
		Chaos:             mishap.NewFakeChaosEngine(),
		SeedEffectReg:     reg,
		SiteID:            "site-local",
	})
}

// ─── 5.1: full round-trip across two tables ───────────────────────

func TestSandboxSetup_SeedCassandraRowsRoundTrip(t *testing.T) {
	keyspace, sess := setupKeyspaceWithMessageTables(t, "phase4_3_roundtrip")

	s := &scenario.Scenario{
		Name: "cassandra-roundtrip",
		Seed: scenario.SeedBlock{
			CassandraData: []scenario.SeedCassandraTable{
				{
					Table: "messages_by_room",
					Rows: []scenario.SeedCassandraRow{
						{
							"room_id":    "r-history-test",
							"message_id": "mPastOne00000000000000",
							"msg":        "msg one",
							"site_id":    "${site}",
							"created_at": "${now - 2m}",
						},
						{
							"room_id":    "r-history-test",
							"message_id": "mPastTwo00000000000000",
							"msg":        "msg two",
							"site_id":    "${site}",
							"created_at": "${now - 1m}",
						},
					},
				},
				{
					Table: "messages_by_id",
					Rows: []scenario.SeedCassandraRow{
						{
							"message_id": "mPastOne00000000000000",
							"room_id":    "r-history-test",
							"msg":        "msg one",
							"site_id":    "${site}",
							"created_at": "${now - 2m}",
						},
					},
				},
			},
		},
	}

	sb := newCassandraIntegrationSandbox(t, s, keyspace, sess)
	require.NoError(t, sb.Setup(context.Background()))

	t.Run("messages_by_room rows persisted in order with auto-bucket", func(t *testing.T) {
		var (
			roomID, msg, siteID string
			bucket              int64
			createdAt           time.Time
			rowCount            int
		)
		iter := sess.Query(`SELECT room_id, bucket, created_at, msg, site_id FROM messages_by_room
			WHERE room_id = ? ALLOW FILTERING`, "r-history-test").Iter()
		for iter.Scan(&roomID, &bucket, &createdAt, &msg, &siteID) {
			rowCount++
			assert.Equal(t, "site-local", siteID, "${site} resolved")
			assert.NotZero(t, bucket, "auto-bucket must inject a non-zero bucket value")
		}
		require.NoError(t, iter.Close())
		assert.Equal(t, 2, rowCount, "exactly two rows seeded")
	})

	t.Run("created_at lands within tolerance of T_open − N", func(t *testing.T) {
		var ts time.Time
		require.NoError(t, sess.Query(
			`SELECT created_at FROM messages_by_room WHERE message_id = ? ALLOW FILTERING`,
			"mPastOne00000000000000",
		).Scan(&ts))
		expected := sb.StartTime.UTC().Add(-2 * time.Minute)
		diff := ts.Sub(expected)
		if diff < 0 {
			diff = -diff
		}
		assert.LessOrEqual(t, diff, 10*time.Millisecond,
			"created_at must equal T_open - 2m (within 10ms tolerance)")
	})

	t.Run("messages_by_id receives its row", func(t *testing.T) {
		var msg string
		require.NoError(t, sess.Query(
			`SELECT msg FROM messages_by_id WHERE message_id = ?`,
			"mPastOne00000000000000",
		).Scan(&msg))
		assert.Equal(t, "msg one", msg)
	})
}

// ─── 5.2: TRUNCATE drop discipline ────────────────────────────────

func TestSandboxSetup_TruncatesOwnedTablesAtSetup(t *testing.T) {
	keyspace, sess := setupKeyspaceWithMessageTables(t, "phase4_3_truncate")
	// Pre-populate a stale row directly (bypassing the seed engine) so
	// we can prove Setup wipes it before any seed insert runs.
	require.NoError(t, sess.Query(
		`INSERT INTO messages_by_room (room_id, bucket, created_at, message_id, msg)
		 VALUES (?, ?, ?, ?, ?)`,
		"r-stale", int64(1000), time.Now().UTC(), "mStale000000000000000", "stale",
	).Exec())

	s := &scenario.Scenario{
		Name: "cassandra-truncate-test",
		// No cassandra_data — proves TRUNCATE runs even without seed inserts.
	}
	sb := newCassandraIntegrationSandbox(t, s, keyspace, sess)
	require.NoError(t, sb.Setup(context.Background()))

	var msg string
	err := sess.Query(`SELECT msg FROM messages_by_room WHERE message_id = ? ALLOW FILTERING`,
		"mStale000000000000000").Scan(&msg)
	assert.ErrorIs(t, err, gocql.ErrNotFound,
		"stale row must be gone after Setup's TRUNCATE pass")
}

// ─── 5.3: nil-tolerance ───────────────────────────────────────────

func TestSandboxSetup_CassandraNilSkipsEngine(t *testing.T) {
	// Sandbox with Cassandra=nil but a non-empty CassandraData block.
	// Setup must short-circuit cleanly — Step 4b's truncate AND Step 9's
	// insert both gated on sb.Deps.Cassandra != nil.
	s := &scenario.Scenario{
		Name: "cassandra-nil-tolerant",
		Seed: scenario.SeedBlock{
			CassandraData: []scenario.SeedCassandraTable{
				{
					Table: "messages_by_room",
					Rows: []scenario.SeedCassandraRow{
						{"room_id": "r-x", "message_id": "m1", "created_at": "${now}"},
					},
				},
			},
		},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
		// Cassandra intentionally unset.
	})
	require.NoError(t, sb.Setup(context.Background()),
		"Setup must complete cleanly when Cassandra=nil even with declared cassandra_data")
}

// ─── 5.4: explicit bucket token = auto-bucket ─────────────────────

func TestSandboxSetup_ExplicitBucketTokenWinsAndMatchesAutoBucket(t *testing.T) {
	keyspace, sess := setupKeyspaceWithMessageTables(t, "phase4_3_bucket_parity")
	s := &scenario.Scenario{
		Name: "cassandra-bucket-parity",
		Seed: scenario.SeedBlock{
			CassandraData: []scenario.SeedCassandraTable{
				{
					Table: "messages_by_room",
					Rows: []scenario.SeedCassandraRow{
						{
							"room_id":    "r-explicit",
							"message_id": "mExplicit00000000000",
							"created_at": "${now - 5m}",
							"bucket":     "${bucket(created_at)}",
						},
						{
							"room_id":    "r-auto",
							"message_id": "mAuto00000000000000",
							"created_at": "${now - 5m}",
							// no explicit bucket → auto path runs
						},
					},
				},
			},
		},
	}
	sb := newCassandraIntegrationSandbox(t, s, keyspace, sess)
	require.NoError(t, sb.Setup(context.Background()))

	var bExplicit, bAuto int64
	require.NoError(t, sess.Query(
		`SELECT bucket FROM messages_by_room WHERE message_id = ? ALLOW FILTERING`,
		"mExplicit00000000000",
	).Scan(&bExplicit))
	require.NoError(t, sess.Query(
		`SELECT bucket FROM messages_by_room WHERE message_id = ? ALLOW FILTERING`,
		"mAuto00000000000000",
	).Scan(&bAuto))
	assert.Equal(t, bExplicit, bAuto,
		"explicit ${bucket(created_at)} and auto-bucket must produce identical bucket values")
}

// ─── 5.5: placeholder-ref in a seed value ─────────────────────────

func TestSandboxSetup_PlaceholderRefInCassandraValue(t *testing.T) {
	keyspace, sess := setupKeyspaceWithMessageTables(t, "phase4_3_placeholder")
	s := &scenario.Scenario{
		Name: "cassandra-placeholder",
		Seed: scenario.SeedBlock{
			// Materialize alice so Placeholders gets built at Step 7.
			Users: map[string]scenario.SeedUserFlags{
				"alice": {"verified": true},
			},
			CassandraData: []scenario.SeedCassandraTable{
				{
					Table: "messages_by_id",
					Rows: []scenario.SeedCassandraRow{
						{
							"message_id": "mPlaceholder000000000",
							"room_id":    "r-x",
							"msg":        "${alice.account} says hi",
							"created_at": "${now - 1m}",
						},
					},
				},
			},
		},
	}
	sb := newCassandraIntegrationSandbox(t, s, keyspace, sess)
	// VerifiedEffect requires AuthURL to mint a NATS identity. We don't
	// care about JWTs for this assertion, but the materialization loop
	// expects AuthURL to be either unset (skip mint) or reachable. The
	// integration sandbox sets it to "" so MintNATSIdentity is skipped.
	require.NoError(t, sb.Setup(context.Background()))

	var msg string
	require.NoError(t, sess.Query(
		`SELECT msg FROM messages_by_id WHERE message_id = ?`,
		"mPlaceholder000000000",
	).Scan(&msg))
	assert.Equal(t, "alice says hi", msg,
		"${alice.account} must resolve to the materialised user's account")
}
