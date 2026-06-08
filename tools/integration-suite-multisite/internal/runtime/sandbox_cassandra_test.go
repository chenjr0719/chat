package runtime

import (
	"testing"
	"time"

	"github.com/gocql/gocql"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
)

// TestCoerceColumnTypes_TimestampFromInt covers §2.7 Gap A: YAML
// decodes an unquoted integer literal as Go int, but gocql's
// marshalTimestamp accepts only int64 / time.Time / Marshaler — the
// exact-type switch rejects plain int. Without the coercion pass,
// `created_at: 1748736000000` reaches the binder as int and fails
// at bind time with "can not marshal int into timestamp".
func TestCoerceColumnTypes_TimestampFromInt(t *testing.T) {
	schema := map[string]gocql.Type{
		"message_id": gocql.TypeText,
		"created_at": gocql.TypeTimestamp,
	}
	row := scenario.SeedCassandraRow{
		"message_id": "m-abc",
		"created_at": 1748736000000, // YAML-decoded as Go int
	}

	coerceColumnTypes(row, schema)

	require.IsType(t, time.Time{}, row["created_at"])
	got := row["created_at"].(time.Time)
	assert.Equal(t, int64(1748736000000), got.UnixMilli())
	assert.Equal(t, "m-abc", row["message_id"], "non-timestamp columns must not be touched")
}

// TestCoerceColumnTypes_TimestampFromInt64 covers the other numeric
// shape the binder might see — int64 (e.g. from a programmatic seed
// or from go-yaml when it picks int64 over int).
func TestCoerceColumnTypes_TimestampFromInt64(t *testing.T) {
	schema := map[string]gocql.Type{"created_at": gocql.TypeTimestamp}
	row := scenario.SeedCassandraRow{"created_at": int64(1748736000000)}

	coerceColumnTypes(row, schema)

	require.IsType(t, time.Time{}, row["created_at"])
	assert.Equal(t, int64(1748736000000), row["created_at"].(time.Time).UnixMilli())
}

// TestCoerceColumnTypes_TimeUntouched: a time.Time value (e.g. from
// ResolveRowNowTokens) is already what gocql wants; the coercion
// pass leaves it alone.
func TestCoerceColumnTypes_TimeUntouched(t *testing.T) {
	schema := map[string]gocql.Type{"created_at": gocql.TypeTimestamp}
	when := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	row := scenario.SeedCassandraRow{"created_at": when}

	coerceColumnTypes(row, schema)

	assert.Equal(t, when, row["created_at"])
}

// TestCoerceColumnTypes_NonTimestampColumn: a numeric value bound for
// a non-timestamp column (e.g. tcount on messages_by_id which is
// INT) must NOT be coerced — that would break the bind on the
// correctly-typed column.
func TestCoerceColumnTypes_NonTimestampColumn(t *testing.T) {
	schema := map[string]gocql.Type{
		"tcount": gocql.TypeInt,
	}
	row := scenario.SeedCassandraRow{"tcount": 5}

	coerceColumnTypes(row, schema)

	assert.Equal(t, 5, row["tcount"])
}

// TestCoerceColumnTypes_NilSchema: when the schema lookup is
// unavailable (e.g. in tests, or when KeyspaceMetadata errors), the
// pass no-ops rather than panicking. Mirrors applyAutoBucket's
// existing nil-schema discipline.
func TestCoerceColumnTypes_NilSchema(t *testing.T) {
	row := scenario.SeedCassandraRow{"created_at": 1748736000000}

	coerceColumnTypes(row, nil)

	assert.Equal(t, 1748736000000, row["created_at"], "nil schema must leave the row untouched")
}

// TestNormalizeNamedMaps_SeedRowConvertedToMap covers §2.7 Gap B:
// a nested mapping in YAML decodes as scenario.SeedCassandraRow when
// the parent is decoded as that named type. gocql's marshalUDT
// type-switches on EXACTLY map[string]interface{} and falls through
// for any named map type. Normalize once, all UDT columns work.
func TestNormalizeNamedMaps_SeedRowConvertedToMap(t *testing.T) {
	row := scenario.SeedCassandraRow{
		"sender": scenario.SeedCassandraRow{
			"id":      "u-bob",
			"account": "bob",
		},
	}

	normalizeNamedMaps(row)

	got, ok := row["sender"].(map[string]interface{})
	require.True(t, ok, "sender must be normalized to plain map[string]interface{}")
	assert.Equal(t, "u-bob", got["id"])
	assert.Equal(t, "bob", got["account"])
}

// TestNormalizeNamedMaps_NestedSeedRow: deep nesting (e.g. a
// quoted_parent_message UDT whose `sender` is itself a UDT) must
// normalize at every level. Decisive for any future scenario seeding
// quoted_parent_message.
func TestNormalizeNamedMaps_NestedSeedRow(t *testing.T) {
	row := scenario.SeedCassandraRow{
		"quoted_parent_message": scenario.SeedCassandraRow{
			"message_id": "m-parent",
			"sender": scenario.SeedCassandraRow{
				"id":      "u-carol",
				"account": "carol",
			},
		},
	}

	normalizeNamedMaps(row)

	outer, ok := row["quoted_parent_message"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "m-parent", outer["message_id"])
	inner, ok := outer["sender"].(map[string]interface{})
	require.True(t, ok, "nested SeedCassandraRow must also normalize")
	assert.Equal(t, "carol", inner["account"])
}

// TestNormalizeNamedMaps_ListOfSeedRows: UDT-set / UDT-list columns
// (mentions: set<frozen<Participant>>) decode as []any where each
// element is a SeedCassandraRow. Normalize each element.
func TestNormalizeNamedMaps_ListOfSeedRows(t *testing.T) {
	row := scenario.SeedCassandraRow{
		"mentions": []any{
			scenario.SeedCassandraRow{"id": "u-alice", "account": "alice"},
			scenario.SeedCassandraRow{"id": "u-bob", "account": "bob"},
		},
	}

	normalizeNamedMaps(row)

	mentions, ok := row["mentions"].([]any)
	require.True(t, ok)
	require.Len(t, mentions, 2)
	for i, want := range []string{"alice", "bob"} {
		m, ok := mentions[i].(map[string]interface{})
		require.True(t, ok, "mentions[%d] must normalize to plain map", i)
		assert.Equal(t, want, m["account"])
	}
}

// TestNormalizeNamedMaps_ScalarsUntouched: non-map values pass
// through unchanged.
func TestNormalizeNamedMaps_ScalarsUntouched(t *testing.T) {
	row := scenario.SeedCassandraRow{
		"message_id": "m-abc",
		"created_at": time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		"deleted":    false,
		"tcount":     int64(0),
	}
	want := copyCassandraRow(row)

	normalizeNamedMaps(row)

	for k, v := range want {
		assert.Equal(t, v, row[k], "scalar column %q must not be transformed", k)
	}
}
