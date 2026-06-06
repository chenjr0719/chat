package infra

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfig_DefaultImageTagFromEnv(t *testing.T) {
	t.Setenv("TEST_IMAGE_TAG", "v1.2.3")
	cfg := Config{}
	assert.Equal(t, "v1.2.3", resolveImageTag(&cfg))
}

func TestConfig_DefaultImageTagExplicitWins(t *testing.T) {
	t.Setenv("TEST_IMAGE_TAG", "v1.2.3")
	cfg := Config{ImageTag: "explicit"}
	assert.Equal(t, "explicit", resolveImageTag(&cfg))
}

func TestConfig_DefaultImageTagFallsBackToLatest(t *testing.T) {
	_ = os.Unsetenv("TEST_IMAGE_TAG")
	cfg := Config{}
	assert.Equal(t, "latest", resolveImageTag(&cfg))
}

func TestConfig_DefaultServicesPhase35(t *testing.T) {
	// Phase 3.5 trimmed search-service + search-sync-worker; the suite
	// never exercises them and their boot-time strict env validation
	// tripped infra.Up. Mock-user-service is still opt-in only.
	cfg := Config{}
	got := resolveServices(&cfg)
	want := []string{
		"auth-service", "broadcast-worker", "history-service", "inbox-worker",
		"message-gatekeeper", "message-worker", "notification-worker",
		"room-service", "room-worker",
	}
	assert.Equal(t, want, got)
	assert.NotContains(t, got, "search-service")
	assert.NotContains(t, got, "search-sync-worker")
}

func TestConfig_DefaultServicesOmitsMockUserService(t *testing.T) {
	cfg := Config{}
	got := resolveServices(&cfg)
	for _, s := range got {
		assert.NotEqual(t, "mock-user-service", s,
			"mock-user-service must be opt-in only")
	}
}

func TestConfig_ExplicitServicesOverrideDefault(t *testing.T) {
	cfg := Config{Services: []string{"room-service"}}
	assert.Equal(t, []string{"room-service"}, resolveServices(&cfg))
}

func TestConfig_DefaultSiteID(t *testing.T) {
	cfg := Config{}
	assert.Equal(t, "site-local", resolveSiteID(&cfg))
}

func TestServiceImage_FormatsTagSuffix(t *testing.T) {
	assert.Equal(t, "chat-local-services-room-service:latest",
		serviceImage("room-service", "latest"))
	assert.Equal(t, "chat-local-services-auth-service:v1.2.3",
		serviceImage("auth-service", "v1.2.3"))
}

func TestRequiredImages_DerivesFromServices(t *testing.T) {
	got := requiredImages([]string{"room-service", "room-worker"}, "v9")
	assert.Equal(t, []string{
		"chat-local-services-room-service:v9",
		"chat-local-services-room-worker:v9",
	}, got)
}

func TestResolveRepoRoot_ExplicitWins(t *testing.T) {
	got, err := resolveRepoRoot(&Config{RepoRoot: "/explicit/path"})
	require.NoError(t, err)
	assert.Equal(t, "/explicit/path", got)
}

