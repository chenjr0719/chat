package runtime

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hmchangw/chat/pkg/roomkeystore"
)

// buildValkeyKeyStore returns a RoomKeyStore the runner uses once at
// startup to write seed/room-keys.json into Valkey.
//
// Two paths:
//
//   - Default (USE_INFRA unset / manual `make deps-up` flow):
//     NewValkeyClusterStore opens a fresh ClusterClient, which issues
//     CLUSTER SLOTS and trusts the addresses the cluster advertises.
//     That's fine when the runner can reach those addresses by DNS.
//
//   - USE_INFRA=true (Phase 2 programmatic flow):
//     The Valkey container is launched with
//     `--cluster-announce-hostname valkey`, so CLUSTER SLOTS reports
//     `valkey:6379` as the slot owner. The runner runs on the HOST,
//     where `valkey` doesn't resolve — every command after the initial
//     PING would `dial tcp: lookup valkey: no such host`. We bypass the
//     advertisement by handing the ClusterClient a ClusterSlots
//     override that statically maps every slot (0–16383) to the
//     host-mapped Testcontainers port. This is the exact workaround
//     pkg/roomkeystore documents on NewValkeyClusterStoreFromClient.
//
// Both paths return the same RoomKeyStore interface so seed.LoadRoomKeys
// runs identically regardless of which one was taken.
func buildValkeyKeyStore(addr string) (roomkeystore.RoomKeyStore, error) {
	const gracePeriod = 24 * time.Hour

	if os.Getenv("USE_INFRA") != "true" {
		return roomkeystore.NewValkeyClusterStore(roomkeystore.ClusterConfig{
			Addrs:       []string{addr},
			GracePeriod: gracePeriod,
		})
	}

	client := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        []string{addr},
		ClusterSlots: staticClusterSlots(addr),
	})
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(pingCtx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("valkey cluster connect (USE_INFRA): %w", err)
	}
	return roomkeystore.NewValkeyClusterStoreFromClient(client, gracePeriod), nil
}

// staticClusterSlots returns a ClusterSlots callback that maps the
// entire keyspace (slots 0–16383) to a single node at addr. This is
// the host-side override the USE_INFRA=true path installs to bypass
// the `valkey:6379` advertisement that comes back from
// `--cluster-announce-hostname valkey`.
//
// Lifted out of buildValkeyKeyStore so tests can verify the slot
// coverage without touching the network (Ping would block 5 s).
func staticClusterSlots(addr string) func(context.Context) ([]redis.ClusterSlot, error) {
	return func(_ context.Context) ([]redis.ClusterSlot, error) {
		return []redis.ClusterSlot{
			{
				Start: 0,
				End:   16383,
				Nodes: []redis.ClusterNode{{Addr: addr}},
			},
		}, nil
	}
}
