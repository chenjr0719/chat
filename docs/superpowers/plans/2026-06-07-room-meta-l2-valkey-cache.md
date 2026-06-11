# Room Metadata L2 (Valkey) Cache + Write-Site Invalidation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a shared L2 (Valkey) read-through tier beneath the existing L1 in-process `roommetacache` so room metadata (Name, UserCount) reads survive process restarts and are shared across replicas, and add best-effort write-site invalidation in room-worker so renames and member-count changes evict the L2 entry promptly.

**Architecture:** A two-tier cache for the `roommetacache.Meta` projection. **L1** (already exists: `pkg/roommetacache.Cache`, hashicorp LRU+TTL+singleflight, per-process, default 2m TTL) is unchanged. **L2** (new) is a Valkey string key `room:{roomID}:meta` holding the JSON-encoded `Meta`, with a 15m absolute TTL. Readers (broadcast-worker, message-gatekeeper, notification-worker) replace their L1 loader's terminal Mongo fetch with a new `roommetacache.ReadThrough` that does `GET L2 → on miss, Mongo + SET L2`. The write-site (room-worker) calls `roommetacache.BustMeta` (an L2 `DEL`) immediately after each authoritative Mongo write to `name` / `userCount`. Both read and bust are **fail-open**: any Valkey error logs at warn and degrades to the TTL/Mongo, never blocking a request.

**Accepted residual staleness (TTL-backstopped, by design):**
- The write-site bust clears **L2 only**. A reader's **L1** can still serve a stale `Meta` for up to its L1 TTL (default 2m) after a rename/count change. Bounded, acceptable.
- A reader whose Mongo fetch straddles a concurrent write can re-`SET` a stale value into L2 just after the `DEL` (repopulate race). Bounded by the 15m L2 TTL. Not worth versioning to eliminate.
- The bust fires *after* Mongo commits, leaving a sub-millisecond window where L2 holds the old value. Same TTL backstop.

**Tech Stack:** Go 1.25, `pkg/valkeyutil` (go-redis/v9 cluster wrapper), `pkg/roommetacache` (hashicorp/golang-lru/v2/expirable), MongoDB driver v2, `pkg/testutil` (testcontainers Valkey + Mongo), testify, `go.uber.org/mock`.

**Docs note — no `docs/client-api.md` change required:** this is an internal caching change. No client-facing request/response schema, error case, or triggered event changes. The large-room post restriction (message-gatekeeper) and rename behavior (room-worker) are functionally identical; only the *source* of the `Meta` (cache vs Mongo) changes.

---

## File Structure

**New files:**
- `pkg/roommetacache/valkey.go` — `MetaKey`, `ReadThrough`, `BustMeta` (the only genuinely new logic; everything else is wiring).
- `pkg/roommetacache/valkey_test.go` — unit tests (fake `valkeyutil.Client`, no containers).
- `pkg/roommetacache/integration_test.go` — integration tests (real Valkey + Mongo) + this package's `TestMain`.
- `broadcast-worker/metacache_integration_test.go` — store read-through integration test (no `TestMain`; package already has one in `main_test.go`).
- `message-gatekeeper/store_integration_test.go` — store read-through integration test (no `TestMain`).

**Modified files:**
- `pkg/roommetacache/roommetacache.go` — add `json` tags to `Meta`; update package doc comment (active L2 invalidation now exists).
- `broadcast-worker/store_mongo.go` + `broadcast-worker/main.go` — inject optional `valkeyutil.Client` + L2 TTL; `GetRoomMeta` → `ReadThrough`.
- `message-gatekeeper/store_mongo.go` + `message-gatekeeper/main.go` — same shape.
- `notification-worker/main.go` — swap the L1 loader's `FetchFromMongo` for `ReadThrough`; reorder Valkey client construction.
- `room-worker/handler.go` + `room-worker/main.go` — add `valkey` field + `bustRoomMeta` method + 5 invalidation call sites; construct a `valkeyutil.Client` (reusing existing `VALKEY_ADDRS`) and disconnect on shutdown.
- `room-worker/handler_test.go` — unit tests for `bustRoomMeta`.
- `broadcast-worker/deploy/docker-compose.yml`, `message-gatekeeper/deploy/docker-compose.yml` — add `VALKEY_ADDRS` / `ROOM_META_L2_TTL` for local dev.

**No-cycle check:** `roommetacache` importing `valkeyutil` is safe — `valkeyutil` imports only go-redis/stdlib, never `roommetacache`.

---

## Task 1: roommetacache L2 primitives (`MetaKey`, `ReadThrough`, `BustMeta`)

**Files:**
- Modify: `pkg/roommetacache/roommetacache.go` (add `json` tags to `Meta`, lines 33-39; update package doc, lines 9-12)
- Create: `pkg/roommetacache/valkey.go`
- Test: `pkg/roommetacache/valkey_test.go`

