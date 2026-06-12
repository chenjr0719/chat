//go:build integration

package mongorepo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestAggregateSubscriptions_Integration(t *testing.T) {
	r, db := newTestSubscriptionRepo(t)
	ctx := context.Background()

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -100)

	// Seed rooms for every local sub that must survive — deleted-filter drops local subs with no room doc.
	seed(t, db, "rooms",
		bson.M{"_id": "r-eng", "name": "Eng", "siteId": "site-a", "userCount": 5,
			"lastMsgId": "m-eng", "lastMsgAt": now, "lastMentionAllAt": now},
		// distinct room for the stale sub-old row (a user can't sub the same room twice)
		bson.M{"_id": "r-eng-old", "name": "EngOld", "siteId": "site-a", "userCount": 1, "lastMsgAt": old},
		bson.M{"_id": "r-dm", "name": "DM-bob", "siteId": "site-a", "userCount": 2,
			"lastMsgId": "m-dm", "lastMsgAt": now},
		// botDM rooms — production always pairs a room with a botDM; missing rooms cause the deleted-filter to drop those subs.
		bson.M{"_id": "r-bot", "name": "helper.bot", "siteId": "site-a", "userCount": 1},
		bson.M{"_id": "r-bot2", "name": "off.bot", "siteId": "site-a", "userCount": 1},
		bson.M{"_id": "r-del", "name": "Del-Old", "siteId": "site-a", "userCount": 3},
		// r-missing intentionally NOT seeded
		// cross-site room is not in the local rooms collection by design
	)

	seed(t, db, "subscriptions",
		// local channel (kept, enriched)
		bson.M{"_id": "sub-eng", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-eng",
			"name": "Eng", "roomType": "channel", "siteId": "site-a", "favorite": true, "updatedAt": now, "createdAt": now},
		// local dm (kept, enriched)
		bson.M{"_id": "sub-dm", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-dm",
			"name": "bob", "roomType": "dm", "siteId": "site-a", "updatedAt": now, "createdAt": now},
		// local subscribed botDM (kept for current/apps)
		bson.M{"_id": "sub-bot", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-bot",
			"name": "helper.bot", "roomType": "botDM", "siteId": "site-a", "isSubscribed": true, "updatedAt": now, "createdAt": now},
		// local unsubscribed botDM (excluded from apps/current)
		bson.M{"_id": "sub-bot-off", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-bot2",
			"name": "off.bot", "roomType": "botDM", "siteId": "site-a", "isSubscribed": false, "updatedAt": now},
		// local channel whose room is Del-prefixed (DROPPED)
		bson.M{"_id": "sub-del", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-del",
			"name": "Del-Old", "roomType": "channel", "siteId": "site-a", "updatedAt": now},
		// local channel whose room is missing (DROPPED)
		bson.M{"_id": "sub-missing", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-missing",
			"name": "Gone", "roomType": "channel", "siteId": "site-a", "updatedAt": now},
		// cross-site channel (KEPT even though no local room doc)
		bson.M{"_id": "sub-xsite", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-xsite",
			"name": "Remote", "roomType": "channel", "siteId": "site-b", "updatedAt": now},
		// stale-ROOM row: its OWN updatedAt is deliberately `now` to prove the window
		// keys on room.lastMsgAt (r-eng-old is 100d stale), NOT on the subscription doc.
		bson.M{"_id": "sub-old", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-eng-old",
			"name": "EngOld", "roomType": "channel", "siteId": "site-a", "updatedAt": now},
	)

	t.Run("rooms returns dm+channel, drops Del- and missing, keeps cross-site", func(t *testing.T) {
		subs, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.ID] = true
		}
		assert.True(t, got["sub-eng"], "local channel kept")
		assert.True(t, got["sub-dm"], "local dm kept")
		assert.True(t, got["sub-xsite"], "cross-site channel kept")
		assert.False(t, got["sub-del"], "Del- local room dropped")
		assert.False(t, got["sub-missing"], "missing local room dropped")
		assert.False(t, got["sub-bot"], "botDM excluded from rooms")
	})

	t.Run("local row enriched, cross-site empty", func(t *testing.T) {
		subs, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, 100)
		require.NoError(t, err)
		byID := map[string]int{}
		for i, sub := range subs {
			byID[sub.ID] = i
		}
		eng := subs[byID["sub-eng"]]
		assert.Equal(t, 5, eng.UserCount)
		assert.Equal(t, "m-eng", eng.LastMsgID)
		require.NotNil(t, eng.LastMsgAt)
		require.NotNil(t, eng.LastMentionAllAt, "$lookup baseline must carry lastMentionAllAt for degraded-path hasMention")
		xsite := subs[byID["sub-xsite"]]
		assert.Equal(t, 0, xsite.UserCount, "cross-site has no local enrichment")
		assert.Empty(t, xsite.LastMsgID)
	})

	t.Run("favorite sorts before non-favorite then by name", func(t *testing.T) {
		subs, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, 100)
		require.NoError(t, err)
		require.NotEmpty(t, subs)
		assert.Equal(t, "sub-eng", subs[0].ID, "favorite Eng sorts first")
	})

	t.Run("apps returns only subscribed botDMs", func(t *testing.T) {
		subs, err := r.AggregateSubscriptions(ctx, "alice", "apps", nil, 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.ID] = true
		}
		assert.True(t, got["sub-bot"], "subscribed botDM kept")
		assert.False(t, got["sub-bot-off"], "unsubscribed botDM excluded")
		assert.False(t, got["sub-eng"], "channels excluded from apps")
	})

	t.Run("current merges rooms+subscribed botDMs", func(t *testing.T) {
		subs, err := r.AggregateSubscriptions(ctx, "alice", "current", nil, 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.ID] = true
		}
		assert.True(t, got["sub-eng"], "channel in current")
		assert.True(t, got["sub-dm"], "dm in current")
		assert.True(t, got["sub-bot"], "subscribed botDM in current")
		assert.False(t, got["sub-bot-off"], "unsubscribed botDM excluded from current")
		assert.False(t, got["sub-del"], "Del- dropped in current")
		assert.False(t, got["sub-missing"], "missing dropped in current")
	})

	t.Run("rooms window drops stale-ROOM subs, keeps fresh + cross-site", func(t *testing.T) {
		within := 30
		subs, err := r.AggregateSubscriptions(ctx, "alice", "rooms", &within, 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.ID] = true
		}
		assert.False(t, got["sub-old"], "stale room (lastMsgAt 100d ago) excluded by 30-day window")
		assert.True(t, got["sub-eng"], "fresh room (lastMsgAt now) kept")
		assert.True(t, got["sub-xsite"], "cross-site sub kept regardless of window")
	})

	t.Run("current ignores withinDays — keeps stale rows", func(t *testing.T) {
		within := 30
		subs, err := r.AggregateSubscriptions(ctx, "alice", "current", &within, 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.ID] = true
		}
		assert.True(t, got["sub-old"], "current returns the full active set; updatedWithinDays is ignored")
	})

	t.Run("limit caps results", func(t *testing.T) {
		subs, err := r.AggregateSubscriptions(ctx, "alice", "rooms", nil, 1)
		require.NoError(t, err)
		assert.Len(t, subs, 1)
	})
}

