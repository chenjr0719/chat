//go:build integration

package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
)

// fakeAuthForSandbox returns a server that always returns the same JWT.
func fakeAuthForSandbox(t *testing.T, jwt string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"natsJwt": jwt})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestSandboxSetup_DropsCollectionsAndAppliesEffects(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase3-sandbox-setup")
	authURL := fakeAuthForSandbox(t, "test-jwt-token")

	// Pre-populate `users` collection with a row that should be dropped
	// at Setup so we can prove the drop happens.
	_, err := db.Collection("users").InsertOne(ctx, bson.M{"_id": "u-leftover", "account": "leftover"})
	require.NoError(t, err)

	// Same for rooms + subscriptions.
	_, err = db.Collection("rooms").InsertOne(ctx, bson.M{"_id": "r-stale"})
	require.NoError(t, err)
	_, err = db.Collection("subscriptions").InsertOne(ctx, bson.M{"_id": "sub-stale"})
	require.NoError(t, err)

	// Scenario with two users — alice verified, eve unverified.
	s := &scenario.Scenario{
		Name: "sandbox-setup-test",
		Seed: scenario.SeedBlock{
			Users: map[string]scenario.SeedUserFlags{
				"alice":          {"verified": true},
				"eve_unverified": {"verified": false},
			},
		},
	}

	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)

	sb := NewSandbox(s, &SandboxDeps{
		Mongo:         db,
		AuthURL:       authURL,
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})

	require.NoError(t, sb.Setup(ctx))

	// Leftover rows dropped.
	stale := db.Collection("users").FindOne(ctx, bson.M{"_id": "u-leftover"})
	assert.Error(t, stale.Err(), "leftover user should have been dropped at Setup")

	// Phase 3.8 split: every seeded user gets a NATS identity from
	// Sandbox.Setup → MintNATSIdentity. The `verified` flag now rides
	// on SeedUser.Verified (set by VerifiedEffect) and is persisted
	// into the Mongo user-profile doc; it no longer gates the JWT.
	alice, ok := sb.Users["alice"]
	require.True(t, ok)
	assert.Equal(t, "alice", alice.Account)
	assert.Equal(t, "u-alice", alice.ID)
	assert.Equal(t, "test-jwt-token", alice.JWT)
	assert.NotEmpty(t, alice.NkeySeed)
	assert.True(t, alice.Verified, "VerifiedEffect must set Verified=true")

	eve, ok := sb.Users["eve_unverified"]
	require.True(t, ok)
	assert.Equal(t, "eve_unverified", eve.Account)
	assert.Equal(t, "u-eve_unverified", eve.ID)
	assert.Equal(t, "test-jwt-token", eve.JWT, "unverified users still get a JWT (Phase 3.8)")
	assert.NotEmpty(t, eve.NkeySeed, "unverified users still get an nkey (Phase 3.8)")
	assert.False(t, eve.Verified, "without VerifiedEffect, Verified stays false")

	// Placeholders map populated correctly for substitution use.
	require.NotNil(t, sb.Placeholders["alice"])
	assert.Equal(t, "alice", sb.Placeholders["alice"]["account"])
	assert.Equal(t, "u-alice", sb.Placeholders["alice"]["id"])
	assert.Equal(t, "test-jwt-token", sb.Placeholders["alice"]["jwt"])
	assert.NotEmpty(t, sb.Placeholders["alice"]["nkey"])

	require.NotNil(t, sb.Placeholders["eve_unverified"])
	assert.Equal(t, "eve_unverified", sb.Placeholders["eve_unverified"]["account"])
	assert.Equal(t, "test-jwt-token", sb.Placeholders["eve_unverified"]["jwt"],
		"Phase 3.8: eve's placeholder JWT mirrors the minted credential")

	// Phase 3.7: every materialized user must have a minimal profile
	// doc in the users collection so room-service's create-room guard
	// at room-service/handler.go:186-188 (non-empty EngName +
	// ChineseName) passes. VerifiedEffect alone only mints a NATS
	// identity; without this step every positive case fails with
	// "user not found".
	wantVerified := map[string]bool{"alice": true, "eve_unverified": false}
	for account, expectVerified := range wantVerified {
		var got map[string]any
		require.NoError(t, db.Collection("users").FindOne(ctx, bson.M{"_id": "u-" + account}).Decode(&got),
			"user profile doc for %q must be inserted by Setup", account)
		assert.Equal(t, account, got["account"])
		assert.NotEmpty(t, got["engName"], "EngName must be non-empty (room-service guard)")
		assert.NotEmpty(t, got["chineseName"], "ChineseName must be non-empty (room-service guard)")
		assert.Equal(t, expectVerified, got["verified"],
			"Phase 3.8: verified flag mirrors VerifiedEffect application for %q", account)
	}
}