- [ ] **Step 1: Write the failing unit tests**

Create `pkg/roommetacache/valkey_test.go`:

```go
package roommetacache

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/model"
	"github.com/hmchangw/chat/pkg/valkeyutil"
)

// fakeValkey is an in-memory valkeyutil.Client for unit tests.
type fakeValkey struct {
	mu     sync.Mutex
	data   map[string]string
	dels   []string
	getErr error
	setErr error
	delErr error
}

func newFakeValkey() *fakeValkey { return &fakeValkey{data: map[string]string{}} }

func (f *fakeValkey) Get(_ context.Context, key string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return "", f.getErr
	}
	v, ok := f.data[key]
	if !ok {
		return "", valkeyutil.ErrCacheMiss
	}
	return v, nil
}

func (f *fakeValkey) Set(_ context.Context, key, value string, _ time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.data[key] = value
	return nil
}

func (f *fakeValkey) Del(_ context.Context, keys ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dels = append(f.dels, keys...)
	if f.delErr != nil {
		return f.delErr
	}
	for _, k := range keys {
		delete(f.data, k)
	}
	return nil
}

func (f *fakeValkey) Close() error { return nil }

func TestMetaKey(t *testing.T) {
	assert.Equal(t, "room:{r123}:meta", MetaKey("r123"))
}

func TestReadThrough_L2Hit(t *testing.T) {
	fake := newFakeValkey()
	want := Meta{ID: "r1", Type: model.RoomTypeChannel, Name: "general", SiteID: "site-a", UserCount: 7}
	raw, err := json.Marshal(want)
	require.NoError(t, err)
	fake.data[MetaKey("r1")] = string(raw)

	// nil *mongo.Collection is safe: on an L2 hit, Mongo is never touched.
	got, err := ReadThrough(context.Background(), fake, nil, "r1", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestBustMeta_CallsDel(t *testing.T) {
	fake := newFakeValkey()
	fake.data[MetaKey("r1")] = "{}"
	BustMeta(context.Background(), fake, "r1")
	assert.Equal(t, []string{MetaKey("r1")}, fake.dels)
	_, present := fake.data[MetaKey("r1")]
	assert.False(t, present)
}

func TestBustMeta_NilClient_NoPanic(t *testing.T) {
	assert.NotPanics(t, func() { BustMeta(context.Background(), nil, "r1") })
}

func TestBustMeta_FailOpen(t *testing.T) {
	fake := newFakeValkey()
	fake.delErr = errors.New("valkey down")
	// Must not panic and must not propagate — best-effort.
	assert.NotPanics(t, func() { BustMeta(context.Background(), fake, "r1") })
	assert.Equal(t, []string{MetaKey("r1")}, fake.dels)
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `make test SERVICE=pkg/roommetacache`
Expected: FAIL — `undefined: MetaKey`, `undefined: ReadThrough`, `undefined: BustMeta`.

- [ ] **Step 3: Add `json` tags to `Meta` and update the package doc**

In `pkg/roommetacache/roommetacache.go`, replace the `Meta` struct (lines 33-39):

```go
// Meta is the cached projection of a room document. Both consumers
// (gatekeeper and broadcast-worker) use these four fields and nothing
// else from the room. The json tags pin the L2 (Valkey) wire format.
type Meta struct {
	ID        string         `json:"id"`
	Type      model.RoomType `json:"type"`
	Name      string         `json:"name"`
	SiteID    string         `json:"siteId"`
	UserCount int            `json:"userCount"`
}
```

In the same file, replace the package doc paragraph (lines 9-12):

```go
// Freshness is TTL-bounded at L1. An L2 (Valkey) tier (see valkey.go:
// ReadThrough / BustMeta) is shared across replicas and survives restarts;
// room-worker actively busts the L2 entry on writes to name/userCount. The
// L1 LRU remains TTL-only — Invalidate exists but the cross-process bust is
// L2-scoped, so L1 staleness is bounded by ROOM_META_CACHE_TTL. See the spec
// at docs/superpowers/specs/2026-05-18-message-pipeline-mongo-caching-design.md.
```

- [ ] **Step 4: Create the implementation**

Create `pkg/roommetacache/valkey.go`:

```go
package roommetacache

