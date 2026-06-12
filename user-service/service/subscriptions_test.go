package service

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/hmchangw/chat/pkg/errcode"
	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/user-service/models"
)

func TestListSubscriptions_Types(t *testing.T) {
	for _, typ := range []string{"current", "rooms", "apps"} {
		t.Run(typ, func(t *testing.T) {
			svc, subs, _, _, rooms, _ := newSvc(t)
			subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", typ, gomock.Any(), 1000).
				Return([]model.Subscription{{ID: "s1"}}, nil)
			rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
			resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: typ})
			require.NoError(t, err)
			assert.Equal(t, 1, resp.Total)
		})
	}
}

func TestListSubscriptions_BadType(t *testing.T) {
	for _, typ := range []string{"", "bogus"} {
		t.Run(typ, func(t *testing.T) {
			svc, _, _, _, _, _ := newSvc(t)
			_, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: typ})
			requireCode(t, err, errcode.CodeBadRequest)
		})
	}
}

func TestListSubscriptions_NegativeWithinDays(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	neg := -1
	_, err := svc.ListSubscriptions(ctx("alice", "site-a"),
		models.SubscriptionListRequest{Type: "rooms", UpdatedWithinDays: &neg})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestFilterFavorites(t *testing.T) {
	subs := []model.Subscription{
		{ID: "a", Favorite: true},
		{ID: "b", Favorite: false},
		{ID: "c", Favorite: true},
	}
	got := filterFavorites(subs)
	require.Len(t, got, 2)
	assert.Equal(t, "a", got[0].ID)
	assert.Equal(t, "c", got[1].ID)
}

func TestMoveSelfDMFront(t *testing.T) {
	subs := []model.Subscription{
		{ID: "a", RoomType: model.RoomTypeChannel, Name: "Eng"},
		{ID: "self", RoomType: model.RoomTypeDM, Name: "alice"},
		{ID: "b", RoomType: model.RoomTypeDM, Name: "bob"},
	}
	got := moveSelfDMFront(subs, "alice")
	require.Len(t, got, 3)
	assert.Equal(t, "self", got[0].ID)
	assert.Equal(t, "a", got[1].ID)
	assert.Equal(t, "b", got[2].ID)
}

func TestMoveSelfDMFront_NoSelf(t *testing.T) {
	subs := []model.Subscription{{ID: "a", RoomType: model.RoomTypeChannel}}
	got := moveSelfDMFront(subs, "alice")
	require.Equal(t, "a", got[0].ID)
}

func TestMoveSelfDMFront_Nil(t *testing.T) {
	got := moveSelfDMFront(nil, "alice")
	assert.Empty(t, got)
}

func TestMoveSelfDMFront_AlreadyFirst(t *testing.T) {
	subs := []model.Subscription{
		{ID: "self", RoomType: model.RoomTypeDM, Name: "alice"},
		{ID: "a", RoomType: model.RoomTypeChannel, Name: "Eng"},
		{ID: "b", RoomType: model.RoomTypeDM, Name: "bob"},
	}
	got := moveSelfDMFront(subs, "alice")
	require.Len(t, got, 3)
	assert.Equal(t, "self", got[0].ID)
	assert.Equal(t, "a", got[1].ID)
	assert.Equal(t, "b", got[2].ID)
}

func TestApplyRoomInfo_NestedRoom(t *testing.T) {
	seen := time.UnixMilli(100).UTC()
	lastMsg := int64(200)
	lastMention := int64(50)
	pk := "a2V5LWJhc2U2NA=="
	kv := 3
	sub := model.Subscription{Name: "helper.bot", SiteID: "site-a", RoomID: "r1", LastSeenAt: &seen}
	info := model.RoomInfo{
		RoomID: "r1", Found: true, SiteID: "site-a", Name: "Canonical",
		UserCount: 7, AppCount: 2, LastMsgAt: &lastMsg, LastMsgID: "m9",
		LastMentionAllAt: &lastMention, PrivateKey: &pk, KeyVersion: &kv,
	}
	applyRoomInfo(&sub, &info)
	assert.Equal(t, "helper.bot", sub.Name, "room canonical name must not overwrite the subscription name")
	require.NotNil(t, sub.Room)
	assert.Equal(t, "site-a", sub.Room.SiteID)
	assert.Equal(t, "Canonical", sub.Room.Name)
	assert.Equal(t, 7, sub.Room.UserCount)
	assert.Equal(t, 2, sub.Room.AppCount)
	assert.Equal(t, "m9", sub.Room.LastMsgID)
	require.NotNil(t, sub.Room.LastMsgAt)
	assert.Equal(t, time.UnixMilli(200).UTC(), *sub.Room.LastMsgAt)
	require.NotNil(t, sub.Room.LastMentionAllAt)
	assert.Equal(t, time.UnixMilli(50).UTC(), *sub.Room.LastMentionAllAt)
	require.NotNil(t, sub.Room.PrivateKey, "private key must be forwarded, not dropped")
	assert.Equal(t, pk, *sub.Room.PrivateKey)
	require.NotNil(t, sub.Room.KeyVersion)
	assert.Equal(t, 3, *sub.Room.KeyVersion)
	assert.True(t, sub.Alert, "lastMsg newer than lastSeen")
	assert.False(t, sub.HasMention, "mention older than lastSeen")
}

func TestApplyRoomInfo_NotFound_NoRoom(t *testing.T) {
	sub := model.Subscription{Name: "general", SiteID: "site-a", RoomID: "r1"}
	applyRoomInfo(&sub, &model.RoomInfo{RoomID: "r1", Found: false})
	assert.Nil(t, sub.Room)
	assert.Equal(t, "general", sub.Name)
}

func TestListSubscriptions_DegradedSite_BaselineRoom(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	lastMsg := time.UnixMilli(400).UTC()
	storeSubs := []model.Subscription{{
		ID: "s1", RoomID: "r1", SiteID: "site-a", Name: "general",
		RoomType: model.RoomTypeChannel, UserCount: 9, LastMsgAt: &lastMsg, LastMsgID: "m1",
	}}
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), 1000).Return(storeSubs, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).Return(nil, errors.New("down"))
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "current"})
	require.NoError(t, err)
	require.Len(t, resp.Subscriptions, 1)
	room := resp.Subscriptions[0].Room
	require.NotNil(t, room, "degraded site must still yield a baseline room object from the $lookup values")
	assert.Equal(t, "site-a", room.SiteID)
	assert.Equal(t, 9, room.UserCount)
	assert.Equal(t, "m1", room.LastMsgID)
	require.NotNil(t, room.LastMsgAt)
	assert.Equal(t, lastMsg, *room.LastMsgAt)
	assert.Nil(t, room.PrivateKey, "baseline has no key material")
}

