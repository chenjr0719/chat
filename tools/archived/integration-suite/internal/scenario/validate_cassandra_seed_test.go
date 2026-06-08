package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// ─── Pass-cleanly cases ────────────────────────────────────────────

func TestValidateCassandraData_EmptyBlockIsNoOp(t *testing.T) {
	// Every pre-Phase-4.3 scenario MUST keep validating without
	// declaring cassandra_data — nil/empty stays the no-op default.
	assert.NoError(t, ValidateCassandraData(SeedBlock{}))
	assert.NoError(t, ValidateCassandraData(SeedBlock{
		Users: map[string]SeedUserFlags{"alice": {"verified": true}},
	}))
}

func TestValidateCassandraData_FullyValidBlockAccepted(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{
						"room_id":    "r-history-test",
						"message_id": "mPastOne00000000000000",
						"msg":        "msg one",
						"created_at": "${now - 2m}",
					},
					{
						"room_id":    "r-history-test",
						"message_id": "mPastTwo00000000000000",
						"msg":        "msg two",
						"created_at": "${now - 1m}",
					},
				},
			},
			{
				Table: "messages_by_id",
				Rows: []SeedCassandraRow{
					{
						"message_id": "mPastOne00000000000000",
						"room_id":    "r-history-test",
						"msg":        "msg one",
						"created_at": "${now - 2m}",
					},
				},
			},
		},
	}
	assert.NoError(t, ValidateCassandraData(seed))
}

func TestValidateCassandraData_ExplicitBucketTokenAccepted(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{
						"room_id":    "r-x",
						"created_at": "${now - 2m}",
						"bucket":     "${bucket(created_at)}",
						"message_id": "mEdge00000000000000",
					},
				},
			},
		},
	}
	assert.NoError(t, ValidateCassandraData(seed),
		"bucket token referencing a column declared in the same row must validate")
}

// ─── Rule 1 — invalid table name ───────────────────────────────────

func TestValidateCassandraData_InvalidTableNameRejected(t *testing.T) {
	cases := []struct {
		name  string
		table string
		want  string
	}{
		{"with spaces", "messages by room", `invalid table name "messages by room"`},
		{"with uppercase", "MessagesByRoom", `invalid table name "MessagesByRoom"`},
		{"leading digit", "1messages", `invalid table name "1messages"`},
		{"with hyphen", "messages-by-room", `invalid table name "messages-by-room"`},
		{"empty", "", `invalid table name ""`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := SeedBlock{
				CassandraData: []SeedCassandraTable{
					{Table: tc.table, Rows: []SeedCassandraRow{{"col": "v"}}},
				},
			}
			err := ValidateCassandraData(seed)
			require.Error(t, err)
			assert.Contains(t, err.Error(), `cassandra_data[0]`,
				"error must name the table-index coordinate")
			assert.Contains(t, err.Error(), tc.want,
				"error must quote the offending table literal")
		})
	}
}

// ─── Rule 2 — empty rows list ──────────────────────────────────────

func TestValidateCassandraData_EmptyRowsRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{Table: "messages_by_room", Rows: []SeedCassandraRow{}},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		`cassandra_data[messages_by_room]: rows: must have at least one entry`)
}

func TestValidateCassandraData_NilRowsRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{Table: "messages_by_room", Rows: nil},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		`cassandra_data[messages_by_room]: rows: must have at least one entry`)
}

// ─── Rule 3 — empty row map ────────────────────────────────────────

func TestValidateCassandraData_EmptyRowMapRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{Table: "messages_by_room", Rows: []SeedCassandraRow{
				{"room_id": "r-x"}, // good row first
				{},                 // empty second row
			}},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		`cassandra_data[messages_by_room][1]: empty row`,
		"error must pinpoint the empty row's index")
}

// ─── Rule 4 — invalid column name ──────────────────────────────────

func TestValidateCassandraData_InvalidColumnNameRejected(t *testing.T) {
	cases := []struct {
		name string
		col  string
	}{
		{"uppercase", "RoomID"},
		{"camelCase", "roomId"},
		{"leading digit", "1col"},
		{"hyphen", "room-id"},
		{"space", "room id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := SeedBlock{
				CassandraData: []SeedCassandraTable{
					{Table: "messages_by_room", Rows: []SeedCassandraRow{{tc.col: "v"}}},
				},
			}
			err := ValidateCassandraData(seed)
			require.Error(t, err)
			assert.Contains(t, err.Error(),
				`cassandra_data[messages_by_room][0]`,
				"error must name (table, row-index) coordinate")
			assert.Contains(t, err.Error(),
				`invalid column `,
				"error must label the failure as a column name violation")
			assert.Contains(t, err.Error(), `"`+tc.col+`"`,
				"error must quote the offending column literal")
		})
	}
}

// ─── Rule 5 — bucket token references missing column ──────────────