import (
	"context"
	"errors"
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
	if err := valkeyutil.GetJSON(ctx, client, key, &cached); err == nil {
		return cached, nil
	} else if !errors.Is(err, valkeyutil.ErrCacheMiss) {
		slog.WarnContext(ctx, "room meta L2 read failed, falling back to mongo",
			"room_id", roomID, "error", err)
	}

	meta, err := FetchFromMongo(ctx, rooms, roomID)
	if err != nil {
		return Meta{}, err
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
// swallowed — the 15m L2 TTL reconciles a missed bust.
func BustMeta(ctx context.Context, client valkeyutil.Client, roomID string) {
	if client == nil {
		return
	}
	if err := client.Del(ctx, MetaKey(roomID)); err != nil {
		slog.WarnContext(ctx, "room meta L2 invalidate failed (TTL will reconcile)",
			"room_id", roomID, "error", err)
	}
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `make test SERVICE=pkg/roommetacache`
Expected: PASS (all 6 unit tests).

- [ ] **Step 6: Lint**

Run: `make lint`
Expected: no findings in `pkg/roommetacache`.

- [ ] **Step 7: Commit**

```bash
git add pkg/roommetacache/valkey.go pkg/roommetacache/valkey_test.go pkg/roommetacache/roommetacache.go
git commit -m "feat(roommetacache): add L2 Valkey read-through + bust primitives

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

---

## Task 2: roommetacache L2 integration tests (real Valkey + Mongo)

**Files:**
- Create: `pkg/roommetacache/integration_test.go`

- [ ] **Step 1: Write the failing integration tests**

Create `pkg/roommetacache/integration_test.go`:

```go
//go:build integration

package roommetacache

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

func TestMain(m *testing.M) { testutil.RunTests(m) }

func setupValkey(t *testing.T) valkeyutil.Client {
	t.Helper()
	t.Cleanup(func() { testutil.FlushValkey(t) })
	return valkeyutil.WrapClusterClient(testutil.SharedValkeyCluster(t))
}

func TestReadThrough_MissPopulatesThenServesFromL2(t *testing.T) {
	ctx := context.Background()
	client := setupValkey(t)
	db := testutil.MongoDB(t, "roommetacache")
	rooms := db.Collection("rooms")

	_, err := rooms.InsertOne(ctx, bson.M{
		"_id": "r1", "name": "general", "type": model.RoomTypeChannel,
		"siteId": "site-a", "userCount": 3,
	})
	require.NoError(t, err)

	// First read: L2 miss → Mongo → populate L2.
	got, err := ReadThrough(ctx, client, rooms, "r1", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "general", got.Name)
	assert.Equal(t, 3, got.UserCount)

	// Prove L2 now serves it: delete the Mongo doc, second read still hits.
	_, err = rooms.DeleteOne(ctx, bson.M{"_id": "r1"})
	require.NoError(t, err)

	again, err := ReadThrough(ctx, client, rooms, "r1", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "general", again.Name)
	assert.Equal(t, 3, again.UserCount)
}

func TestReadThrough_NilClientReadsMongo(t *testing.T) {
	ctx := context.Background()
	db := testutil.MongoDB(t, "roommetacache")
	rooms := db.Collection("rooms")
	_, err := rooms.InsertOne(ctx, bson.M{
		"_id": "r2", "name": "ops", "type": model.RoomTypeChannel,
		"siteId": "site-a", "userCount": 1,
	})
	require.NoError(t, err)

	got, err := ReadThrough(ctx, nil, rooms, "r2", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "ops", got.Name)
}

func TestBustMeta_RemovesL2Entry(t *testing.T) {
	ctx := context.Background()
	client := setupValkey(t)
	db := testutil.MongoDB(t, "roommetacache")
	rooms := db.Collection("rooms")

	_, err := rooms.InsertOne(ctx, bson.M{
		"_id": "r3", "name": "first", "type": model.RoomTypeChannel,
		"siteId": "site-a", "userCount": 2,
	})
	require.NoError(t, err)

	// Populate L2.
	_, err = ReadThrough(ctx, client, rooms, "r3", time.Minute)
	require.NoError(t, err)

	// Authoritative write + bust.
	_, err = rooms.UpdateOne(ctx, bson.M{"_id": "r3"}, bson.M{"$set": bson.M{"name": "second"}})
	require.NoError(t, err)
	BustMeta(ctx, client, "r3")

	// Next read repopulates from Mongo with the fresh value.
	got, err := ReadThrough(ctx, client, rooms, "r3", time.Minute)
	require.NoError(t, err)
	assert.Equal(t, "second", got.Name)
}
```

- [ ] **Step 2: Run to verify it fails (compiles, behavior asserted)**

Run: `make test-integration SERVICE=pkg/roommetacache`
Expected: PASS (the implementation from Task 1 already satisfies these). If anything FAILS, fix `valkey.go` before continuing. (This task is the integration safety net for Task 1's primitives.)

- [ ] **Step 3: Commit**

```bash
git add pkg/roommetacache/integration_test.go
git commit -m "test(roommetacache): integration coverage for L2 read-through + bust

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

---

## Task 3: broadcast-worker — L2 read-through under the existing L1

**Files:**
- Modify: `broadcast-worker/store_mongo.go:16-23` (struct + constructor), `:48-50` (`GetRoomMeta`)
- Modify: `broadcast-worker/main.go:42-50` (config), `:74-80` (store construction), `:211-214` (shutdown)
- Test: `broadcast-worker/metacache_integration_test.go` (new)

- [ ] **Step 1: Write the failing integration test**

Create `broadcast-worker/metacache_integration_test.go`:

```go
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
	db := testutil.MongoDB(t, "bw-meta")
	rooms := db.Collection("rooms")

	_, err := rooms.InsertOne(ctx, bson.M{
		"_id": "r1", "name": "general", "type": model.RoomTypeChannel,
		"siteId": "site-a", "userCount": 4,
	})
	require.NoError(t, err)

	store := NewMongoStore(rooms, db.Collection("subscriptions"), client, time.Minute)

	got, err := store.GetRoomMeta(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "general", got.Name)

	// Served from L2 after the Mongo doc is gone.
	_, err = rooms.DeleteOne(ctx, bson.M{"_id": "r1"})
	require.NoError(t, err)
	again, err := store.GetRoomMeta(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, "general", again.Name)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `make test-integration SERVICE=broadcast-worker`
Expected: FAIL — `not enough arguments in call to NewMongoStore` (constructor still takes 2 args).

- [ ] **Step 3: Update the store**

In `broadcast-worker/store_mongo.go`, replace the struct + constructor (lines 16-23):

```go
type mongoStore struct {
	roomCol *mongo.Collection
	subCol  *mongo.Collection
	valkey  valkeyutil.Client // nil disables the L2 tier (pure Mongo)
	metaTTL time.Duration
}

func NewMongoStore(roomCol, subCol *mongo.Collection, valkey valkeyutil.Client, metaTTL time.Duration) *mongoStore {
	return &mongoStore{roomCol: roomCol, subCol: subCol, valkey: valkey, metaTTL: metaTTL}
}
```

Replace `GetRoomMeta` (lines 48-50):

```go
func (m *mongoStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return roommetacache.ReadThrough(ctx, m.valkey, m.roomCol, roomID, m.metaTTL)
}
```

Add `"github.com/hmchangw/chat/pkg/valkeyutil"` to the import block (after the `roommetacache` import). `time` is already imported.

- [ ] **Step 4: Wire the Valkey client in main**

In `broadcast-worker/main.go`, add to the `config` struct (after line 43, `RoomMetaCacheTTL`):

```go
	RoomMetaL2TTL        time.Duration           `env:"ROOM_META_L2_TTL"          envDefault:"15m"`
```

Replace the store construction block (lines 74-80) with:

```go
	db := mongoClient.Database(cfg.MongoDB)
	var metaValkey valkeyutil.Client
	if len(cfg.ValkeyAddrs) > 0 {
		metaValkey, err = valkeyutil.ConnectCluster(ctx, cfg.ValkeyAddrs, cfg.ValkeyPassword)
		if err != nil {
			slog.Error("valkey connect (room-meta L2) failed", "error", err)
			os.Exit(1)
		}
		slog.Info("room-meta L2 cache enabled", "ttl", cfg.RoomMetaL2TTL)
	}
	store := NewMongoStore(db.Collection("rooms"), db.Collection("subscriptions"), metaValkey, cfg.RoomMetaL2TTL)
	cachedStore, err := newCachedMetaStore(store, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL)
	if err != nil {
		slog.Error("init room meta cache failed", "error", err)
		os.Exit(1)
	}
	slog.Info("room-meta-cache enabled", "size", cfg.RoomMetaCacheSize, "ttl", cfg.RoomMetaCacheTTL)
```

Add the shutdown hook — after line 214 (`mongoutil.Disconnect`), append before `shutdown.Wait`:

```go
	hooks = append(hooks, func(_ context.Context) error { valkeyutil.Disconnect(metaValkey); return nil })
```

Add `"github.com/hmchangw/chat/pkg/valkeyutil"` to the import block.

> Note: `metaValkey` is a **separate** `valkeyutil.Client` from the encryption `keyStore` (different interface, `Del` not exposed by `RoomKeyStore`). Two pools to the same cluster is acceptable. `valkeyutil.Disconnect` is nil-safe, so the hook is fine when Valkey is unconfigured.

- [ ] **Step 5: Run the integration test to verify it passes**

Run: `make test-integration SERVICE=broadcast-worker`
Expected: PASS.

- [ ] **Step 6: Run unit tests + lint**

Run: `make test SERVICE=broadcast-worker && make lint`
Expected: PASS, no findings. (Existing store/handler unit tests that construct `NewMongoStore` may need the two new args — if any unit test calls `NewMongoStore`, pass `nil, 0`. Search: `grep -rn "NewMongoStore(" broadcast-worker`.)

- [ ] **Step 7: Commit**

```bash
git add broadcast-worker/store_mongo.go broadcast-worker/main.go broadcast-worker/metacache_integration_test.go
git commit -m "feat(broadcast-worker): L2 Valkey read-through for room meta

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

---

## Task 4: message-gatekeeper — L2 read-through for the large-room check

**Files:**
- Modify: `message-gatekeeper/store_mongo.go:15-25` (struct + constructor), `:39-41` (`GetRoomMeta`)
- Modify: `message-gatekeeper/main.go:25-44` (config), `:79-82` (store construction), `:161-179` (shutdown)
- Test: `message-gatekeeper/store_integration_test.go` (new)

- [ ] **Step 1: Write the failing integration test**

Create `message-gatekeeper/store_integration_test.go`:

```go
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

	// Served from L2 after the Mongo doc is gone.
	_, err = db.Collection("rooms").DeleteOne(ctx, bson.M{"_id": "r1"})
	require.NoError(t, err)
	again, err := store.GetRoomMeta(ctx, "r1")
	require.NoError(t, err)
	assert.Equal(t, 600, again.UserCount)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `make test-integration SERVICE=message-gatekeeper`
Expected: FAIL — `not enough arguments in call to NewMongoStore`.

- [ ] **Step 3: Update the store**

In `message-gatekeeper/store_mongo.go`, replace the struct + constructor (lines 15-25):

```go
type MongoStore struct {
	subscriptions *mongo.Collection
	rooms         *mongo.Collection
	valkey        valkeyutil.Client // nil disables the L2 tier (pure Mongo)
	metaTTL       time.Duration
}

func NewMongoStore(db *mongo.Database, valkey valkeyutil.Client, metaTTL time.Duration) *MongoStore {
	return &MongoStore{
		subscriptions: db.Collection("subscriptions"),
		rooms:         db.Collection("rooms"),
		valkey:        valkey,
		metaTTL:       metaTTL,
	}
}
```

Replace `GetRoomMeta` (lines 39-41):

```go
func (s *MongoStore) GetRoomMeta(ctx context.Context, roomID string) (roommetacache.Meta, error) {
	return roommetacache.ReadThrough(ctx, s.valkey, s.rooms, roomID, s.metaTTL)
}
```

Add `"time"` and `"github.com/hmchangw/chat/pkg/valkeyutil"` to the import block.

- [ ] **Step 4: Wire the Valkey client in main**

In `message-gatekeeper/main.go`, add to the `config` struct (after line 39, `RoomMetaCacheTTL`):

```go
	ValkeyAddrs        []string                `env:"VALKEY_ADDRS"               envSeparator:","`
	ValkeyPassword     string                  `env:"VALKEY_PASSWORD"            envDefault:""`
	RoomMetaL2TTL      time.Duration           `env:"ROOM_META_L2_TTL"           envDefault:"15m"`
```

Replace the store construction (lines 79-82) — currently `db := ...` then `mongoStore := NewMongoStore(db)` then `withMeta, err := newCachedMetaStore(...)`:

```go
	db := mongoClient.Database(cfg.MongoDB)

	var metaValkey valkeyutil.Client
	if len(cfg.ValkeyAddrs) > 0 {
		metaValkey, err = valkeyutil.ConnectCluster(ctx, cfg.ValkeyAddrs, cfg.ValkeyPassword)
		if err != nil {
			slog.Error("valkey connect (room-meta L2) failed", "error", err)
			os.Exit(1)
		}
		slog.Info("room-meta L2 cache enabled", "ttl", cfg.RoomMetaL2TTL)
	}

	mongoStore := NewMongoStore(db, metaValkey, cfg.RoomMetaL2TTL)
	withMeta, err := newCachedMetaStore(mongoStore, cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL)
	if err != nil {
		slog.Error("init room meta cache failed", "error", err)
		os.Exit(1)
	}
```

Add the shutdown hook. Replace the `shutdown.Wait(...)` call (lines 161-179) by adding one final hook before the closing `)`:

```go
		func(ctx context.Context) error { mongoutil.Disconnect(ctx, mongoClient); return nil },
		func(_ context.Context) error { valkeyutil.Disconnect(metaValkey); return nil },
	)
```

Add `"github.com/hmchangw/chat/pkg/valkeyutil"` to the import block.

- [ ] **Step 5: Run the integration test to verify it passes**

Run: `make test-integration SERVICE=message-gatekeeper`
Expected: PASS.

- [ ] **Step 6: Run unit tests + lint**

Run: `make test SERVICE=message-gatekeeper && make lint`
Expected: PASS. If any unit test calls `NewMongoStore(db)`, update it to `NewMongoStore(db, nil, 0)`. Search: `grep -rn "NewMongoStore(" message-gatekeeper`.

- [ ] **Step 7: Commit**

```bash
git add message-gatekeeper/store_mongo.go message-gatekeeper/main.go message-gatekeeper/store_integration_test.go
git commit -m "feat(message-gatekeeper): L2 Valkey read-through for room meta

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

---

## Task 5: notification-worker — point the L1 loader at ReadThrough

**Files:**
- Modify: `notification-worker/main.go:43-53` (config), `:139-155` (loader + Valkey client ordering)

This service already constructs a `valkeyutil.Client` (`valkeyClient`, line 150) and disconnects it on shutdown (line 341). The only change: move the client construction *above* the `roomMetaCache` construction and swap the loader from `FetchFromMongo` to `ReadThrough`.

- [ ] **Step 1: Add the L2 TTL config**

In `notification-worker/main.go`, add to the `config` struct (after line 44, `RoomMetaCacheTTL`):

```go
	RoomMetaL2TTL          time.Duration           `env:"ROOM_META_L2_TTL"          envDefault:"15m"`
```

- [ ] **Step 2: Reorder Valkey construction and swap the loader**

Replace lines 139-155 (from `roomsCol := db.Collection("rooms")` through the `roomsubcache.NewValkeyCache` line). Currently `roomMetaCache` is built (139-148) *before* `valkeyClient` (150-154). Reorder so the client comes first:

```go
	roomsCol := db.Collection("rooms")

	valkeyClient, err := valkeyutil.ConnectCluster(ctx, cfg.ValkeyAddrs, cfg.ValkeyPassword)
	if err != nil {
		slog.Error("valkey connect failed", "error", err)
		os.Exit(1)
	}

	roomMetaCache, err := roommetacache.New(cfg.RoomMetaCacheSize, cfg.RoomMetaCacheTTL,
		func(ctx context.Context, roomID string) (roommetacache.Meta, error) {
			return roommetacache.ReadThrough(ctx, valkeyClient, roomsCol, roomID, cfg.RoomMetaL2TTL)
		})
	if err != nil {
		slog.Error("init room-meta cache failed", "error", err)
		os.Exit(1)
	}

	cache := roomsubcache.NewValkeyCache(valkeyClient)
```

(The original `subCol`/`threadRoomCol` assignments on lines 137-138 stay where they are above; only `roomsCol` and the two constructions move into this reordered block. The `loader := &mongoMemberLoader{col: subCol}` line that followed `cache := ...` is unchanged and stays directly after this block.)

- [ ] **Step 3: Verify it builds + unit tests pass**

Run: `make test SERVICE=notification-worker`
Expected: PASS (this is main-wiring; behavior is covered by `pkg/roommetacache` integration tests).

- [ ] **Step 4: Lint**

Run: `make lint`
Expected: no findings.

- [ ] **Step 5: Commit**

```bash
git add notification-worker/main.go
git commit -m "feat(notification-worker): route room-meta loader through L2 Valkey

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

---

## Task 6: room-worker — write-site L2 invalidation

**Files:**
- Modify: `room-worker/handler.go:52-75` (Handler struct + add `bustRoomMeta`), `:339-341`, `:547-549`, `:969-971`, `:1397-1399`, `:1846-1849` (5 call sites)
- Modify: `room-worker/main.go:53-148` (construct + inject Valkey client), `:199-225` (shutdown)
- Test: `room-worker/handler_test.go` (add `bustRoomMeta` unit tests)

- [ ] **Step 1: Write the failing unit tests**

Append to `room-worker/handler_test.go`:

```go
type fakeBustClient struct {
	dels   []string
	delErr error
}

func (f *fakeBustClient) Get(context.Context, string) (string, error) { return "", nil }
func (f *fakeBustClient) Set(context.Context, string, string, time.Duration) error {
	return nil
}
func (f *fakeBustClient) Del(_ context.Context, keys ...string) error {
	f.dels = append(f.dels, keys...)
	return f.delErr
}
func (f *fakeBustClient) Close() error { return nil }

func TestHandler_bustRoomMeta_CallsDel(t *testing.T) {
	fake := &fakeBustClient{}
	h := &Handler{valkey: fake}
	h.bustRoomMeta(context.Background(), "r123")
	assert.Equal(t, []string{roommetacache.MetaKey("r123")}, fake.dels)
}

func TestHandler_bustRoomMeta_NilClient_NoPanic(t *testing.T) {
	h := &Handler{} // valkey nil
	assert.NotPanics(t, func() { h.bustRoomMeta(context.Background(), "r123") })
}

func TestHandler_bustRoomMeta_FailOpen(t *testing.T) {
	fake := &fakeBustClient{delErr: errors.New("valkey down")}
	h := &Handler{valkey: fake}
	assert.NotPanics(t, func() { h.bustRoomMeta(context.Background(), "r123") })
}
```

Ensure `room-worker/handler_test.go` imports `"context"`, `"errors"`, `"time"`, `"github.com/stretchr/testify/assert"`, and `"github.com/hmchangw/chat/pkg/roommetacache"` (add any missing).

- [ ] **Step 2: Run to verify it fails**

Run: `make test SERVICE=room-worker`
Expected: FAIL — `unknown field 'valkey' in struct literal of type Handler` and `h.bustRoomMeta undefined`.

- [ ] **Step 3: Add the field + method to the handler**

In `room-worker/handler.go`, add a field to the `Handler` struct (after `keyFanoutWorkers int`, line 63):

```go
	// valkey is the L2 (Valkey) client used only to invalidate room metadata
	// after authoritative writes. nil disables invalidation (best-effort).
	valkey valkeyutil.Client
```

Add the method (place it right after `NewHandler`, after line 75):

```go
// bustRoomMeta best-effort invalidates a room's L2 (Valkey) metadata entry
// after an authoritative Mongo write to name or userCount. Fail-open: nil
// client is a no-op, Valkey errors are logged-and-swallowed by BustMeta, and a
// short timeout ensures a hung Valkey never stalls the room operation.
func (h *Handler) bustRoomMeta(ctx context.Context, roomID string) {
	if h.valkey == nil {
		return
	}
	bustCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	roommetacache.BustMeta(bustCtx, h.valkey, roomID)
}
```

Add `"github.com/hmchangw/chat/pkg/roommetacache"` and `"github.com/hmchangw/chat/pkg/valkeyutil"` to the import block. (`context` and `time` are already imported.)

- [ ] **Step 4: Add the 5 invalidation call sites**

Each insertion goes immediately after the `ReconcileMemberCounts` / `UpdateRoomName` write commits.

**4a — `processRemoveIndividual` (after line 341):**

```go
	if err := h.store.ReconcileMemberCounts(ctx, req.RoomID); err != nil {
		return fmt.Errorf("reconcile member counts: %w", err)
	}
	h.bustRoomMeta(ctx, req.RoomID)
```

**4b — `processRemoveOrg` (after line 549):**

```go
	if err := h.store.ReconcileMemberCounts(ctx, req.RoomID); err != nil {
		return fmt.Errorf("reconcile member counts: %w", err)
	}
	h.bustRoomMeta(ctx, req.RoomID)
```

**4c — `processAddMembers` (after line 971):**

```go
	if err := h.store.ReconcileMemberCounts(ctx, req.RoomID); err != nil {
		return fmt.Errorf("reconcile member counts: %w", err)
	}
	h.bustRoomMeta(ctx, req.RoomID)
```

**4d — `finishCreateRoom` (after line 1399):**

```go
	if err := h.store.ReconcileMemberCounts(ctx, room.ID); err != nil {
		return fmt.Errorf("reconcile member counts: %w", err)
	}
	h.bustRoomMeta(ctx, room.ID)
```

(On create the L2 entry is almost never present — `ReadThrough` only populates on a successful Mongo fetch — so this is a near-no-op kept for uniformity: every count-changing write busts.)

**4e — `processRoomRename` (after line 1849, i.e. after `UpdateSubscriptionNamesForRoom` succeeds):**

```go
	if err = h.store.UpdateSubscriptionNamesForRoom(ctx, req.RoomID, req.NewName); err != nil {
		return fmt.Errorf("update subscription names: %w", err)
	}
	h.bustRoomMeta(ctx, req.RoomID)
```

- [ ] **Step 5: Wire and inject the Valkey client in main**

In `room-worker/main.go`, construct a `valkeyutil.Client` reusing the existing required `cfg.ValkeyAddrs` / `cfg.ValkeyPassword`. Add this immediately after the `keyStore` construction block (after line 112, the closing `}` of the `if err != nil` for `NewValkeyClusterStore`):

```go
	metaValkey, err := valkeyutil.ConnectCluster(ctx, cfg.ValkeyAddrs, cfg.ValkeyPassword)
	if err != nil {
		slog.Error("valkey connect (room-meta L2 invalidation) failed", "error", err)
		os.Exit(1)
	}
