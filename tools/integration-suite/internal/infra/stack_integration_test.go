//go:build integration

package infra

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCreateNetwork_ReturnsUniqueNameWithRunIDSuffix(t *testing.T) {
	ctx := context.Background()
	nw1, runID1, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw1.Remove(ctx) })

	nw2, runID2, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw2.Remove(ctx) })

	assert.True(t, strings.HasPrefix(nw1.Name, "infra-"),
		"network name %q must start with infra-", nw1.Name)
	assert.NotEqual(t, runID1, runID2, "run IDs must be unique across calls")
	assert.NotEqual(t, nw1.Name, nw2.Name)
}

func TestStartNATS_ReachableOnHostPort(t *testing.T) {
	ctx := context.Background()
	nw, _, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	c, url, err := startNATS(ctx, nw.Name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	assert.True(t, strings.HasPrefix(url, "nats://"), "url %q must start with nats://", url)

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(url, "nats://"), 2*time.Second)
	require.NoError(t, err)
	_ = conn.Close()
}

func TestStartMongo_ReachableOnHostPort(t *testing.T) {
	ctx := context.Background()
	nw, _, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	c, uri, err := startMongo(ctx, nw.Name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	assert.True(t, strings.HasPrefix(uri, "mongodb://"), "uri %q must start with mongodb://", uri)
}

func TestStartCassandraAndInit_AppliesEveryCQL(t *testing.T) {
	ctx := context.Background()
	nw, _, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	repoRoot, err := resolveRepoRoot(&Config{})
	require.NoError(t, err)

	c, hostPort, err := startCassandra(ctx, nw.Name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	require.NoError(t, cassandraInit(ctx, c, repoRoot))

	// Verify the 'chat' keyspace exists by exec-ing cqlsh.
	exitCode, _, err := c.Exec(ctx, []string{"cqlsh", "-e", "DESCRIBE KEYSPACE chat"})
	require.NoError(t, err)
	assert.Equal(t, 0, exitCode, "DESCRIBE KEYSPACE chat must succeed after init")
	assert.NotEmpty(t, hostPort, "host:port returned for downstream use")
}

func TestStartToxiproxy_AdminReachableAndProxiesProvisioned(t *testing.T) {
	ctx := context.Background()
	nw, _, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	repoRoot, err := resolveRepoRoot(&Config{})
	require.NoError(t, err)

	c, adminURL, err := startToxiproxy(ctx, nw.Name, repoRoot)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	resp, err := http.Get(adminURL + "/proxies")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	for _, name := range []string{"MongoProxy", "CassandraProxy", "WANProxy"} {
		assert.Contains(t, string(body), name, "admin /proxies must list %s", name)
	}
}

// Requires `make build-test-images` to have produced
// chat-local-services-auth-service:latest before this test runs.
// auth-service is the cheapest dependency-free service for a smoke.
func TestStartService_AuthServiceReachesReady(t *testing.T) {
	if os.Getenv("INFRA_SMOKE_IMAGES") != "1" {
		t.Skip("skipping: set INFRA_SMOKE_IMAGES=1 once chat-local-services-* images are built")
	}
	ctx := context.Background()
	nw, _, err := createNetwork(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = nw.Remove(ctx) })

	c, err := startService(ctx, nw.Name, "auth-service", "latest", "site-local", "", "", 24)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	host, err := c.Host(ctx)
	require.NoError(t, err)
	assert.NotEmpty(t, host)
}
