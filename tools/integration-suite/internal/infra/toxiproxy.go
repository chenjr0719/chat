package infra

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startToxiproxy launches the Phase 1 Toxiproxy container on the
// shared network, mounting docker-local/toxiproxy.json so the three
// pre-provisioned proxies (MongoProxy, CassandraProxy, WANProxy) are
// available the moment the admin /version endpoint answers.
//
// Returns the container handle and the host-mapped admin URL the
// chaos engine uses to toggle proxies.
func startToxiproxy(ctx context.Context, networkName, repoRoot string) (testcontainers.Container, string, error) {
	cfgPath := filepath.Join(repoRoot, "docker-local", "toxiproxy.json")
	req := testcontainers.ContainerRequest{
		Image:        "ghcr.io/shopify/toxiproxy:2.9.0",
		ExposedPorts: []string{"8474/tcp", "27017/tcp", "9042/tcp"},
		Cmd:          []string{"-host=0.0.0.0", "-config=/etc/toxiproxy/toxiproxy.json"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"chat-local-toxiproxy"},
		},
		Files: []testcontainers.ContainerFile{
			{HostFilePath: cfgPath, ContainerFilePath: "/etc/toxiproxy/toxiproxy.json", FileMode: 0o444},
		},
		WaitingFor: wait.ForHTTP("/version").
			WithPort("8474/tcp").
			WithStartupTimeout(30 * time.Second),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		return nil, "", fmt.Errorf("start toxiproxy: %w", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("toxiproxy host: %w", err)
	}
	port, err := c.MappedPort(ctx, "8474")
	if err != nil {
		_ = c.Terminate(ctx)
		return nil, "", fmt.Errorf("toxiproxy port: %w", err)
	}
	return c, fmt.Sprintf("http://%s:%s", host, port.Port()), nil
}