```

After the handler is constructed (after line 150, `handler.dekProvisioner = dekProvisioner`), inject the client:

```go
	handler.valkey = metaValkey
```

Add a shutdown hook. In the `hooks` slice (lines 199-225), add after the `keyStore.Close()` hook (line 216):

```go
		func(_ context.Context) error { valkeyutil.Disconnect(metaValkey); return nil },
```

Add `"github.com/hmchangw/chat/pkg/valkeyutil"` to the import block.

> Note: this is a second pool to the same Valkey cluster as `keyStore` (`roomkeystore.RoomKeyStore` doesn't expose a generic `Del`). Acceptable. `VALKEY_ADDRS` is already `required` in room-worker, so no new env var is introduced.

- [ ] **Step 6: Run unit tests + lint**

Run: `make test SERVICE=room-worker && make lint`
Expected: PASS. Existing handler tests construct `NewHandler(...)` (5 args) — they're unaffected because `valkey` is a field defaulting to nil (bust no-ops).

- [ ] **Step 7: Run room-worker integration tests (sanity)**

Run: `make test-integration SERVICE=room-worker`
Expected: PASS — the bust is a best-effort side effect; existing member/rename flows are unchanged.

- [ ] **Step 8: Commit**

```bash
git add room-worker/handler.go room-worker/main.go room-worker/handler_test.go
git commit -m "feat(room-worker): bust room-meta L2 cache on rename/member-count writes

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

