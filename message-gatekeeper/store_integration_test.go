//go:build integration

package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/testutil"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

func TestMongoStore_GetRoomMeta_ReadsThroughL2(t *testing.T) {
	ctx := context.Background()
	t.Cleanup(func() { testutil.FlushValkey(t) })
	client := valkeyutil.WrapClusterClient(testutil.SharedValkeyCluster(t))
	db := testutil.MongoDB(t, "gk-meta")

	_, err := db.Collection("rooms").InsertOne(ctx, bson.M{
		"_id": "r1", "name": "general", "type": model.RoomTypeChannel,
		"siteId": "site-a", "userCount": 600,
	})
	require.NoError(t, err)

	store := NewMongoStore(db, client, time.Minute)

	got, err := store.GetRoomMeta(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 600, got.UserCount)
	assert.Equal(t, "general", got.Name)
	assert.Equal(t, "site-a", got.SiteID)
	assert.Equal(t, model.RoomTypeChannel, got.Type)

	// Served from L2 after the Mongo doc is gone.
	_, err = db.Collection("rooms").DeleteOne(ctx, bson.M{"_id": "r1"})
	require.NoError(t, err)
	again, err := store.GetRoomMeta(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 600, again.UserCount)
	assert.Equal(t, "general", again.Name)
	assert.Equal(t, "site-a", again.SiteID)
	assert.Equal(t, model.RoomTypeChannel, again.Type)
}