func TestSandboxSetup_UnknownFlagRejected(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase3-sandbox-unknown-flag")

	s := &scenario.Scenario{
		Name: "sandbox-bad-flag",
		Seed: scenario.SeedBlock{
			Users: map[string]scenario.SeedUserFlags{
				"alice": {"never_registered": true},
			},
		},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)

	sb := NewSandbox(s, &SandboxDeps{
		Mongo:         db,
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})

	err := sb.Setup(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never_registered")
}

// TestSandboxSetup_SeedRoomsAndMemberships drives the Phase 4.2 seed
// block end-to-end against a real testcontainers Mongo. Asserts on the
// three written collections (rooms, subscriptions, room_members),
// including userCount, sorted+paired uids/accounts, denormalized
// name+roomType on subscriptions, and the deterministic _id scheme.
func TestSandboxSetup_SeedRoomsAndMemberships(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-sandbox-rooms-seed")
	authURL := fakeAuthForSandbox(t, "test-jwt-token")

	s := &scenario.Scenario{
		Name: "rooms-seed-test",
		Seed: scenario.SeedBlock{
			Rooms: []scenario.SeedRoom{
				{ID: "r-engineering", Name: "Engineering", Type: scenario.RoomTypeChannel},
				{ID: "r-design", Name: "Design", Type: scenario.RoomTypeChannel},
			},
			Users: map[string]scenario.SeedUserFlags{
				"alice": {"verified": true},
				"bob":   {"verified": true},
			},
			Memberships: map[string][]scenario.SeedMembership{
				"alice": {
					{Room: "r-engineering", Roles: []string{scenario.RoleOwner}},
					{Room: "r-design"}, // default-member
				},
				"bob": {
					{Room: "r-engineering"}, // default-member
				},
			},
		},
	}

	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)

	sb := NewSandbox(s, &SandboxDeps{
		Mongo:         db,
		AuthURL:       authURL,
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
		SiteID:        "site-local",
	})

	require.NoError(t, sb.Setup(ctx))
	require.False(t, sb.StartTime.IsZero(), "Setup must capture T_open into sb.StartTime")

	// ─── rooms collection ─────────────────────────────────────────────
	// Count first, then per-doc shape.
	roomsCount, err := db.Collection("rooms").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(2), roomsCount, "two rooms declared, two rooms written")

	var eng map[string]any
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-engineering"}).Decode(&eng))
	assert.Equal(t, "Engineering", eng["name"])
	assert.Equal(t, "channel", eng["type"])
	assert.Equal(t, "site-local", eng["siteId"])
	assert.EqualValues(t, 2, eng["userCount"], "r-engineering has 2 members (alice + bob)")
	assert.EqualValues(t, 0, eng["appCount"])
	assert.Equal(t, "", eng["lastMsgId"])
	// uids sorted lexically ("u-alice" < "u-bob"); accounts paired.
	assert.Equal(t, []any{"u-alice", "u-bob"}, eng["uids"], "uids must be sorted by id")
	assert.Equal(t, []any{"alice", "bob"}, eng["accounts"], "accounts must be paired with uids by index")

	var design map[string]any
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-design"}).Decode(&design))
	assert.EqualValues(t, 1, design["userCount"], "r-design has 1 member (alice)")
	assert.Equal(t, []any{"u-alice"}, design["uids"])
	assert.Equal(t, []any{"alice"}, design["accounts"])

	// ─── subscriptions collection ────────────────────────────────────
	subsCount, err := db.Collection("subscriptions").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), subsCount,
		"3 memberships expected: alice→eng, alice→design, bob→eng")

	var aliceEng map[string]any
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx,
		bson.M{"_id": "sub-alice-r-engineering"}).Decode(&aliceEng))
	assert.Equal(t, "r-engineering", aliceEng["roomId"])
	assert.Equal(t, "site-local", aliceEng["siteId"])
	assert.Equal(t, []any{"owner"}, aliceEng["roles"], "explicit role flows through verbatim")
	// Denormalized fields copied from the room.
	assert.Equal(t, "Engineering", aliceEng["name"], "subscription.name copied from room.name")
	assert.Equal(t, "channel", aliceEng["roomType"], "subscription.roomType copied from room.type")
	assert.Equal(t, true, aliceEng["isSubscribed"])
	assert.Equal(t, false, aliceEng["hasMention"])
	assert.Equal(t, false, aliceEng["alert"])
	assert.Equal(t, false, aliceEng["muted"])
	// Embedded user-ref.
	u, _ := aliceEng["u"].(map[string]any)
	require.NotNil(t, u, "subscription.u must be the embedded SubscriptionUser doc")
	assert.Equal(t, "u-alice", u["_id"])
	assert.Equal(t, "alice", u["account"])
	assert.Equal(t, false, u["isBot"])

	var aliceDesign map[string]any
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx,
		bson.M{"_id": "sub-alice-r-design"}).Decode(&aliceDesign))
	assert.Equal(t, []any{"member"}, aliceDesign["roles"],
		"unset roles must default to [member]")
	assert.Equal(t, "Design", aliceDesign["name"])

	var bobEng map[string]any
	require.NoError(t, db.Collection("subscriptions").FindOne(ctx,
		bson.M{"_id": "sub-bob-r-engineering"}).Decode(&bobEng))
	assert.Equal(t, []any{"member"}, bobEng["roles"])

	// ─── room_members collection ──────────────────────────────────────
	memCount, err := db.Collection("room_members").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.Equal(t, int64(3), memCount, "1 room_member per membership")

	var rmAliceEng map[string]any
	require.NoError(t, db.Collection("room_members").FindOne(ctx,
		bson.M{"_id": "rm-alice-r-engineering"}).Decode(&rmAliceEng))
	assert.Equal(t, "r-engineering", rmAliceEng["rid"])
	mem, _ := rmAliceEng["member"].(map[string]any)
	require.NotNil(t, mem, "room_member.member must be present")
	assert.Equal(t, "u-alice", mem["id"])
	assert.Equal(t, "individual", mem["type"])
	assert.Equal(t, "alice", mem["account"])
}