---

## Task 7: Local dev wiring + repo-wide verification

**Files:**
- Modify: `broadcast-worker/deploy/docker-compose.yml`, `message-gatekeeper/deploy/docker-compose.yml`

room-worker and notification-worker already require `VALKEY_ADDRS` (their compose files already set it). broadcast-worker (Valkey was optional, encryption-only) and message-gatekeeper (no Valkey before) need the env added so the L2 tier is exercised locally.

- [ ] **Step 1: Add Valkey env to broadcast-worker compose**

Read `broadcast-worker/deploy/docker-compose.yml`. In the broadcast-worker service's `environment:` block, add (matching the existing indentation and the Valkey address used by other services' compose, e.g. `valkey:6379` or the cluster seed list already present for room-worker):

```yaml
      VALKEY_ADDRS: "valkey:6379"
      ROOM_META_L2_TTL: "15m"
```

If a `valkey` service is not already a dependency in this compose file, add it under `depends_on:` mirroring how room-worker's compose declares it. (Cross-check `room-worker/deploy/docker-compose.yml` for the exact service name and address.)

- [ ] **Step 2: Add Valkey env to message-gatekeeper compose**

Read `message-gatekeeper/deploy/docker-compose.yml`. In the message-gatekeeper service's `environment:` block, add:

