package infra

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"

	"github.com/testcontainers/testcontainers-go"
)

// startService boots one microservice container on the shared network.
// `tag` resolves the image (chat-local-services-<svc>:<tag>); `siteID`
// is plumbed to every service's SITE_ID env. `repoRoot` is the path the
// trust-chain files (backend.creds + .env) live under — required for
// every service including auth-service (which needs AUTH_SIGNING_KEY
// piped in via the authSigningKey arg). `msgBucketHours` is plumbed
// into MESSAGE_BUCKET_HOURS for services that read/write the bucketed
// message tables (history-service, message-worker) so suite seeds and
// service reads target the same partition.
//
// Readiness comes from readinessFor; unknown services return an error
// so a typo in cfg.Services doesn't silently start a container that
// never satisfies a wait strategy.
func startService(ctx context.Context, networkName, svc, tag, siteID, repoRoot, authSigningKey string, msgBucketHours int) (testcontainers.Container, error) {
	strategy, ok := readinessFor(svc)
	if !ok {
		return nil, fmt.Errorf("startService: unknown service %q", svc)
	}
	if repoRoot == "" {
		return nil, fmt.Errorf("startService %s: repoRoot required for trust-chain mounts", svc)
	}
	req := testcontainers.ContainerRequest{
		Image:    serviceImage(svc, tag),
		Networks: []string{networkName},
		NetworkAliases: map[string][]string{
			networkName: {svc},
		},
		Env:        serviceEnv(svc, siteID, authSigningKey, msgBucketHours),
		WaitingFor: strategy,
	}
	// auth-service is the only HTTP service we expose host-side.
	if svc == "auth-service" {
		req.ExposedPorts = []string{"8080/tcp"}
	}
	// Every service except auth-service mounts the NATS creds file —
	// auth-service uses AUTH_SIGNING_KEY (env-injected, no file).
	if svc != "auth-service" {
		req.Files = []testcontainers.ContainerFile{
			{
				HostFilePath:      filepath.Join(repoRoot, "docker-local", "backend.creds"),
				ContainerFilePath: "/etc/nats/backend.creds",
				// 0o444 (world-readable) — the service runtime in every
				// microservice Dockerfile runs as a non-root user
				// (UID 10001 alpine / UID 65532 distroless). 0o400 makes
				// the mounted file owned by root and unreadable by the
				// service process; the chatapp account is the only
				// principal embedded in the JWT inside, so weakening the
				// mode to 0o444 doesn't expose anything outside the
				// container that the container itself can't already see.
				FileMode: 0o444,
			},
		}
	}
	return testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
}

// serviceEnv returns the environment map for the named service.
// Routing values mirror docker-local/compose.services.yaml's override
// block — every Mongo URI and Cassandra host points at
// chat-local-toxiproxy on the shared network.
//
// `authSigningKey` is the chatapp account nkey seed parsed out of
// docker-local/.env. It's only consumed by auth-service (which uses it
// to sign user JWTs); for every other service it's ignored.
//
// `msgBucketHours` is set on services that read or write the bucketed
// message tables (history-service, message-worker). Must match the
// suite's seed-engine bucket window or partition-keyed reads silently
// miss the seeded rows. Suite default is 24h (CLAUDE.md §6); a
// zero value here means "let the service use its own envDefault" so
// callers that don't care don't have to plumb the value.
//
// Bootstrap flags (BOOTSTRAP_STREAMS) match the per-service
// deploy/docker-compose.yml convention exactly so the runner sees
// the same JetStream stream set it does today.
//
// Unknown services return an empty map. The caller (startService) is
// responsible for treating that as a programming error.
func serviceEnv(svc, siteID, authSigningKey string, msgBucketHours int) map[string]string {
	common := map[string]string{
		"SITE_ID":         siteID,
		"NATS_URL":        "nats://nats:4222",
		"NATS_CREDS_FILE": "/etc/nats/backend.creds",
		"MONGO_URI":       "mongodb://chat-local-toxiproxy:27017",
		"MONGO_DB":        "chat",
	}
	merge := func(extras map[string]string) map[string]string {
		out := make(map[string]string, len(common)+len(extras))
		for k, v := range common {
			out[k] = v
		}
		for k, v := range extras {
			out[k] = v
		}
		return out
	}

	switch svc {
	case "auth-service":
		// auth-service is HTTP-only: no NATS, no Mongo, no Cassandra.
		// AUTH_SIGNING_KEY is the chatapp account nkey seed; without it
		// auth-service panics on startup. Sourced from docker-local/.env
		// by loadAuthSigningKey() in stack.go.
		return map[string]string{
			"PORT":             "8080",
			"DEV_MODE":         "true",
			"NATS_JWT_EXPIRY":  "2h",
			"OIDC_ISSUER_URL":  "http://keycloak:8080/realms/chatapp",
			"OIDC_AUDIENCES":   "nats-chat",
			"TLS_SKIP_VERIFY":  "false",
			"AUTH_SIGNING_KEY": authSigningKey,
		}
	case "broadcast-worker":
		return merge(map[string]string{
			"BOOTSTRAP_STREAMS": "true",
		})
	case "history-service":
		extras := map[string]string{
			"CASSANDRA_HOSTS":    "chat-local-toxiproxy",
			"CASSANDRA_KEYSPACE": "chat",
		}
		if msgBucketHours > 0 {
			extras["MESSAGE_BUCKET_HOURS"] = strconv.Itoa(msgBucketHours)
		}
		return merge(extras)
	case "inbox-worker":
		return merge(map[string]string{
			"BOOTSTRAP_STREAMS": "true",
		})
	case "message-gatekeeper":
		return merge(map[string]string{
			"BOOTSTRAP_STREAMS": "true",
		})
	case "message-worker":
		extras := map[string]string{
			"CASSANDRA_HOSTS":    "chat-local-toxiproxy",
			"CASSANDRA_KEYSPACE": "chat",
			"BOOTSTRAP_STREAMS":  "true",
		}
		if msgBucketHours > 0 {
			extras["MESSAGE_BUCKET_HOURS"] = strconv.Itoa(msgBucketHours)
		}
		return merge(extras)
	case "notification-worker":
		return merge(map[string]string{
			"BOOTSTRAP_STREAMS": "true",
		})
	case "room-service":
		return merge(map[string]string{
			"MAX_ROOM_SIZE":           "1000",
			"MAX_BATCH_SIZE":          "500",
			"VALKEY_ADDRS":            "valkey:6379",
			"VALKEY_KEY_GRACE_PERIOD": "24h",
			"CASSANDRA_HOSTS":         "chat-local-toxiproxy",
			"CASSANDRA_KEYSPACE":      "chat",
			"BOOTSTRAP_STREAMS":       "true",
		})
	case "room-worker":
		// VALKEY_ADDRS is strictly required by room-worker's boot-time
		// config parser (room-worker/main.go via caarlos0/env) — without
		// it the process panics before NATS connect. Mirror room-service's
		// addr value so both consume the same valkey alias on the shared
		// network.
		return merge(map[string]string{
			"BOOTSTRAP_STREAMS": "true",
			"VALKEY_ADDRS":      "valkey:6379",
		})
	case "mock-user-service":
		// NATS-only; no Mongo/Cassandra/HTTP. Confirmed against
		// mock-user-service/deploy/docker-compose.yml.
		return map[string]string{
			"SITE_ID":         siteID,
			"NATS_URL":        "nats://nats:4222",
			"NATS_CREDS_FILE": "/etc/nats/backend.creds",
		}
	}
	return map[string]string{}
}
