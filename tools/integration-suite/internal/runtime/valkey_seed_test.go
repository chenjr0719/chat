package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildValkeyKeyStore_NoAddrIsAcallerProblem documents what
// buildValkeyKeyStore does NOT do: it doesn't sniff the env var on
// its own. Callers in runner.go already guard with `cfg.ValkeyAddr != ""`.
// Empty addr passed through here would cascade into a redis dial
// failure on the network — not a programming error we surface earlier.
func TestBuildValkeyKeyStore_DefaultPathDocsClusterDiscovery(t *testing.T) {
	// USE_INFRA unset → default path. NewValkeyClusterStore PINGs the
	// cluster, so an unreachable addr returns an error (it doesn't panic
	// or silently no-op). We deliberately point at a dead port so the
	// dial fails fast — proves the default path is wired to the real
	// cluster constructor.
	t.Setenv("USE_INFRA", "")
	_, err := buildValkeyKeyStore("127.0.0.1:1") // port 1 — no listener
	require.Error(t, err)
	assert.Contains(t, err.Error(), "valkey cluster connect")
}

// TestStaticClusterSlotsCallback_MapsAllSlotsToSeedAddr regresses the
// Phase 3.6 routing decision: the ClusterSlots override must cover
// every hash slot (0–16383) and point at the seed addr, otherwise
// go-redis's MOVED handling would route to the advertised
// `valkey:6379` and the host-side runner would fail with `no such host`.
//
// We can't invoke buildValkeyKeyStore directly under USE_INFRA=true
// without a live Valkey (the Ping inside it blocks for 5s), but we
// CAN extract and verify the slot-callback shape — the same closure
// the production path builds.
func TestStaticClusterSlotsCallback_MapsAllSlotsToSeedAddr(t *testing.T) {
	const seedAddr = "host.example:54321"
	slots, err := staticClusterSlots(seedAddr)(context.Background())
	require.NoError(t, err)
	require.Len(t, slots, 1, "must be exactly one slot range covering the whole keyspace")
	assert.Equal(t, 0, slots[0].Start, "range start")
	assert.Equal(t, 16383, slots[0].End, "range end (last hash slot)")
	require.Len(t, slots[0].Nodes, 1, "single node per range")
	assert.Equal(t, seedAddr, slots[0].Nodes[0].Addr, "node must point at the seed addr, not the announced valkey:6379")
}
