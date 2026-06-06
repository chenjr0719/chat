package infra

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startToxiproxy launches the multi-site Toxiproxy container on the
// shared network with NO preloaded config. CreateSiteNamedProxies
// (called once Toxiproxy is up) POSTs all 6 site-named proxies via
// the admin API — that is the single source of truth for the proxy
// set in the multi-site stack.
//
// Returns the container handle and the host-mapped admin URL.
func startToxiproxy(ctx context.Context, networkName string) (testcontainers.Container, string, error) {
	req := testcontainers.ContainerRequest{
		Image:        "ghcr.io/shopify/toxiproxy:2.9.0",
		ExposedPorts: []string{"8474/tcp"},
		Cmd:          []string{"-host=0.0.0.0"},
		Networks:     []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {"chat-local-toxiproxy"},
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

type toxiproxySpec struct {
	Name     string `json:"name"`
	Listen   string `json:"listen"`
	Upstream string `json:"upstream"`
	Enabled  bool   `json:"enabled"`
}

// CreateSiteNamedProxies POSTs the 6 site-named proxies to the
// Toxiproxy admin API. Called once at boot, after Toxiproxy is up
// and before services start.
//
// Mongo and Cassandra route through site-specific proxies; both
// CassandraProxy-site-X upstream the same shared cassandra alias.
func CreateSiteNamedProxies(ctx context.Context, adminURL string) error {
	proxies := []toxiproxySpec{
		{Name: "MongoProxy-site-a", Listen: "0.0.0.0:27017", Upstream: "mongo-site-a:27017", Enabled: true},
		{Name: "MongoProxy-site-b", Listen: "0.0.0.0:27018", Upstream: "mongo-site-b:27017", Enabled: true},
		{Name: "CassandraProxy-site-a", Listen: "0.0.0.0:9042", Upstream: "cassandra:9042", Enabled: true},
		{Name: "CassandraProxy-site-b", Listen: "0.0.0.0:9043", Upstream: "cassandra:9042", Enabled: true},
		{Name: "NATSProxy-site-a", Listen: "0.0.0.0:4222", Upstream: "nats-site-a:4222", Enabled: true},
		{Name: "NATSProxy-site-b", Listen: "0.0.0.0:4223", Upstream: "nats-site-b:4222", Enabled: true},
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, p := range proxies {
		body, _ := json.Marshal(p)
		req, _ := http.NewRequestWithContext(ctx, "POST", adminURL+"/proxies", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("toxiproxy: create %s: %w", p.Name, err)
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("toxiproxy: create %s: HTTP %d", p.Name, resp.StatusCode)
		}
	}
	return nil
}
