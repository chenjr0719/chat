package scenario

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Pass-cleanly cases (the "Green" half of each Red-Green pair) ───────

func TestValidateSeedBlock_EmptyIsNoOp(t *testing.T) {
	// Pre-Phase-4.2 scenarios that declare no rooms / no memberships
	// must short-circuit cleanly so no existing scenario starts failing
	// after this code lands.
	assert.NoError(t, ValidateSeedBlock(SeedBlock{}))
	assert.NoError(t, ValidateSeedBlock(SeedBlock{
		Users: map[string]SeedUserFlags{"alice": {"verified": true}},
	}))
}

func TestValidateSeedBlock_FullyValidBlock(t *testing.T) {
	// Realistic shape pulled from the spec's §2.1 example.
	seed := SeedBlock{
		Rooms: []SeedRoom{
			{ID: "r-engineering", Name: "Engineering", Type: RoomTypeChannel},
			{ID: "r-design", Name: "Design", Type: RoomTypeChannel},
			{ID: "r-alice-bob-dm", Type: RoomTypeDM},
		},
		Memberships: map[string][]SeedMembership{
			"alice": {
				{Room: "r-engineering", Roles: []string{RoleOwner}},
				{Room: "r-design"},
				{Room: "r-alice-bob-dm"},
			},
			"bob": {
				{Room: "r-engineering"},
				{Room: "r-alice-bob-dm"},
			},
		},
	}
	assert.NoError(t, ValidateSeedBlock(seed))
}

func TestValidateSeedBlock_RoomTypeEmptyIsValid(t *testing.T) {
	// Empty Type means "default channel applied at insertion time" —
	// MUST NOT trip rule 2.
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-x"}},
	}
	assert.NoError(t, ValidateSeedBlock(seed))
}

// ─── Rule 1 — Red: duplicate room ids ───────────────────────────────────

func TestValidateSeedBlock_DuplicateRoomIDRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{
			{ID: "r-engineering"},
			{ID: "r-engineering"},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms: duplicate id "r-engineering"`,
		"rule 1 must name both the field and the offending id")
}

func TestValidateSeedBlock_MissingRoomIDRejected(t *testing.T) {
	// Defensive: an entry without an id can never resolve from
	// memberships; surface it loudly.
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: ""}},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms[0]: missing id`)
}

// ─── Rule 2 — Red: bogus room type ──────────────────────────────────────

func TestValidateSeedBlock_BogusRoomTypeRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{
			{ID: "r-x", Type: "channel-extra"},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms[r-x]: type "channel-extra" must be one of channel|dm|botDM|discussion`,
		"rule 2 must name the room id, the bad type literal, and the closed enum")
}

// ─── Rule 6 — Red/Green: per-room created_at token (Phase 4.5.1) ────────

func TestValidateSeedBlock_RoomCreatedAtEmptyIsValid(t *testing.T) {
	// Default behavior — empty CreatedAt falls back to sb.StartTime at
	// insertion time. The ten pre-Phase-4.5.1 scenarios rely on this.
	seed := SeedBlock{Rooms: []SeedRoom{{ID: "r-x"}}}
	assert.NoError(t, ValidateSeedBlock(seed))
}

func TestValidateSeedBlock_RoomCreatedAtValidNowTokenAccepted(t *testing.T) {
	seed := SeedBlock{Rooms: []SeedRoom{
		{ID: "r-now", CreatedAt: "${now}"},
		{ID: "r-past", CreatedAt: "${now - 1h}"},
		{ID: "r-future", CreatedAt: "${now + 30m}"},
	}}
	assert.NoError(t, ValidateSeedBlock(seed))
}

func TestValidateSeedBlock_RoomCreatedAtNonTokenRejected(t *testing.T) {
	seed := SeedBlock{Rooms: []SeedRoom{
		{ID: "r-rfc", CreatedAt: "2024-10-27T00:00:00Z"},
	}}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms[r-rfc]: created_at "2024-10-27T00:00:00Z" must be a ${now ± duration} token`)
}

func TestValidateSeedBlock_RoomCreatedAtMalformedDurationRejected(t *testing.T) {
	seed := SeedBlock{Rooms: []SeedRoom{
		{ID: "r-bad", CreatedAt: "${now - 1 hour}"},
	}}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms[r-bad]: created_at`)
	assert.Contains(t, err.Error(), `"1 hour"`)
}

// ─── Rule 3 — Red: membership references undeclared room ────────────────

func TestValidateSeedBlock_MemberOfUndeclaredRoomRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-engineering"}},
		Memberships: map[string][]SeedMembership{
			"alice": {
				{Room: "r-engineering"},
				{Room: "r-nonexistent"}, // bad ref
			},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.memberships[alice][1]: references undeclared room "r-nonexistent"`,
		"rule 3 must name the alias, the membership index, and the offending room id")
}