func TestListSubscriptions_BotDM_AppDisplayName(t *testing.T) {
	svc, subs, _, apps, rooms, _ := newSvc(t)
	storeSubs := []model.Subscription{
		{ID: "a1", RoomID: "rb1", SiteID: "site-a", RoomType: model.RoomTypeBotDM, Name: "helper.bot"},
		{ID: "c1", RoomID: "rc1", SiteID: "site-a", RoomType: model.RoomTypeChannel, Name: "general"},
	}
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), 1000).Return(storeSubs, nil)
	apps.EXPECT().GetAppNamesByAssistants(gomock.Any(), []string{"helper.bot"}).
		Return(map[string]string{"helper.bot": "Helper App"}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.InAnyOrder([]string{"rb1", "rc1"})).
		Return([]model.RoomInfo{
			{RoomID: "rb1", Found: true, Name: "bot-room-canonical"},
			{RoomID: "rc1", Found: true, Name: "general"},
		}, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "current"})
	require.NoError(t, err)
	require.Len(t, resp.Subscriptions, 2)
	assert.Equal(t, "Helper App", resp.Subscriptions[0].Name, "botDM name must be replaced by the app display name")
	require.NotNil(t, resp.Subscriptions[0].Room)
	assert.Equal(t, "bot-room-canonical", resp.Subscriptions[0].Room.Name)
	assert.Equal(t, "general", resp.Subscriptions[1].Name, "channel name must stay the subscription name")
}

func TestListSubscriptions_BotDM_AppLookupDegrades(t *testing.T) {
	svc, subs, _, apps, rooms, _ := newSvc(t)
	storeSubs := []model.Subscription{
		{ID: "a1", RoomID: "rb1", SiteID: "site-a", RoomType: model.RoomTypeBotDM, Name: "helper.bot"},
	}
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "apps", gomock.Any(), 1000).Return(storeSubs, nil)
	apps.EXPECT().GetAppNamesByAssistants(gomock.Any(), []string{"helper.bot"}).
		Return(nil, errors.New("db down"))
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"rb1"}).Return(nil, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "apps"})
	require.NoError(t, err, "app-name lookup failure must degrade, not fail the request")
	require.Len(t, resp.Subscriptions, 1)
	assert.Equal(t, "helper.bot", resp.Subscriptions[0].Name, "degraded lookup keeps the bot account name")
}

