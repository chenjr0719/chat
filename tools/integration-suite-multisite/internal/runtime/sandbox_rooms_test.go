package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/seedeffect"
)

// TestBuildRoomDocs_UserCountDerivedFromMemberships covers the default
// path: a SeedRoom with no explicit UserCount writes the membership
// count into Mongo. Preserves the existing behavior for every
// scenario that doesn't set UserCount.
func TestBuildRoomDocs_UserCountDerivedFromMemberships(t *testing.T) {
	tOpen := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	users := map[string]*seedeffect.SeedUser{
		"alice": {ID: "u-alice", Account: "alice"},
		"bob":   {ID: "u-bob", Account: "bob"},
	}
	membersByRoom := map[string][]string{
		"r-general": {"alice", "bob"},
	}
	rooms := []scenario.SeedRoom{
		{ID: "r-general", Name: "General", Type: scenario.RoomTypeChannel},
	}

	docs, err := buildRoomDocs(rooms, membersByRoom, users, "site-a", tOpen)
	require.NoError(t, err)
	require.Len(t, docs, 1)

	doc := docs[0].(bson.M)
	assert.Equal(t, 2, doc["userCount"], "default path: userCount == len(memberships)")
}

// TestBuildRoomDocs_UserCountExplicitOverride covers §2.7 T1: explicit
// UserCount in YAML wins over the derived value. This is the canonical
// shape for scenarios exercising the large-room post restriction —
// the gate keys on rooms.userCount (cached metadata), not on the
// actual subscription rows, so seeding userCount: 501 with two
// members is sufficient to trip the cap.
func TestBuildRoomDocs_UserCountExplicitOverride(t *testing.T) {
	tOpen := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	users := map[string]*seedeffect.SeedUser{
		"alice": {ID: "u-alice", Account: "alice"},
		"bob":   {ID: "u-bob", Account: "bob"},
	}
	membersByRoom := map[string][]string{
		"r-busy": {"alice", "bob"},
	}
	largeCount := 501
	rooms := []scenario.SeedRoom{
		{
			ID:        "r-busy",
			Name:      "BusyChannel",
			Type:      scenario.RoomTypeChannel,
			UserCount: &largeCount,
		},
	}

	docs, err := buildRoomDocs(rooms, membersByRoom, users, "site-a", tOpen)
	require.NoError(t, err)
	require.Len(t, docs, 1)

	doc := docs[0].(bson.M)
	assert.Equal(t, 501, doc["userCount"],
		"explicit override: userCount == *r.UserCount, NOT derived from memberships")
	assert.Len(t, doc["uids"].([]string), 2,
		"actual seeded uids stays at len(memberships) — only the cached metadata is inflated")
}

// TestBuildRoomDocs_UserCountExplicitZero ensures the *int pointer
// shape distinguishes "set to 0" from "not set". A scenario testing
// an empty-room edge case might explicitly write userCount: 0 even
// with no memberships seeded.
func TestBuildRoomDocs_UserCountExplicitZero(t *testing.T) {
	tOpen := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	users := map[string]*seedeffect.SeedUser{
		"alice": {ID: "u-alice", Account: "alice"},
	}
	membersByRoom := map[string][]string{
		"r-empty": {"alice"},
	}
	zero := 0
	rooms := []scenario.SeedRoom{
		{
			ID:        "r-empty",
			Name:      "EmptyEdge",
			Type:      scenario.RoomTypeChannel,
			UserCount: &zero,
		},
	}

	docs, err := buildRoomDocs(rooms, membersByRoom, users, "site-a", tOpen)
	require.NoError(t, err)

	doc := docs[0].(bson.M)
	assert.Equal(t, 0, doc["userCount"], "explicit zero must win over the derived 1")
}
