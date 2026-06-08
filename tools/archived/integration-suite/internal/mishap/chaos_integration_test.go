//go:build integration

package mishap

import (
	"context"
	"net"
	"testing"
	"time"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// startToxiproxy spins a Toxiproxy container in host-network mode (so
// the container shares the host's network namespace) and pre-provisions
// a single TestProxy whose upstream is an in-test TCP echo listener.
// Returns (adminURL, testProxyListenAddr, cleanup).
//
// Host networking is Linux-specific. The integration suite already
// assumes Linux per Azure Pipelines convention (CLAUDE.md §1).
func startToxiproxy(t *testing.T) (adminURL, testProxyListenAddr string) {
	t.Helper()
	ctx := context.Background()

	// In-test TCP echo listener: Toxiproxy's upstream.
	echo, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() {
		for {
			c, err := echo.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 64)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					_, _ = c.Write(buf[:n])
				}
			}(c)
		}
	}()
	t.Cleanup(func() { echo.Close() })

	upstream := echo.Addr().String() // 127.0.0.1:NNNNN

	req := testcontainers.ContainerRequest{
		Image:      "ghcr.io/shopify/toxiproxy:2.9.0",
		Cmd:        []string{"-host=0.0.0.0"},
		WaitingFor: wait.ForHTTP("/version").WithPort("8474/tcp").WithStartupTimeout(30 * time.Second),
		HostConfigModifier: func(hc *container.HostConfig) {
			hc.NetworkMode = "host"
		},
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Terminate(ctx) })

	adminURL = "http://localhost:8474"
	testProxyListenAddr = "127.0.0.1:21974"

	// Provision TestProxy. ResetState would no-op (nothing was provisioned
	// at container boot since no -config flag was passed); CreateProxy is
	// the right call. Eventually-retry because the wait-strategy fires
	// when /version answers, but the proxy-create endpoint can race for
	// a few hundred ms after that.
	raw := toxiproxy.NewClient(adminURL)
	require.Eventually(t, func() bool {
		_, err := raw.CreateProxy("TestProxy", "0.0.0.0:21974", upstream)
		return err == nil
	}, 10*time.Second, 200*time.Millisecond)

	return adminURL, testProxyListenAddr
}

func TestToxiproxyEngine_NewVerifiesExpected(t *testing.T) {
	adminURL, _ := startToxiproxy(t)

	// Asking for a proxy that doesn't exist must fail fast.
	_, err := NewToxiproxyEngine(adminURL, []string{"TestProxy", "NopeProxy"})
	assert.Error(t, err)

	// Asking only for present names succeeds.
	eng, err := NewToxiproxyEngine(adminURL, []string{"TestProxy"})
	require.NoError(t, err)
	assert.NotNil(t, eng)
}

func TestToxiproxyEngine_PartitionAndHeal(t *testing.T) {
	adminURL, listenAddr := startToxiproxy(t)

	eng, err := NewToxiproxyEngine(adminURL, []string{"TestProxy"})
	require.NoError(t, err)
	ctx := context.Background()

	// Baseline: dial succeeds.
	conn, err := net.DialTimeout("tcp", listenAddr, 2*time.Second)
	require.NoError(t, err)
	_ = conn.Close()

	// Partition: dial must fail.
	require.NoError(t, eng.Partition(ctx, "TestProxy"))
	_, err = net.DialTimeout("tcp", listenAddr, 500*time.Millisecond)
	assert.Error(t, err, "dial through disabled proxy should fail")

	// Heal: dial succeeds again.
	require.NoError(t, eng.Heal(ctx, "TestProxy"))
	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", listenAddr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 3*time.Second, 100*time.Millisecond, "dial should recover after Heal")
}

func TestToxiproxyEngine_UnknownProxyFastFails(t *testing.T) {
	adminURL, _ := startToxiproxy(t)

	eng, err := NewToxiproxyEngine(adminURL, []string{"TestProxy"})
	require.NoError(t, err)

	err = eng.Partition(context.Background(), "TotallyMadeUp")
	assert.ErrorIs(t, err, ErrUnknownProxy)
}

func TestToxiproxyEngine_ResetReEnablesProxies(t *testing.T) {
	adminURL, listenAddr := startToxiproxy(t)

	eng, err := NewToxiproxyEngine(adminURL, []string{"TestProxy"})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, eng.Partition(ctx, "TestProxy"))
	require.NoError(t, eng.Reset(ctx))

	require.Eventually(t, func() bool {
		c, err := net.DialTimeout("tcp", listenAddr, 500*time.Millisecond)
		if err == nil {
			_ = c.Close()
			return true
		}
		return false
	}, 3*time.Second, 100*time.Millisecond, "Reset should re-enable disabled proxies")
}