func TestListSubscriptions_BotDM_NoAppMatch(t *testing.T) {
	svc, subs, _, apps, rooms, _ := newSvc(t)
	storeSubs := []model.Subscription{
		{ID: "a1", RoomID: "rb1", SiteID: "site-a", RoomType: model.RoomTypeBotDM, Name: "orphan.bot"},
	}
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "apps", gomock.Any(), 1000).Return(storeSubs, nil)
	apps.EXPECT().GetAppNamesByAssistants(gomock.Any(), []string{"orphan.bot"}).
		Return(map[string]string{}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"rb1"}).Return(nil, nil)
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "apps"})
	require.NoError(t, err)
	assert.Equal(t, "orphan.bot", resp.Subscriptions[0].Name, "unmatched bot keeps the account name")
}

func TestListSubscriptions_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), 1000).
		Return(nil, errors.New("db down"))
	_, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{Type: "current"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestListSubscriptions_Favorite(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	storeSubs := []model.Subscription{
		{ID: "ch1", RoomType: model.RoomTypeChannel, Name: "general", Favorite: false},
		{ID: "self", RoomType: model.RoomTypeDM, Name: "alice", Favorite: true},
		{ID: "ch2", RoomType: model.RoomTypeChannel, Name: "random", Favorite: true},
	}
	subs.EXPECT().AggregateSubscriptions(gomock.Any(), "alice", "current", gomock.Any(), 1000).
		Return(storeSubs, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.ListSubscriptions(ctx("alice", "site-a"), models.SubscriptionListRequest{
		Type:     "current",
		Favorite: ptrBool(true),
	})
	require.NoError(t, err)
	require.Len(t, resp.Subscriptions, 2)
	assert.Equal(t, "self", resp.Subscriptions[0].ID, "self-DM favorite must be first")
	assert.Equal(t, "ch2", resp.Subscriptions[1].ID)
}

func ptrBool(b bool) *bool { return &b }

func TestGetChannels_ExactlyOne(t *testing.T) {
	t.Run("both_empty", func(t *testing.T) {
		svc, _, _, _, _, _ := newSvc(t)
		_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{})
		requireCode(t, err, errcode.CodeBadRequest)
	})
	t.Run("both_set", func(t *testing.T) {
		svc, _, _, _, _, _ := newSvc(t)
		_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "x", AccountNames: []string{"y"}})
		requireCode(t, err, errcode.CodeBadRequest)
	})
}

func TestGetChannels_TooManyAccountNames(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	names := make([]string, maxAccountNames+1)
	for i := range names {
		names[i] = "u"
	}
	// No store expectation — the cap must reject before FindChannelsByMembers.
	_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{AccountNames: names})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestGetChannels_AccountNamesAtCap(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	names := make([]string, maxAccountNames)
	for i := range names {
		names[i] = "u"
	}
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", names, 1000).Return([]model.Subscription{{ID: "c1"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{AccountNames: names})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Total)
}

func TestGetChannels_ByMembersContain(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol"}, 1000).Return([]model.Subscription{{ID: "c1"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "carol"})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Total)
}

func TestGetChannels_ByAccountNames(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol", "dave"}, 1000).Return([]model.Subscription{{ID: "c1"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{AccountNames: []string{"carol", "dave"}})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Total)
}

func TestGetChannels_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol"}, 1000).Return(nil, errors.New("db down"))
	_, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "carol"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetDM_Empty(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: ""})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestGetDM_InvalidTarget(t *testing.T) {
	for _, target := range []string{"p_system", "helper.bot", "p_", ".bot", "p_.bot"} {
		t.Run(target, func(t *testing.T) {
			svc, _, _, _, _, _ := newSvc(t)
			_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: target})
			requireCode(t, err, errcode.CodeBadRequest)
			assert.True(t, errcode.HasReason(err, errcode.UserInvalidDMTarget))
		})
	}
}

func TestGetDM_NotFound(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").Return(nil, nil)
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	requireCode(t, err, errcode.CodeNotFound)
	assert.True(t, errcode.HasReason(err, errcode.UserSubscriptionNotFound))
}