func TestFindChannelsByMembers_Integration(t *testing.T) {
	r, db := newTestSubscriptionRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// r-1 createdAt == now, r-2 == now-1h; sort must use room.createdAt DESC, not subscription.createdAt.
	seed(t, db, "rooms",
		bson.M{"_id": "r-1", "name": "Team1", "siteId": "site-a", "userCount": 3, "createdAt": now},
		bson.M{"_id": "r-2", "name": "Team2", "siteId": "site-a", "userCount": 2, "createdAt": now.Add(-time.Hour)},
	)
	// All subscription createdAt values == now so only room.createdAt drives ordering.
	seed(t, db, "subscriptions",
		bson.M{"_id": "a1", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-1",
			"name": "Team1", "roomType": "channel", "siteId": "site-a", "createdAt": now},
		bson.M{"_id": "c1", "u": bson.M{"_id": "u-carol", "account": "carol"}, "roomId": "r-1",
			"name": "Team1", "roomType": "channel", "siteId": "site-a", "createdAt": now},
		bson.M{"_id": "d1", "u": bson.M{"_id": "u-dave", "account": "dave"}, "roomId": "r-1",
			"name": "Team1", "roomType": "channel", "siteId": "site-a", "createdAt": now},
		bson.M{"_id": "a2", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "r-2",
			"name": "Team2", "roomType": "channel", "siteId": "site-a", "createdAt": now},
		bson.M{"_id": "c2", "u": bson.M{"_id": "u-carol", "account": "carol"}, "roomId": "r-2",
			"name": "Team2", "roomType": "channel", "siteId": "site-a", "createdAt": now},
	)

	t.Run("single member matches both rooms", func(t *testing.T) {
		subs, err := r.FindChannelsByMembers(ctx, "alice", []string{"carol"}, 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.RoomID] = true
		}
		assert.True(t, got["r-1"])
		assert.True(t, got["r-2"])
	})

	t.Run("two members match only the room containing both", func(t *testing.T) {
		subs, err := r.FindChannelsByMembers(ctx, "alice", []string{"carol", "dave"}, 100)
		require.NoError(t, err)
		require.Len(t, subs, 1)
		assert.Equal(t, "r-1", subs[0].RoomID)
	})

	t.Run("sorted by room createdAt DESC", func(t *testing.T) {
		subs, err := r.FindChannelsByMembers(ctx, "alice", []string{"carol"}, 100)
		require.NoError(t, err)
		require.Len(t, subs, 2)
		// r-1's room.createdAt == now, r-2's room.createdAt == now-1h → r-1 first.
		assert.Equal(t, "r-1", subs[0].RoomID, "room with newer createdAt sorts first")
		assert.Equal(t, "r-2", subs[1].RoomID)
	})

	t.Run("limit caps results", func(t *testing.T) {
		// alice matches 2 rooms (r-1, r-2); a limit of 1 must cap to the first
		// (r-1, the room with the newer createdAt under the DESC sort).
		subs, err := r.FindChannelsByMembers(ctx, "alice", []string{"carol"}, 1)
		require.NoError(t, err)
		require.Len(t, subs, 1)
		assert.Equal(t, "r-1", subs[0].RoomID)
	})

	t.Run("field-path-shaped member is treated as a literal, not a path", func(t *testing.T) {
		// "$u.account" must be a literal (no match), not a field path that makes $setIsSubset trivially true.
		subs, err := r.FindChannelsByMembers(ctx, "alice", []string{"$u.account"}, 100)
		require.NoError(t, err)
		assert.Empty(t, subs, "$-prefixed member must not bypass the member filter")
	})
}