// TestSandboxSetup_RoomMembersCollectionDroppedAtSetup confirms Phase
// 4.2 extended sandboxOwnedCollections — a leftover row in room_members
// from a prior scenario must be wiped at Step 3 so each run starts from
// a byte-identical Mongo state.
// TestSandboxSetup_RoomCreatedAtTokenShiftsTimestamp is the Phase
// 4.5.1 pin: a `created_at: ${now - 1h}` in seed.rooms must land in
// the Mongo room doc as sb.StartTime - 1h, NOT sb.StartTime. This is
// the fix for the history-service bucket-walk floor issue
// (Finding 19) — without per-room createdAt the floor collapses to
// T_open and excludes pre-seeded historical messages.
func TestSandboxSetup_RoomCreatedAtTokenShiftsTimestamp(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-5-1-room-createdat")
	authURL := fakeAuthForSandbox(t, "test-jwt-token")

	s := &scenario.Scenario{
		Name: "phase4.5.1-room-createdat",
		Seed: scenario.SeedBlock{
			Users: map[string]scenario.SeedUserFlags{"alice": {"verified": true}},
			Rooms: []scenario.SeedRoom{
				{ID: "r-aged", Name: "Aged Room", Type: scenario.RoomTypeChannel, CreatedAt: "${now - 1h}"},
				{ID: "r-default", Name: "Default Room", Type: scenario.RoomTypeChannel}, // no override
			},
			Memberships: map[string][]scenario.SeedMembership{
				"alice": {{Room: "r-aged"}, {Room: "r-default"}},
			},
		},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Mongo:         db,
		AuthURL:       authURL,
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})
	require.NoError(t, sb.Setup(ctx))

	var aged map[string]any
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-aged"}).Decode(&aged))
	agedAt, ok := aged["createdAt"].(bson.DateTime)
	require.True(t, ok, "createdAt must decode as bson.DateTime, got %T", aged["createdAt"])
	want := sb.StartTime.UTC().Add(-time.Hour)
	diff := agedAt.Time().Sub(want)
	if diff < 0 {
		diff = -diff
	}
	assert.LessOrEqual(t, diff, 10*time.Millisecond,
		"r-aged.createdAt must equal sb.StartTime - 1h (within 10ms tolerance)")

	var def map[string]any
	require.NoError(t, db.Collection("rooms").FindOne(ctx, bson.M{"_id": "r-default"}).Decode(&def))
	defAt, ok := def["createdAt"].(bson.DateTime)
	require.True(t, ok)
	defDiff := defAt.Time().Sub(sb.StartTime.UTC())
	if defDiff < 0 {
		defDiff = -defDiff
	}
	assert.LessOrEqual(t, defDiff, 10*time.Millisecond,
		"default (empty CreatedAt) must fall back to sb.StartTime — backward-compat with pre-4.5.1 scenarios")
}

func TestSandboxSetup_RoomMembersCollectionDroppedAtSetup(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-room-members-drop")
	authURL := fakeAuthForSandbox(t, "test-jwt-token")

	_, err := db.Collection("room_members").InsertOne(ctx, bson.M{"_id": "rm-stale", "rid": "r-stale"})
	require.NoError(t, err)

	s := &scenario.Scenario{
		Name: "phase4-room-members-drop",
		Seed: scenario.SeedBlock{Users: map[string]scenario.SeedUserFlags{"alice": {"verified": true}}},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Mongo:         db,
		AuthURL:       authURL,
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})
	require.NoError(t, sb.Setup(ctx))

	stale := db.Collection("room_members").FindOne(ctx, bson.M{"_id": "rm-stale"})
	assert.Error(t, stale.Err(), "stale room_members doc must be dropped at Setup")
}

func TestSandboxSetup_ChaosResetCalled(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase3-sandbox-chaos-reset")

	s := &scenario.Scenario{Name: "sandbox-chaos-reset"}
	reg := seedeffect.NewRegistry()
	chaos := mishap.NewFakeChaosEngine()

	sb := NewSandbox(s, &SandboxDeps{Mongo: db, Chaos: chaos, SeedEffectReg: reg})
	require.NoError(t, sb.Setup(ctx))

	assert.Contains(t, chaos.Calls, "Reset()")
}