func TestGetDM_OK(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").
		Return(&model.DMSubscription{
			Subscription: &model.Subscription{ID: "d1"},
			HRInfo:       &model.SubscriptionHRInfo{Account: "bob", Name: "bob", EngName: "Bob"},
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil, nil).AnyTimes()
	resp, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	require.NoError(t, err)
	assert.Equal(t, "d1", resp.Subscription.ID)
	assert.Equal(t, "Bob", resp.Subscription.HRInfo.EngName)
}

func TestGetDM_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").Return(nil, errors.New("db down"))
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetDM_NilEmbeddedSubscription(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").Return(
		&model.DMSubscription{Subscription: nil, HRInfo: &model.SubscriptionHRInfo{Account: "bob"}},
		nil,
	)
	_, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetDM_Enriched(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	someMillis := int64(500)
	subs.EXPECT().GetDMSubscription(gomock.Any(), "alice", "bob").
		Return(&model.DMSubscription{
			Subscription: &model.Subscription{ID: "d1", SiteID: "site-a", RoomID: "r1", Name: "bob"},
			HRInfo:       &model.SubscriptionHRInfo{Account: "bob", Name: "bob", EngName: "Bob"},
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Renamed", LastMsgAt: &someMillis}}, nil)
	resp, err := svc.GetDM(ctx("alice", "site-a"), models.GetDMRequest{AccountName: "bob"})
	require.NoError(t, err)
	assert.Equal(t, "bob", resp.Subscription.Name, "subscription name must survive enrichment")
	require.NotNil(t, resp.Subscription.Room, "enriched room must propagate through GetDM write-back")
	assert.Equal(t, "Renamed", resp.Subscription.Room.Name)
	require.NotNil(t, resp.Subscription.HRInfo, "HRInfo must survive the enrichment write-back")
	assert.Equal(t, "Bob", resp.Subscription.HRInfo.EngName)
}

func TestGetByRoomID_Empty(t *testing.T) {
	svc, _, _, _, _, _ := newSvc(t)
	_, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: ""})
	requireCode(t, err, errcode.CodeBadRequest)
}

func TestGetByRoomID_NotFound(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "r1").Return(nil, nil)
	resp, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Total)
	assert.Empty(t, resp.Subscriptions)
	assert.NotNil(t, resp.Subscriptions, "empty result must be a non-nil slice")
}

func TestGetByRoomID_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "r1").Return(nil, errors.New("db down"))
	_, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "r1"})
	requireCode(t, err, errcode.CodeInternal)
}

func TestGetByRoomID_OK_Enriched(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	someMillis := int64(500)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "r1").
		Return(&model.Subscription{ID: "s1", SiteID: "site-a", RoomID: "r1", Name: "Stale"}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, Name: "Renamed", LastMsgAt: &someMillis}}, nil)
	resp, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "r1"})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Total)
	require.Len(t, resp.Subscriptions, 1)
	assert.Equal(t, "s1", resp.Subscriptions[0].ID)
	assert.Equal(t, "Stale", resp.Subscriptions[0].Name, "subscription name must survive enrichment")
	require.NotNil(t, resp.Subscriptions[0].Room, "enriched room must propagate through the 1-elem slice")
	assert.Equal(t, "Renamed", resp.Subscriptions[0].Room.Name)
}

func TestGetChannels_Empty(t *testing.T) {
	for _, name := range []string{"nil_slice", "empty_slice"} {
		t.Run(name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			var returned []model.Subscription
			if name == "empty_slice" {
				returned = []model.Subscription{}
			}
			subs.EXPECT().FindChannelsByMembers(gomock.Any(), "alice", []string{"carol"}, 1000).Return(returned, nil)
			resp, err := svc.GetChannels(ctx("alice", "site-a"), models.GetChannelsRequest{MembersContain: "carol"})
			require.NoError(t, err)
			assert.Equal(t, 0, resp.Total)
		})
	}
}

func TestGetByRoomID_BotDM_AppDisplayName(t *testing.T) {
	svc, subs, _, apps, rooms, _ := newSvc(t)
	subs.EXPECT().GetSubscriptionByRoomID(gomock.Any(), "alice", "rb1").
		Return(&model.Subscription{ID: "a1", RoomID: "rb1", SiteID: "site-a", RoomType: model.RoomTypeBotDM, Name: "helper.bot"}, nil)
	apps.EXPECT().GetAppNamesByAssistants(gomock.Any(), []string{"helper.bot"}).
		Return(map[string]string{"helper.bot": "Helper App"}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"rb1"}).Return(nil, nil)
	resp, err := svc.GetByRoomID(ctx("alice", "site-a"), models.GetByRoomIDRequest{RoomID: "rb1"})
	require.NoError(t, err)
	require.Len(t, resp.Subscriptions, 1)
	assert.Equal(t, "Helper App", resp.Subscriptions[0].Name, "botDM via getByRoomID must also carry the app display name")
}