func TestValidateCassandraData_BucketReferencesMissingColumnRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{
						"room_id": "r-x",
						"bucket":  "${bucket(created_at)}", // no created_at in row
					},
				},
			},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		`cassandra_data[messages_by_room][0]: bucket(created_at): row has no created_at column`)
}

func TestValidateCassandraData_BucketReferencesPresentColumnAccepted(t *testing.T) {
	// Green companion to the rule-5 Red: as long as the referenced
	// column is *declared* in the row, validation passes — type checks
	// happen at substitution time, not validation time.
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{
						"room_id":    "r-x",
						"created_at": "${now - 2m}",
						"bucket":     "${bucket(created_at)}",
					},
				},
			},
		},
	}
	assert.NoError(t, ValidateCassandraData(seed))
}

// ─── Rule 6 — malformed ${now ± duration} ─────────────────────────

func TestValidateCassandraData_MalformedNowDurationRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{
						"room_id":    "r-x",
						"created_at": "${now - 2 minutes}", // bad duration literal
					},
				},
			},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(),
		`cassandra_data[messages_by_room][0]`,
		"error must name (table, row-index)")
	assert.Contains(t, err.Error(),
		`column "created_at"`,
		"error must name the offending column")
	assert.Contains(t, err.Error(),
		`"2 minutes"`,
		"error must quote the offending duration")
}

func TestValidateCassandraData_UnknownNowOperatorRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{"created_at": "${now * 2m}"},
				},
			},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `operator "*"`)
}

func TestValidateCassandraData_MissingDurationAfterOperatorRejected(t *testing.T) {
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{"created_at": "${now -}"},
				},
			},
		},
	}
	err := ValidateCassandraData(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing duration after operator")
}

// ─── Determinism: error ordering across runs ──────────────────────

func TestValidateCassandraData_DeterministicErrorOrderAcrossRuns(t *testing.T) {
	// Two columns with malformed now tokens — sorted column iteration
	// must pick the lexically-first column ("alpha") every time.
	seed := SeedBlock{
		CassandraData: []SeedCassandraTable{
			{
				Table: "messages_by_room",
				Rows: []SeedCassandraRow{
					{
						"zulu":  "${now * 2m}",
						"alpha": "${now / 2m}",
					},
				},
			},
		},
	}
	first := ValidateCassandraData(seed).Error()
	for i := 0; i < 10; i++ {
		assert.Equal(t, first, ValidateCassandraData(seed).Error(),
			"validation errors must be deterministic across runs")
	}
	assert.Contains(t, first, `column "alpha"`,
		"lexically-first column must be the one surfaced")
}

// ─── YAML decoding sanity ─────────────────────────────────────────

func TestSeedBlock_DecodesCassandraDataFromYAML(t *testing.T) {
	const src = `
cassandra_data:
  - table: messages_by_room
    rows:
      - room_id: r-history-test
        message_id: mPastOne00000000000000
        msg: msg one
        created_at: "${now - 2m}"
      - room_id: r-history-test
        message_id: mPastTwo00000000000000
        msg: msg two
        created_at: "${now - 1m}"
  - table: messages_by_id
    rows:
      - message_id: mPastOne00000000000000
        room_id: r-history-test
        msg: msg one
        created_at: "${now - 2m}"
`
	var sb SeedBlock
	require.NoError(t, yaml.Unmarshal([]byte(src), &sb))
	require.Len(t, sb.CassandraData, 2)
	assert.Equal(t, "messages_by_room", sb.CassandraData[0].Table)
	require.Len(t, sb.CassandraData[0].Rows, 2)
	assert.Equal(t, "r-history-test", sb.CassandraData[0].Rows[0]["room_id"])
	assert.Equal(t, "${now - 2m}", sb.CassandraData[0].Rows[0]["created_at"],
		"token strings must survive YAML decode intact for the substitution layer")
	assert.Equal(t, "messages_by_id", sb.CassandraData[1].Table)
}

func TestSeedBlock_MixedScalarTypesInOneRowDecodeWithoutCoercion(t *testing.T) {
	// YAML's native typing means int → int64 (yaml.v3), bool → bool,
	// string → string. Token strings stay strings. No coercion.
	const src = `
cassandra_data:
  - table: messages_by_room
    rows:
      - room_id: r-x
        tcount: 42
        deleted: false
        created_at: "${now}"
        msg: hello
`
	var sb SeedBlock
	require.NoError(t, yaml.Unmarshal([]byte(src), &sb))
	row := sb.CassandraData[0].Rows[0]
	assert.Equal(t, "r-x", row["room_id"])
	assert.EqualValues(t, 42, row["tcount"])
	assert.Equal(t, false, row["deleted"])
	assert.Equal(t, "${now}", row["created_at"])
	assert.Equal(t, "hello", row["msg"])
}