func TestResolveRepoRoot_WalksUpToModuleRoot(t *testing.T) {
	// The test binary runs under tools/integration-suite/internal/infra
	// when `go test` is invoked from that package; the walk-up should
	// stop at the repo root where go.mod has module github.com/hmchangw/chat.
	got, err := resolveRepoRoot(&Config{})
	require.NoError(t, err)
	b, err := os.ReadFile(filepath.Join(got, "go.mod"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "module github.com/hmchangw/chat")
}

func TestTerminateAll_NilStackIsNoOp(t *testing.T) {
	// Defensive: defer stack.TerminateAll where Up returned nil
	// must not panic.
	var s *Stack
	assert.NotPanics(t, func() { s.TerminateAll(context.Background()) })
}

func TestReadiness_EveryDefaultServiceHasAStrategy(t *testing.T) {
	for _, svc := range defaultServices {
		strategy, ok := readinessFor(svc)
		assert.True(t, ok, "%s must have a readiness strategy", svc)
		assert.NotNil(t, strategy, "%s strategy must be non-nil", svc)
	}
}

func TestReadiness_MockUserServiceOptInHasStrategy(t *testing.T) {
	strategy, ok := readinessFor("mock-user-service")
	assert.True(t, ok, "mock-user-service must have a readiness strategy (opt-in)")
	assert.NotNil(t, strategy)
}

func TestReadiness_UnknownServiceReturnsFalse(t *testing.T) {
	_, ok := readinessFor("not-a-real-service")
	assert.False(t, ok)
}

// testServiceEnv calls serviceEnv with the per-site test values used
// by the unit tests. Centralising the defaults here keeps the test
// assertions readable without repeating the per-site URL literals.
func testServiceEnv(svc, siteID string, msgBucketHours int) map[string]string {
	natsURL := "nats://nats-" + siteID + ":4222"
	mongoURI := "mongodb://chat-local-toxiproxy:27017"
	if siteID == "site-b" {
		mongoURI = "mongodb://chat-local-toxiproxy:27018"
	}
	valkeyAddr := "valkey-" + siteID + ":6379"
	return serviceEnv(svc, siteID, "test-signing-key", msgBucketHours, natsURL, mongoURI, valkeyAddr)
}

func TestServiceEnv_RoomServiceMatchesMultiSiteRouting(t *testing.T) {
	for _, tc := range []struct {
		site          string
		wantMongo     string
		wantCassandra string
		wantNATS      string
	}{
		{"site-a", "mongodb://chat-local-toxiproxy:27017", "chat-local-toxiproxy", "nats://chat-local-toxiproxy:4222"},
		{"site-b", "mongodb://chat-local-toxiproxy:27018", "chat-local-toxiproxy:9043", "nats://chat-local-toxiproxy:4223"},
	} {
		tc := tc
		t.Run(tc.site, func(t *testing.T) {
			env := testServiceEnv("room-service", tc.site, 24)
			assert.Equal(t, tc.wantMongo, env["MONGO_URI"])
			assert.Equal(t, tc.wantCassandra, env["CASSANDRA_HOSTS"])
			assert.Equal(t, tc.wantNATS, env["NATS_URL"])
			assert.Equal(t, tc.site, env["SITE_ID"])
			assert.Equal(t, "chat", env["CASSANDRA_KEYSPACE"])
			assert.Equal(t, "true", env["BOOTSTRAP_STREAMS"])
		})
	}
}

func TestServiceEnv_AuthServiceDevModeAndPort(t *testing.T) {
	env := testServiceEnv("auth-service", "site-a", 24)
	assert.Equal(t, "8080", env["PORT"])
	assert.Equal(t, "true", env["DEV_MODE"])
}

func TestServiceEnv_MockUserServiceMinimalNATSOnly(t *testing.T) {
	env := testServiceEnv("mock-user-service", "site-a", 24)
	assert.Equal(t, "nats://chat-local-toxiproxy:4222", env["NATS_URL"])
	assert.Equal(t, "site-a", env["SITE_ID"])
	// mock-user-service has no Mongo / Cassandra / HTTP port.
	assert.NotContains(t, env, "MONGO_URI")
	assert.NotContains(t, env, "CASSANDRA_HOSTS")
}

func TestServiceEnv_EveryDefaultServiceReturnsNonEmptyMap(t *testing.T) {
	for _, svc := range defaultServices {
		env := testServiceEnv(svc, "site-a", 24)
		assert.NotEmpty(t, env, "%s env must be populated", svc)
	}
}

// Regression: room-worker's main.go panics on boot when VALKEY_ADDRS
// is unset (caarlos0/env strict parse). The per-site addr is injected
// by startService from the stack's valkeyAddrBySite map.
func TestServiceEnv_RoomWorkerNeedsValkeyAddrs(t *testing.T) {
	for _, site := range []string{"site-a", "site-b"} {
		env := testServiceEnv("room-worker", site, 24)
		assert.Equal(t, "valkey-"+site+":6379", env["VALKEY_ADDRS"],
			"room-worker requires VALKEY_ADDRS at boot — Phase 3.4 regression")
	}
}

// Regression: history-service and message-worker both read
// MESSAGE_BUCKET_HOURS to compute the messages_by_room partition key.
// The suite seeds rows at the same window; mismatched windows
// silently target different partitions (CLAUDE.md §6). startService
// MUST plumb the suite's bucket-hours value into these two services'
// env so the spawned containers agree with the seed engine.
func TestServiceEnv_MessageBucketHoursSetForBucketAwareServices(t *testing.T) {
	for _, svc := range []string{"history-service", "message-worker"} {
		env := testServiceEnv(svc, "site-a", 24)
		assert.Equal(t, "24", env["MESSAGE_BUCKET_HOURS"],
			"%s must receive MESSAGE_BUCKET_HOURS so its partition math matches the suite's seed engine", svc)
	}
}

// Zero msgBucketHours leaves MESSAGE_BUCKET_HOURS unset so each
// service falls back to its own envDefault — the documented "I don't
// care, use the service default" path callers may want.
func TestServiceEnv_MessageBucketHoursZeroLeavesEnvUnset(t *testing.T) {
	for _, svc := range []string{"history-service", "message-worker"} {
		env := testServiceEnv(svc, "site-a", 0)
		assert.NotContains(t, env, "MESSAGE_BUCKET_HOURS",
			"%s must NOT set MESSAGE_BUCKET_HOURS when caller passes 0", svc)
	}
}

func TestServiceEnv_BootstrapStreamsSetForWorkers(t *testing.T) {
	// Phase 3.5 dropped search-sync-worker from defaultServices, so this
	// list mirrors what infra.Up will actually exercise.
	workers := []string{
		"broadcast-worker", "inbox-worker", "message-gatekeeper",
		"message-worker", "notification-worker", "room-service",
		"room-worker",
	}
	for _, svc := range workers {
		env := testServiceEnv(svc, "site-a", 24)
		assert.Equal(t, "true", env["BOOTSTRAP_STREAMS"],
			"%s must set BOOTSTRAP_STREAMS=true (matches per-service deploy/docker-compose.yml)", svc)
	}
}

// TestCassandraProxyHost verifies that site-a gets the default :9042
// alias and site-b gets :9043.
func TestCassandraProxyHost_PerSite(t *testing.T) {
	assert.Equal(t, "chat-local-toxiproxy", cassandraProxyHost("site-a"))
	assert.Equal(t, "chat-local-toxiproxy:9043", cassandraProxyHost("site-b"))
}

func TestPreflightImages_ReportsMissingWithOperatorHint(t *testing.T) {
	missing := []string{"chat-local-services-room-service:test-missing-xyz"}
	err := reportMissingImages(missing)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "chat-local-services-room-service:test-missing-xyz")
	assert.Contains(t, err.Error(), "make build-test-images")
}

func TestPreflightImages_NoMissingReturnsNil(t *testing.T) {
	assert.NoError(t, reportMissingImages(nil))
	assert.NoError(t, reportMissingImages([]string{}))
}
