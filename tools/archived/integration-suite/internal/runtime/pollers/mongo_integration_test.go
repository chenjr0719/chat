//go:build integration

package pollers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/pkg/testutil"
)

// Phase 4.0: MongoPoller (table-bound) → MongoFindPoller (universal).
// The poller is now constructed with just (db, startTime); per-poll
// args (collection, filter) come from PollFn(args, tp).

func TestMongoFindPoller_ReturnsDocsCreatedAfterStart(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-mongo-poller")

	startTime := time.Now().UTC()

	_, err := db.Collection("rooms").InsertOne(ctx, bson.M{
		"_id":       "r-new-1",
		"name":      "Engineering",
		"createdAt": time.Now().UTC(),
	})
	require.NoError(t, err)
	_, err = db.Collection("rooms").InsertOne(ctx, bson.M{
		"_id":       "r-new-2",
		"name":      "Design",
		"createdAt": time.Now().UTC(),
	})
	require.NoError(t, err)

	p := NewMongoFindPoller(db, startTime)
	args := map[string]any{"collection": "rooms"}
	events := p.PollFn(args, "")()

	assert.Len(t, events, 2)
	assert.Equal(t, "mongo_find", events[0].Location)
	assert.Equal(t, "room-worker", events[0].OwnerSvc, "owner hint for known collection")
	payloads := []string{}
	for _, ev := range events {
		doc, ok := ev.Payload.(map[string]any)
		require.True(t, ok)
		name, _ := doc["name"].(string)
		payloads = append(payloads, name)
	}
	assert.ElementsMatch(t, []string{"Engineering", "Design"}, payloads)
}

func TestMongoFindPoller_FiltersDocsCreatedBeforeStart(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-mongo-poller-filter")

	staleTime := time.Now().UTC().Add(-time.Hour)
	_, err := db.Collection("rooms").InsertOne(ctx, bson.M{
		"_id":       "r-stale",
		"name":      "Old Room",
		"createdAt": staleTime,
	})
	require.NoError(t, err)

	startTime := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)

	_, err = db.Collection("rooms").InsertOne(ctx, bson.M{
		"_id":       "r-fresh",
		"name":      "New Room",
		"createdAt": time.Now().UTC(),
	})
	require.NoError(t, err)

	p := NewMongoFindPoller(db, startTime)
	events := p.PollFn(map[string]any{"collection": "rooms"}, "")()

	require.Len(t, events, 1)
	doc := events[0].Payload.(map[string]any)
	assert.Equal(t, "New Room", doc["name"])
}

func TestMongoFindPoller_AuthorFilterMergedWithStartTimeGuard(t *testing.T) {
	// Phase 4.0: a YAML-supplied args.filter combines with the
	// startTime guard (AND-merge). Different room IDs must be
	// distinguishable via the filter clause.
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-mongo-poller-filter-merge")

	now := time.Now().UTC()
	for _, name := range []string{"Engineering", "Design"} {
		_, err := db.Collection("rooms").InsertOne(ctx, bson.M{
			"_id":       "r-" + name,
			"name":      name,
			"createdAt": now,
		})
		require.NoError(t, err)
	}

	p := NewMongoFindPoller(db, now.Add(-time.Minute))
	args := map[string]any{
		"collection": "rooms",
		"filter":     map[string]any{"name": "Engineering"},
	}
	events := p.PollFn(args, "")()

	require.Len(t, events, 1, "args.filter narrowed to Engineering")
	assert.Equal(t, "Engineering", events[0].Payload.(map[string]any)["name"])
}

func TestMongoFindPoller_EmptyCollectionReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "phase4-mongo-poller-empty")

	require.NoError(t, db.Collection("rooms").Drop(ctx))

	p := NewMongoFindPoller(db, time.Now().UTC())
	events := p.PollFn(map[string]any{"collection": "rooms"}, "")()

	assert.Empty(t, events)
}

func TestMongoFindPoller_MissingCollectionArgReturnsNil(t *testing.T) {
	// Phase 4.0: missing required arg surfaces as slog warn + nil
	// (assertion times out cleanly; never panics).
	db := testutil.MongoDB(t, "phase4-mongo-poller-no-arg")
	p := NewMongoFindPoller(db, time.Now().UTC())
	events := p.PollFn(map[string]any{}, "")()
	assert.Nil(t, events)
}
