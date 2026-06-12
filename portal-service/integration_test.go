//go:build integration

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/testutil"
)

func TestMain(m *testing.M) { testutil.RunTests(m) }

func seedDirectory(t *testing.T) *mongoDirectoryStore {
	t.Helper()
	db := testutil.MongoDB(t, "portal")
	store := newMongoDirectoryStore(db)
	ctx := context.Background()
	require.NoError(t, store.EnsureIndexes(ctx))

	_, err := db.Collection("users").InsertOne(ctx,
		bson.M{"_id": "u-alice", "account": "alice", "siteId": "site-a", "employeeId": "E001"})
	require.NoError(t, err)
	_, err = db.Collection("sites").InsertOne(ctx,
		bson.M{"_id": "site-a", "authServiceUrl": "https://auth.site-a.example.com", "natsUrl": "wss://nats.site-a.example.com"})
	require.NoError(t, err)
	return store
}

func TestMongoDirectoryStore_FindUserByAccount(t *testing.T) {
	store := seedDirectory(t)
	ctx := context.Background()

	u, err := store.FindUserByAccount(ctx, "alice")
	require.NoError(t, err)
	assert.Equal(t, "alice", u.Account)
	assert.Equal(t, "site-a", u.SiteID)
	assert.Equal(t, "E001", u.EmployeeID)

	_, err = store.FindUserByAccount(ctx, "nobody")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestMongoDirectoryStore_FindSiteByID(t *testing.T) {
	store := seedDirectory(t)
	ctx := context.Background()

	s, err := store.FindSiteByID(ctx, "site-a")
	require.NoError(t, err)
	assert.Equal(t, "site-a", s.ID)
	assert.Equal(t, "https://auth.site-a.example.com", s.AuthServiceURL)
	assert.Equal(t, "wss://nats.site-a.example.com", s.NATSURL)

	_, err = store.FindSiteByID(ctx, "site-z")
	assert.ErrorIs(t, err, ErrSiteNotFound)
}

func TestMongoDirectoryStore_EnsureIndexes(t *testing.T) {
	store := seedDirectory(t)
	ctx := context.Background()

	// Idempotent: a second call must not error.
	require.NoError(t, store.EnsureIndexes(ctx))

	// Uniqueness: a second document with the same account must be rejected.
	_, err := store.users.InsertOne(ctx,
		bson.M{"_id": "u-alice-dup", "account": "alice", "siteId": "site-b"})
	require.Error(t, err)
	assert.True(t, mongo.IsDuplicateKeyError(err))
}
