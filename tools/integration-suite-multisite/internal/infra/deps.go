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
// chain from docker-local/nats.conf plus a per-site topology config
// file that enables JetStream (with a per-site domain) and leafnode
// transport. The site parameter ("site-a" or "site-b") selects the
// topology config file and determines the container's DNS alias on
// the shared network (nats-site-a / nats-site-b). Both aliases are
// used by Toxiproxy's NATSProxy-<site> entries and by the leafnode
// remote URL in site-b's topology conf.
//
// backend.creds is mounted alongside the configs so site-b's leafnode
// remote can authenticate into site-a's chatapp account at dial time.
func startNATS(ctx context.Context, networkName, repoRoot, site string) (testcontainers.Container, string, error) {
	topologyConfFile := filepath.Join(repoRoot, "tools", "integration-suite-multisite", "internal", "infra", "nats.gateway."+site+".conf")
	req := testcontainers.ContainerRequest{
		Image:        testimages.NATS,
		ExposedPorts: []string{"4222/tcp", "8222/tcp", "7422/tcp"},
		// Load both the operator trust-chain config and the per-site
		// topology config. nats-server merges multiple -c files.
		Cmd:      []string{"-c", "/etc/nats/nats.conf", "-c", "/etc/nats/gateway.conf"},
		Networks: []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"nats-" + site},
		},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      filepath.Join(repoRoot, "docker-local", "nats.conf"),
				ContainerFilePath: "/etc/nats/nats.conf",
				FileMode:          0o400,
			},
			{
				HostFilePath:      topologyConfFile,
				ContainerFilePath: "/etc/nats/gateway.conf",
				FileMode:          0o400,
			},
			{
				HostFilePath:      filepath.Join(repoRoot, "docker-local", "backend.creds"),
				ContainerFilePath: "/etc/nats/backend.creds",
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
		return nil, "", fmt.Errorf("start nats (%s): %w", site, err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("nats (%s) host: %w", site, err)
	}
	port, err := c.MappedPort(ctx, "4222")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("nats (%s) port: %w", site, err)
	}
	return c, fmt.Sprintf("nats://%s:%s", host, port.Port()), nil
}

// startMongo launches a Mongo container on the shared network. The site
// parameter ("site-a" or "site-b") gives each site its own Mongo
// instance with a distinct DNS alias (mongo-site-a / mongo-site-b),
// which Toxiproxy's MongoProxy-<site> entries upstream to.
func startMongo(ctx context.Context, networkName, site string) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        testimages.Mongo,
		ExposedPorts: []string{"27017/tcp"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"mongo-" + site},
		},
		WaitingFor: wait.ForLog("Waiting for connections").
			WithStartupTimeout(60 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start mongo (%s): %w", site, err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("mongo (%s) host: %w", site, err)
	}
	port, err := c.MappedPort(ctx, "27017")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("mongo (%s) port: %w", site, err)
	}
	return c, fmt.Sprintf("mongodb://%s:%s", host, port.Port()), nil
}

// startCassandra launches a cassandra:5 container with the same memory
// caps used in docker-local/compose.deps.yaml. Returns the container
// handle and the host-mapped host:port string (no scheme; gocql takes
// host[:port] directly).
//
// Cassandra is shared across both sites — both CassandraProxy-site-a
// and CassandraProxy-site-b upstream to the same cassandra alias.
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

// startValkey boots a single-node Valkey in cluster mode. The site
// parameter ("site-a" or "site-b") gives each site its own Valkey
// instance with a distinct DNS alias (valkey-site-a / valkey-site-b).
// Each instance announces its hostname so CLUSTER SLOTS responds with
// the alias as the publish address, matching what the services expect.
func startValkey(ctx context.Context, networkName, site string) (testcontainers.Container, string, error) {
	hostname := "valkey-" + site
	entrypoint := fmt.Sprintf(`valkey-server --cluster-enabled yes --cluster-config-file /tmp/nodes.conf --cluster-node-timeout 5000 --cluster-announce-hostname %s --cluster-preferred-endpoint-type hostname --save '' --appendonly no &
until valkey-cli ping > /dev/null 2>&1; do sleep 0.1; done
if ! valkey-cli CLUSTER INFO | grep -q 'cluster_slots_assigned:16384'; then
  valkey-cli CLUSTER ADDSLOTSRANGE 0 16383
fi
wait`, hostname)
	req := testcontainers.ContainerRequest{
		Image:        testimages.Valkey,
		ExposedPorts: []string{"6379/tcp"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {hostname},
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
		return nil, "", fmt.Errorf("start valkey (%s): %w", site, err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("valkey (%s) host: %w", site, err)
	}
	port, err := c.MappedPort(ctx, "6379")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("valkey (%s) port: %w", site, err)
	}
	return c, fmt.Sprintf("%s:%s", host, port.Port()), nil
}
