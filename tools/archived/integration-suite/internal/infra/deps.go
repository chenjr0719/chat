package infra

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/hmchangw/chat/pkg/testutil/testimages"
)

// startNATS launches a NATS container with the operator-signed trust
// chain from docker-local/nats.conf — the same config the manual
// `make deps-up` flow uses. Without this, auth-service can't mint
// JWTs the broker accepts and every other microservice fails its
// connect handshake. Aliases match the production DNS names referenced
// by docker-local/toxiproxy.json (chat-local-nats) and per-service
// compose env vars (nats).
func startNATS(ctx context.Context, networkName, repoRoot string) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        testimages.NATS,
		ExposedPorts: []string{"4222/tcp", "8222/tcp"},
		// `-c <conf>` makes nats-server pick up the operator JWT +
		// resolver_preload + jetstream block from the mounted file.
		// JetStream is enabled via the conf's `jetstream {}` stanza,
		// so the previous `--jetstream` flag is no longer needed.
		Cmd:      []string{"-c", "/etc/nats/nats.conf"},
		Networks: []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"nats", "chat-local-nats"},
		},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      filepath.Join(repoRoot, "docker-local", "nats.conf"),
				ContainerFilePath: "/etc/nats/nats.conf",
				FileMode:          0o400,
			},
		},
		WaitingFor: wait.ForLog("Server is ready").
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start nats: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("nats host: %w", err)
	}
	port, err := c.MappedPort(ctx, "4222")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("nats port: %w", err)
	}
	return c, fmt.Sprintf("nats://%s:%s", host, port.Port()), nil
}

// startMongo launches a Mongo container on the shared network. Aliases
// match both the production DNS forms — `mongodb` (used by per-service
// compose env) and `chat-local-mongodb` (used by toxiproxy.json upstream).
func startMongo(ctx context.Context, networkName string) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        testimages.Mongo,
		ExposedPorts: []string{"27017/tcp"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"mongodb", "chat-local-mongodb"},
		},
		WaitingFor: wait.ForLog("Waiting for connections").
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start mongo: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("mongo host: %w", err)
	}
	port, err := c.MappedPort(ctx, "27017")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("mongo port: %w", err)
	}
	return c, fmt.Sprintf("mongodb://%s:%s", host, port.Port()), nil
}

// startCassandra launches a cassandra:5 container with the same memory
// caps used in docker-local/compose.deps.yaml. Returns the container
// handle and the host-mapped host:port string (no scheme; gocql takes
// host[:port] directly).
//
// Hard-pinned to cassandra:5 (not testimages.Cassandra which points at
// 4.1.3) because Phase 2 mirrors production. Heap caps (256M max,
// 128M new) keep the runner under 1 GB even when Cassandra's off-heap
// allocator wakes up — empirically the smallest values that survive
// schema init + a few writes without GC thrash.
func startCassandra(ctx context.Context, networkName string) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "cassandra:5",
		ExposedPorts: []string{"9042/tcp"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"cassandra", "chat-local-cassandra"},
		},
		Env: map[string]string{
			"CASSANDRA_CLUSTER_NAME": "chat-dev",
			"MAX_HEAP_SIZE":          "256M",
			"HEAP_NEWSIZE":           "128M",
		},
		WaitingFor: wait.ForLog("Starting listening for CQL clients").
			WithStartupTimeout(5 * time.Minute),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start cassandra: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("cassandra host: %w", err)
	}
	port, err := c.MappedPort(ctx, "9042")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("cassandra port: %w", err)
	}
	return c, fmt.Sprintf("%s:%s", host, port.Port()), nil
}

// startValkey boots a single-node Valkey in cluster mode. Mirrors the
// docker-local/compose.deps.yaml entrypoint so CLUSTER SLOTS responds
// with the `valkey` alias as the publish address.
func startValkey(ctx context.Context, networkName string) (testcontainers.Container, string, error) {
	const entrypoint = `valkey-server --cluster-enabled yes --cluster-config-file /tmp/nodes.conf --cluster-node-timeout 5000 --cluster-announce-hostname valkey --cluster-preferred-endpoint-type hostname --save '' --appendonly no &
until valkey-cli ping > /dev/null 2>&1; do sleep 0.1; done
if ! valkey-cli CLUSTER INFO | grep -q 'cluster_slots_assigned:16384'; then
  valkey-cli CLUSTER ADDSLOTSRANGE 0 16383
fi
wait`
	req := testcontainers.ContainerRequest{
		Image:        testimages.Valkey,
		ExposedPorts: []string{"6379/tcp"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"valkey", "chat-local-valkey"},
		},
		Entrypoint: []string{"sh", "-c", entrypoint},
		// "Cluster state changed: ok" is what valkey-server prints to
		// stdout after CLUSTER ADDSLOTSRANGE finishes. The CLUSTER INFO
		// reply field "cluster_state:ok" only appears in the admin
		// response — it's never emitted to the container log, so the
		// previous wait timed out at 30s every time the runner cold-
		// started.
		WaitingFor: wait.ForLog("Cluster state changed: ok").
			WithStartupTimeout(30 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start valkey: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("valkey host: %w", err)
	}
	port, err := c.MappedPort(ctx, "6379")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("valkey port: %w", err)
	}
	return c, fmt.Sprintf("%s:%s", host, port.Port()), nil
}