func TestValidateSeedBlock_EmptyMembershipRoomRejected(t *testing.T) {
	// Defensive: an entry without a room can never resolve; surface it.
	seed := SeedBlock{
		Memberships: map[string][]SeedMembership{
			"alice": {{Room: ""}},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.memberships[alice][0]: missing room`)
}

// ─── Rule 4 — Red: invalid role ─────────────────────────────────────────

func TestValidateSeedBlock_BadRoleRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-engineering"}},
		Memberships: map[string][]SeedMembership{
			"alice": {
				{Room: "r-engineering", Roles: []string{"viewer"}}, // bad role
			},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.memberships[alice][0]: role "viewer" must be one of owner|admin|member`,
		"rule 4 must name the alias, the membership index, and the closed role enum")
}

func TestValidateSeedBlock_AllValidRolesAccepted(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-engineering"}},
		Memberships: map[string][]SeedMembership{
			"alice": {{Room: "r-engineering", Roles: []string{RoleOwner, RoleAdmin, RoleMember}}},
		},
	}
	assert.NoError(t, ValidateSeedBlock(seed))
}

// ─── Rule 5 — Red: DM arity violations ──────────────────────────────────

func TestValidateSeedBlock_DMWithOneMemberRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-lonely-dm", Type: RoomTypeDM}},
		Memberships: map[string][]SeedMembership{
			"alice": {{Room: "r-lonely-dm"}},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms[r-lonely-dm]: dm room needs exactly 2 distinct member aliases, got 1`,
		"rule 5 must name the room and the actual member count")
}

func TestValidateSeedBlock_DMWithThreeMembersRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-crowded-dm", Type: RoomTypeDM}},
		Memberships: map[string][]SeedMembership{
			"alice": {{Room: "r-crowded-dm"}},
			"bob":   {{Room: "r-crowded-dm"}},
			"carol": {{Room: "r-crowded-dm"}},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `seed.rooms[r-crowded-dm]: dm room needs exactly 2 distinct member aliases, got 3`)
}

func TestValidateSeedBlock_DMWithZeroMembersRejected(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-empty-dm", Type: RoomTypeDM}},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `dm room needs exactly 2 distinct member aliases, got 0`)
}

func TestValidateSeedBlock_DMWithExactlyTwoMembersAccepted(t *testing.T) {
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-perfect-dm", Type: RoomTypeDM}},
		Memberships: map[string][]SeedMembership{
			"alice": {{Room: "r-perfect-dm"}},
			"bob":   {{Room: "r-perfect-dm"}},
		},
	}
	assert.NoError(t, ValidateSeedBlock(seed))
}

func TestValidateSeedBlock_DMArityCountsDistinctAliases(t *testing.T) {
	// Defensive: if alice declares the same DM twice in her own
	// membership list (silly but possible), it still counts as ONE
	// distinct alias on that DM's roster — the rule asserts on
	// aliases, not entries.
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-dup-entry-dm", Type: RoomTypeDM}},
		Memberships: map[string][]SeedMembership{
			"alice": {
				{Room: "r-dup-entry-dm"},
				{Room: "r-dup-entry-dm"},
			},
		},
	}
	err := ValidateSeedBlock(seed)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `dm room needs exactly 2 distinct member aliases, got 1`,
		"two entries from the same alias still count as one distinct member")
}

// ─── Determinism: error ordering across runs ────────────────────────────

func TestValidateSeedBlock_AliasOrderingIsDeterministic(t *testing.T) {
	// Same invalid seed, validated 10 times: the same alias's error
	// should surface every time. Without sorted iteration, Go's map
	// randomization could pick a different alias on each run.
	seed := SeedBlock{
		Rooms: []SeedRoom{{ID: "r-x"}},
		Memberships: map[string][]SeedMembership{
			"alice": {{Room: "r-x", Roles: []string{"viewer"}}}, // both alice
			"bob":   {{Room: "r-x", Roles: []string{"viewer"}}}, // and bob have a bad role
		},
	}
	first := ValidateSeedBlock(seed).Error()
	for i := 0; i < 10; i++ {
		assert.Equal(t, first, ValidateSeedBlock(seed).Error(),
			"validation errors must be deterministic across runs (sorted iteration)")
	}
	// And specifically, alice (lexically first) should be the one named.
	assert.Contains(t, first, "alice")
}
