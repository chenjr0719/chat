package roommetacache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// MetaKey is the L2 (Valkey) key for a room's cached Meta. The {roomID}
// hash tag colocates it in the same cluster slot as the room's encryption
// key (pkg/roomkeystore), matching house convention for room-scoped keys.
func MetaKey(roomID string) string {
	return "room:{" + roomID + "}:meta"
}

// ReadThrough resolves a room Meta through the L2 (Valkey) tier: GET on the
// cache key, and on miss (or any L2 error) fall back to Mongo and repopulate
// L2 with the given TTL. It is fail-open — a nil client or any Valkey error
// degrades to a direct Mongo read; only the Mongo result governs the returned
// error. Intended to be the terminal loader behind the L1 roommetacache.Cache.
func ReadThrough(ctx context.Context, client valkeyutil.Client, rooms *mongo.Collection, roomID string, ttl time.Duration) (Meta, error) {
	if client == nil {
		return FetchFromMongo(ctx, rooms, roomID)
	}

	key := MetaKey(roomID)
	var cached Meta
	err := valkeyutil.GetJSON(ctx, client, key, &cached)
	if err == nil {
		return cached, nil
	}
	if !errors.Is(err, valkeyutil.ErrCacheMiss) {
		slog.WarnContext(ctx, "room meta L2 read failed, falling back to mongo",
			"room_id", roomID, "error", err)
	}

	meta, err := FetchFromMongo(ctx, rooms, roomID)
	if err != nil {
		return Meta{}, fmt.Errorf("l2 read-through: %w", err)
	}
	if err := valkeyutil.SetJSONWithTTL(ctx, client, key, meta, ttl); err != nil {
		slog.WarnContext(ctx, "room meta L2 populate failed (TTL will reconcile)",
			"room_id", roomID, "error", err)
	}
	return meta, nil
}

// BustMeta best-effort deletes a room's L2 Meta entry. Called from the write
// site (room-worker) after an authoritative Mongo write to name/userCount.
// Fail-open: a nil client is a no-op and any Valkey error logs at warn and is
// swallowed — the configured L2 TTL reconciles a missed bust.
func BustMeta(ctx context.Context, client valkeyutil.Client, roomID string) {
	if client == nil {
		return
	}
	if err := client.Del(ctx, MetaKey(roomID)); err != nil {
		slog.WarnContext(ctx, "room meta L2 invalidate failed (TTL will reconcile)",
			"room_id", roomID, "error", err)
	}
}