func TestGetDMSubscription_Integration(t *testing.T) {
	r, db := newTestSubscriptionRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()

	seed(t, db, "rooms",
		bson.M{"_id": "dm-bob", "name": "DM-bob", "siteId": "site-a", "userCount": 2, "lastMsgId": "m1", "lastMsgAt": now},
		bson.M{"_id": "dm-rem", "name": "DM-remote", "siteId": "site-a", "userCount": 2},
	)
	seed(t, db, "users",
		bson.M{"_id": "u-bob", "account": "bob", "active": true, "engName": "Bob", "chineseName": "鮑勃"},
	)
	seed(t, db, "subscriptions",
		bson.M{"_id": "dm-sub-bob", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "dm-bob",
			"name": "bob", "roomType": "dm", "siteId": "site-a"},
		// cross-site DM counterpart whose room is local but user is remote (no local users doc)
		bson.M{"_id": "dm-sub-rem", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "dm-rem",
			"name": "remoteguy", "roomType": "dm", "siteId": "site-a"},
	)

	t.Run("local counterpart populates HRInfo", func(t *testing.T) {
		dm, err := r.GetDMSubscription(ctx, "alice", "bob")
		require.NoError(t, err)
		require.NotNil(t, dm)
		require.NotNil(t, dm.Subscription)
		require.NotNil(t, dm.HRInfo)
		assert.Equal(t, "bob", dm.HRInfo.Account)
		assert.Equal(t, "鮑勃", dm.HRInfo.Name)
		assert.Equal(t, "Bob", dm.HRInfo.EngName)
		assert.Equal(t, 2, dm.UserCount, "room enrichment applied")
	})

	t.Run("cross-site counterpart yields nil HRInfo", func(t *testing.T) {
		dm, err := r.GetDMSubscription(ctx, "alice", "remoteguy")
		require.NoError(t, err)
		require.NotNil(t, dm)
		assert.Nil(t, dm.HRInfo, "no local users doc → HRInfo nil")
	})

	t.Run("miss yields nil", func(t *testing.T) {
		dm, err := r.GetDMSubscription(ctx, "alice", "nobody")
		require.NoError(t, err)
		assert.Nil(t, dm)
	})
}

