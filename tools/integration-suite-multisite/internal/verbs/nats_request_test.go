package verbs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
)

func TestNATSRequest_PicksURLByInputSite(t *testing.T) {
	n := &NATSRequest{
		SiteURLs: map[string]string{
			"site-a": "nats://nonexistent-site-a:4222",
			"site-b": "nats://nonexistent-site-b:4222",
		},
		Timeout: 50 * time.Millisecond,
	}
	in := &Input{
		Site:    "site-a",
		Subject: "x",
		Payload: []byte("{}"),
		// no Credential — connect will fail at "no credential" or
		// at TCP. Either is fine; we're testing URL selection only.
	}
	out := n.Execute(context.Background(), in)
	// The actual connection should fail (no server), but the error
	// must mention site-a's URL, not site-b's.
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "nonexistent-site-a",
		"executor must have tried site-a's URL based on in.Site")
}

func TestNATSRequest_RejectsUnknownSite(t *testing.T) {
	n := &NATSRequest{
		SiteURLs: map[string]string{
			"site-a": "nats://a:4222",
			"site-b": "nats://b:4222",
		},
		Timeout: 50 * time.Millisecond,
	}
	in := &Input{Site: "site-c", Subject: "x"}
	out := n.Execute(context.Background(), in)
	require.Error(t, out.Err)
	assert.Contains(t, out.Err.Error(), "site-c")
}

// TestNATSRequest_MissingCredential_ReturnsErr exercises the early
// validation path. Live NATS isn't required.
func TestNATSRequest_MissingCredential_ReturnsErr(t *testing.T) {
	n := NewNATSRequest(map[string]string{"": "nats://unused"})
	out := n.Execute(context.Background(), &Input{
		Subject: "anywhere",
		Payload: []byte("{}"),
	})
	assert.Error(t, out.Err)
	assert.Nil(t, out.Reply)
}

// TestNATSRequest_PopulatesRequestID_MissingCredential covers the
// shortest-path failure: no JWT/seed → no connection attempted, but
// the Outcome still carries a freshly-generated RequestID for
// correlation against service logs.
func TestNATSRequest_PopulatesRequestID_MissingCredential(t *testing.T) {
	exec := NewNATSRequest(map[string]string{"": "nats://nonexistent.invalid:4222"})
	out := exec.Execute(context.Background(), &Input{
		Subject:    "test.subject",
		Payload:    []byte("{}"),
		Credential: Credential{Account: "test"},
	})

	require.Error(t, out.Err)
	require.NotEmpty(t, out.RequestID, "RequestID must be set even on missing-credential path")
	assert.True(t, idgen.IsValidUUID(out.RequestID), "RequestID %q is not a valid hyphenated UUID", out.RequestID)
	assert.Len(t, out.RequestID, 36, "RequestID must be 36-char hyphenated UUID per CLAUDE.md §3")
}

// TestNATSRequest_PopulatesRequestID_TransportFailure covers the
// connect-failure path: a placeholder credential plus a NATS URL
// nothing listens on. The connection error is returned with the
// RequestID populated.
func TestNATSRequest_PopulatesRequestID_TransportFailure(t *testing.T) {
	exec := &NATSRequest{
		SiteURLs: map[string]string{"": "nats://127.0.0.1:1"},
		Timeout:  200 * time.Millisecond,
	}
	out := exec.Execute(context.Background(), &Input{
		Subject:    "test.subject",
		Payload:    []byte("{}"),
		Credential: Credential{Account: "test", JWT: "x", NkeySeed: "x"},
	})

	require.Error(t, out.Err)
	require.NotEmpty(t, out.RequestID)
	assert.True(t, idgen.IsValidUUID(out.RequestID), "RequestID %q not a valid UUID", out.RequestID)
}

// TestNATSRequest_UniqueIDPerCall confirms the executor generates a
// fresh ID each call rather than reusing a cached one.
func TestNATSRequest_UniqueIDPerCall(t *testing.T) {
	exec := NewNATSRequest(map[string]string{"": "nats://nonexistent.invalid:4222"})
	seen := map[string]struct{}{}
	for i := 0; i < 5; i++ {
		out := exec.Execute(context.Background(), &Input{
			Subject:    "t",
			Payload:    []byte("{}"),
			Credential: Credential{Account: "test"},
		})
		require.NotEmpty(t, out.RequestID)
		if _, dup := seen[out.RequestID]; dup {
			t.Fatalf("duplicate RequestID across calls: %q", out.RequestID)
		}
		seen[out.RequestID] = struct{}{}
	}
}

func TestRegistry_GetMissingReturnsErr(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("nonexistent")
	assert.Error(t, err)
}

func TestRegistry_RegisterThenGet(t *testing.T) {
	r := NewRegistry()
	n := NewNATSRequest(map[string]string{"site-a": "nats://example"})
	r.Register("NATSRequestExecutor", n)
	got, err := r.Get("NATSRequestExecutor")
	assert.NoError(t, err)
	assert.Same(t, n, got)
}
