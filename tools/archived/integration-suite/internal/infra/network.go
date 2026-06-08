package infra

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"strings"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
)

// createNetwork allocates an ephemeral Docker network named
// "infra-<runID>" and returns (network, runID, error). The runID is
// 8 chars of lowercase base32 (no padding) — short enough to fit in
// Docker's 64-char network-name limit, random enough to avoid
// collisions across parallel test runs on the same daemon.
func createNetwork(ctx context.Context) (*testcontainers.DockerNetwork, string, error) {
	runID, err := newRunID()
	if err != nil {
		return nil, "", fmt.Errorf("generate run ID: %w", err)
	}
	nw, err := network.New(ctx,
		network.WithLabels(map[string]string{
			"owner":  "integration-suite",
			"run-id": runID,
		}),
	)
	if err != nil {
		return nil, "", fmt.Errorf("create docker network: %w", err)
	}
	return nw, runID, nil
}

// newRunID returns 8 chars of lowercase base32 (alphabet a-z + 2-7).
// Stripped of base32's "=" padding via the no-padding encoding.
func newRunID() (string, error) {
	var b [5]byte // 5 bytes → 8 base32 chars
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:])), nil
}