func TestGetSubscriptionByRoomID_Integration(t *testing.T) {
	r, db := newTestSubscriptionRepo(t)
	ctx := context.Background()

	seed(t, db, "rooms",
		bson.M{"_id": "ch1", "name": "General", "siteId": "site-a", "userCount": 5, "lastMsgId": "m9"},
		bson.M{"_id": "del1", "name": "Del-Old", "siteId": "site-a", "userCount": 2}, // soft-deleted
	)
	seed(t, db, "subscriptions",
		bson.M{"_id": "sub-ch1", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "ch1",
			"name": "General", "roomType": "channel", "siteId": "site-a"},
		bson.M{"_id": "sub-del", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "del1",
			"name": "Old", "roomType": "channel", "siteId": "site-a"},
		// cross-site sub: no local room doc, must be kept by the deleted-filter.
		bson.M{"_id": "sub-x", "u": bson.M{"_id": "u-alice", "account": "alice"}, "roomId": "rx",
			"name": "Remote", "roomType": "channel", "siteId": "site-b"},
	)

	t.Run("local hit is room-enriched", func(t *testing.T) {
		sub, err := r.GetSubscriptionByRoomID(ctx, "alice", "ch1")
		require.NoError(t, err)
		require.NotNil(t, sub)
		assert.Equal(t, "sub-ch1", sub.ID)
		assert.Equal(t, 5, sub.UserCount, "room enrichment applied")
	})

	t.Run("cross-site sub kept despite no local room", func(t *testing.T) {
		sub, err := r.GetSubscriptionByRoomID(ctx, "alice", "rx")
		require.NoError(t, err)
		require.NotNil(t, sub)
		assert.Equal(t, "sub-x", sub.ID)
	})

	t.Run("soft-deleted local room yields nil", func(t *testing.T) {
		sub, err := r.GetSubscriptionByRoomID(ctx, "alice", "del1")
		require.NoError(t, err)
		assert.Nil(t, sub, "local sub to a ^Del- room must be filtered out")
	})

	t.Run("not subscribed yields nil", func(t *testing.T) {
		sub, err := r.GetSubscriptionByRoomID(ctx, "alice", "nope")
		require.NoError(t, err)
		assert.Nil(t, sub)
	})

	t.Run("other account yields nil", func(t *testing.T) {
		sub, err := r.GetSubscriptionByRoomID(ctx, "bob", "ch1")
		require.NoError(t, err)
		assert.Nil(t, sub)
	})
}

