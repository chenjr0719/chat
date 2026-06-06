package seedeffect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeAuthService returns a canned natsJwt for any /auth POST. The
// returned URL is fed into MintNATSIdentity / Deps.AuthURL.
func fakeAuthService(t *testing.T, returnJWT string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth" || r.Method != http.MethodPost {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"natsJwt": returnJWT})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// Phase 3.8 split: cred-minting lives in MintNATSIdentity, which the
// Sandbox now calls unconditionally for every seeded user. These tests
// pin the new helper's contract.

func TestMintNATSIdentity_ReturnsJWTAndNkey(t *testing.T) {
	authURL := fakeAuthService(t, "eyJhbGciOi-fake-jwt-string")

	jwt, nkey, err := MintNATSIdentity(context.Background(), "alice", authURL)
	require.NoError(t, err)
	assert.Equal(t, "eyJhbGciOi-fake-jwt-string", jwt)
	assert.True(t, strings.HasPrefix(nkey, "SU"), "nkey user seeds start with SU; got %q", nkey[:4])
}

func TestMintNATSIdentity_AuthServiceUnreachable(t *testing.T) {
	_, _, err := MintNATSIdentity(context.Background(), "alice", "http://127.0.0.1:1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "auth-service")
}

func TestMintNATSIdentity_EmptyJWTFromAuthFails(t *testing.T) {
	authURL := fakeAuthService(t, "")
	_, _, err := MintNATSIdentity(context.Background(), "alice", authURL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty natsJwt")
}

func TestMintNATSIdentity_EmptyAuthURLRejected(t *testing.T) {
	_, _, err := MintNATSIdentity(context.Background(), "alice", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authURL is required")
}

// VerifiedEffect's new (Phase 3.8) responsibility: set u.Verified=true.
// The credential mint that used to live here is gone — the unconditional
// Sandbox.Setup call to MintNATSIdentity covers that now.
func TestVerifiedEffect_SetsVerifiedFlag(t *testing.T) {
	u := &SeedUser{Alias: "alice", Account: "alice", ID: "u-alice"}
	require.False(t, u.Verified, "starts as zero value")

	e := &VerifiedEffect{}
	require.NoError(t, e.Apply(context.Background(), u, Deps{}))

	assert.True(t, u.Verified, "VerifiedEffect.Apply must set Verified=true")
	assert.Empty(t, u.JWT, "credentials are minted by Sandbox.Setup, NOT by VerifiedEffect")
	assert.Empty(t, u.NkeySeed, "credentials are minted by Sandbox.Setup, NOT by VerifiedEffect")
}