func TestCount_Total(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(7, nil)
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{})
	require.NoError(t, err)
	assert.Equal(t, 7, resp.Count)
}

func TestCount_StoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(0, errors.New("db down"))
	_, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{})
	requireCode(t, err, errcode.CodeInternal)
}

func TestCountUnread_Happy(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(2, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 2).
		Return([]model.Subscription{{RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, LastMsgAt: &newer}}, nil)
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 1, resp.Count)
}

func TestCountUnread_FallbackToTotal(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(5, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 5).
		Return([]model.Subscription{{RoomID: "r1", SiteID: "site-a"}}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.Any()).Return(nil, errors.New("down"))
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 5, resp.Count) // fell back to total
}

func TestCountUnread_GetActiveStoreError(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(3, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 3).Return(nil, errors.New("db down"))
	yes := true
	_, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	requireCode(t, err, errcode.CodeInternal)
}

func TestCountUnread_MultiSite(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(4, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 4).Return([]model.Subscription{
		{RoomID: "ra1", SiteID: "site-a", LastSeenAt: &seen},
		{RoomID: "ra2", SiteID: "site-a", LastSeenAt: &seen},
		{RoomID: "rb1", SiteID: "site-b", LastSeenAt: &seen},
		{RoomID: "rb2", SiteID: "site-b", LastSeenAt: &seen},
	}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.InAnyOrder([]string{"ra1", "ra2"})).
		Return([]model.RoomInfo{
			{RoomID: "ra1", Found: true, LastMsgAt: &newer}, // unread
			{RoomID: "ra2", Found: true, LastMsgAt: nil},    // read
		}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-b", gomock.InAnyOrder([]string{"rb1", "rb2"})).
		Return([]model.RoomInfo{
			{RoomID: "rb1", Found: true, LastMsgAt: &newer}, // unread
			{RoomID: "rb2", Found: true, LastMsgAt: nil},    // read
		}, nil)
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Count, "one unread on site-a and one on site-b must sum to 2")
}

func TestCountUnread_AllRead(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(300).UTC()
	older := int64(100) // older than seen → not unread
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(2, nil)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 2).Return([]model.Subscription{
		{RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{RoomID: "r2", SiteID: "site-a", LastSeenAt: &seen},
	}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", gomock.Any()).
		Return([]model.RoomInfo{
			{RoomID: "r1", Found: true, LastMsgAt: &older},
			{RoomID: "r2", Found: true, LastMsgAt: nil},
		}, nil)
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Count)
}

func TestCountUnread_EmptyActive(t *testing.T) {
	svc, subs, _, _, _, _ := newSvc(t)
	subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(0, nil)
	// Zero active subs must short-circuit before GetActiveSubscriptions (min(0,maxSubs)=0 → rejected $limit:0).
	yes := true
	resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: &yes})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.Count)
}

func TestCountUnread_DedupsRoomIDs(t *testing.T) {
	svc, subs, _, _, rooms, _ := newSvc(t)
	seen := time.UnixMilli(100).UTC()
	newer := int64(200)
	subs.EXPECT().GetActiveSubscriptions(gomock.Any(), "alice", 2).Return([]model.Subscription{
		{ID: "a", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen},
		{ID: "b", RoomID: "r1", SiteID: "site-a", LastSeenAt: &seen}, // same room
	}, nil)
	rooms.EXPECT().GetRoomsInfo(gomock.Any(), "site-a", []string{"r1"}).
		Return([]model.RoomInfo{{RoomID: "r1", Found: true, LastMsgAt: &newer}}, nil)
	resp, err := svc.countUnread(ctx("alice", "site-a"), "alice", 2)
	require.NoError(t, err)
	assert.Equal(t, 2, resp.Count) // both subs counted unread; RPC roomIDs deduped to ["r1"]
}

func TestCount_UnreadFalse(t *testing.T) {
	for _, name := range []string{"nil", "false"} {
		t.Run(name, func(t *testing.T) {
			svc, subs, _, _, _, _ := newSvc(t)
			subs.EXPECT().CountActiveSubscriptions(gomock.Any(), "alice").Return(9, nil)
			// No GetActiveSubscriptions expectation — short-circuit must fire before calling it.
			var unreadPtr *bool
			if name == "false" {
				f := false
				unreadPtr = &f
			}
			resp, err := svc.CountSubscriptions(ctx("alice", "site-a"), models.CountRequest{Unread: unreadPtr})
			require.NoError(t, err)
			assert.Equal(t, 9, resp.Count)
		})
	}
}