```yaml
      VALKEY_ADDRS: "valkey:6379"
      ROOM_META_L2_TTL: "15m"
```

Add the `valkey` service / `depends_on` entry the same way if absent.

- [ ] **Step 3: Full unit test suite (race)**

Run: `make test`
Expected: PASS across the repo.

- [ ] **Step 4: Full integration suite for touched packages**

Run: `make test-integration SERVICE=pkg/roommetacache && make test-integration SERVICE=broadcast-worker && make test-integration SERVICE=message-gatekeeper`
Expected: PASS.

- [ ] **Step 5: Lint + SAST gate**

Run: `make lint && make sast`
Expected: no findings (SAST fails on medium+). The new code adds no `InsecureSkipVerify`, unchecked conversions, or subprocess calls, so no `#nosec` suppressions should be needed.

- [ ] **Step 6: Commit**

```bash
git add broadcast-worker/deploy/docker-compose.yml message-gatekeeper/deploy/docker-compose.yml
git commit -m "chore(deploy): enable room-meta L2 Valkey for broadcast-worker + gatekeeper

https://claude.ai/code/session_014JMTmDhjFW1nBjfkCfeXJx"
```

- [ ] **Step 7: Push the branch**

```bash
git push -u origin claude/valkey-utilization-opportunities-xGcSW
```