func TestCountAndGetActiveSubscriptions_Integration(t *testing.T) {
	r, db := newTestSubscriptionRepo(t)
	ctx := context.Background()

	seed(t, db, "rooms",
		bson.M{"_id": "r-dm", "name": "Bob DM", "siteId": "site-a"},
		bson.M{"_id": "r-ch", "name": "Eng", "siteId": "site-a"},
		bson.M{"_id": "r-noisy", "name": "Noisy", "siteId": "site-a"},
		bson.M{"_id": "r-bot", "name": "helper.bot", "siteId": "site-a"},
		bson.M{"_id": "r-del", "name": "Del-Gone", "siteId": "site-a"}, // soft-deleted
	)
	seed(t, db, "subscriptions",
		// active dm
		bson.M{"_id": "a-dm", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "bob", "roomId": "r-dm",
			"roomType": "dm", "siteId": "site-a"},
		// active channel
		bson.M{"_id": "a-ch", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "Eng", "roomId": "r-ch",
			"roomType": "channel", "siteId": "site-a"},
		// muted channel (excluded)
		bson.M{"_id": "m-ch", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "Noisy", "roomId": "r-noisy",
			"roomType": "channel", "siteId": "site-a", "muted": true},
		// subscribed botDM (included)
		bson.M{"_id": "a-bot", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "helper.bot", "roomId": "r-bot",
			"roomType": "botDM", "siteId": "site-a", "isSubscribed": true},
		// unsubscribed botDM (excluded)
		bson.M{"_id": "u-bot", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "off.bot", "roomId": "r-offbot",
			"roomType": "botDM", "siteId": "site-a", "isSubscribed": false},
		// muted subscribed botDM (excluded)
		bson.M{"_id": "mu-bot", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "muted.bot", "roomId": "r-mutedbot",
			"roomType": "botDM", "siteId": "site-a", "isSubscribed": true, "muted": true},
		// active by type, but local room is soft-deleted (^Del-) — excluded by room filter
		bson.M{"_id": "del-ch", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "Gone", "roomId": "r-del",
			"roomType": "channel", "siteId": "site-a"},
		// active by type, but local room is missing — excluded by room filter
		bson.M{"_id": "gone-ch", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "Vanished", "roomId": "r-missing",
			"roomType": "channel", "siteId": "site-a"},
		// cross-site sub: no local room doc, kept by the room filter
		bson.M{"_id": "x-ch", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "Remote", "roomId": "rx",
			"roomType": "channel", "siteId": "site-b"},
	)

	t.Run("count excludes muted, unsubscribed, and deleted/missing local rooms", func(t *testing.T) {
		n, err := r.CountActiveSubscriptions(ctx, "alice")
		require.NoError(t, err)
		assert.Equal(t, 4, n) // a-dm, a-ch, a-bot, x-ch
	})

	t.Run("get active returns the same set", func(t *testing.T) {
		subs, err := r.GetActiveSubscriptions(ctx, "alice", 100)
		require.NoError(t, err)
		got := map[string]bool{}
		for _, sub := range subs {
			got[sub.ID] = true
		}
		assert.True(t, got["a-dm"])
		assert.True(t, got["a-ch"])
		assert.True(t, got["a-bot"])
		assert.True(t, got["x-ch"], "cross-site sub kept despite no local room")
		assert.False(t, got["m-ch"])
		assert.False(t, got["u-bot"])
		assert.False(t, got["mu-bot"])
		assert.False(t, got["del-ch"], "local sub to a ^Del- room must be filtered out")
		assert.False(t, got["gone-ch"], "local sub to a missing room must be filtered out")
	})

	t.Run("limit caps active set", func(t *testing.T) {
		subs, err := r.GetActiveSubscriptions(ctx, "alice", 2)
		require.NoError(t, err)
		assert.Len(t, subs, 2)
	})

	t.Run("zero limit does not error (no $limit:0 stage)", func(t *testing.T) {
		// $limit:0 is rejected by MongoDB; the guard must drop the stage so the query returns the uncapped set.
		subs, err := r.GetActiveSubscriptions(ctx, "alice", 0)
		require.NoError(t, err)
		assert.NotEmpty(t, subs)
	})
}

// TestCountUnread_ZeroActive_Integration: no active subs yields count=0 and an empty (non-erroring) active set.
func TestCountUnread_ZeroActive_Integration(t *testing.T) {
	r, _ := newTestSubscriptionRepo(t)
	ctx := context.Background()

	n, err := r.CountActiveSubscriptions(ctx, "nobody")
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	subs, err := r.GetActiveSubscriptions(ctx, "nobody", 0)
	require.NoError(t, err)
	assert.Empty(t, subs)
}

func TestAppSubscriptionRoundTrip_Integration(t *testing.T) {
	r, db := newTestSubscriptionRepo(t)
	ctx := context.Background()

	seed(t, db, "subscriptions",
		bson.M{"_id": "bot-sub", "u": bson.M{"_id": "u-alice", "account": "alice"}, "name": "helper.bot",
			"roomType": "botDM", "siteId": "site-a", "isSubscribed": false, "muted": false},
	)

	t.Run("get existing", func(t *testing.T) {
		sub, err := r.GetAppSubscription(ctx, "alice", "helper.bot")
		require.NoError(t, err)
		require.NotNil(t, sub)
		assert.False(t, sub.IsSubscribed)
	})

	t.Run("get miss", func(t *testing.T) {
		sub, err := r.GetAppSubscription(ctx, "alice", "ghost.bot")
		require.NoError(t, err)
		assert.Nil(t, sub)
	})

	t.Run("set then re-read", func(t *testing.T) {
		require.NoError(t, r.SetAppSubscribed(ctx, "alice", "helper.bot", true, true))
		sub, err := r.GetAppSubscription(ctx, "alice", "helper.bot")
		require.NoError(t, err)
		require.NotNil(t, sub)
		assert.True(t, sub.IsSubscribed)
		assert.True(t, sub.Muted)
	})
}