---

## Self-Review

**Spec coverage:**
- Two-tier L1+L2 → L1 unchanged; L2 added via `ReadThrough` (Task 1) wired into all three readers (Tasks 3-5). ✓
- 15m absolute L2 TTL → `ROOM_META_L2_TTL` default `15m` on every reader (Tasks 3-5). ✓
- Fail-open reads → `ReadThrough` nil-client + error fallthrough (Task 1), unit + integration tested. ✓
- Room-worker write-site invalidation, best-effort `DEL` → `BustMeta` + `bustRoomMeta` at all 4 reconcile sites + rename (Task 6). ✓
- Shared key builder so room-worker never hardcodes the scheme → `roommetacache.MetaKey`, used by both readers (via `ReadThrough`) and the buster. ✓
- No app-side L2 LRU (TTL + cluster `volatile-lru`) → L2 is plain `SET … EX`; no app eviction added. ✓
- Repopulate race + bust-after-commit window → documented as accepted, TTL-backstopped (Architecture section). ✓

**Placeholder scan:** No TBD/TODO; every code step shows complete code; every command has an expected result. Compose edits (Task 7) instruct reading the file first because exact indentation/service-name varies — values to add are given explicitly.

**Type consistency:** `MetaKey(roomID) string`, `ReadThrough(ctx, valkeyutil.Client, *mongo.Collection, roomID string, ttl time.Duration) (Meta, error)`, `BustMeta(ctx, valkeyutil.Client, roomID string)` are used identically across Tasks 1-6. `NewMongoStore` new signatures: broadcast-worker `(roomCol, subCol *mongo.Collection, valkey valkeyutil.Client, metaTTL time.Duration)`, message-gatekeeper `(db *mongo.Database, valkey valkeyutil.Client, metaTTL time.Duration)` — matched in their respective main.go calls and tests. Handler field `valkey valkeyutil.Client` + method `bustRoomMeta(ctx, roomID)` consistent in Task 6.
