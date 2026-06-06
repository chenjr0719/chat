package verbs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/pkg/idgen"
)

func TestJetStreamPublish_MissingCredential_ReturnsErr(t *testing.T) {
	n := NewJetStreamPublish(map[string]string{"": "nats://unused"})
	out := n.Execute(context.Background(), &Input{
		Subject: "anywhere",
		Payload: []byte("{}"),
	})
	assert.Error(t, out.Err)
	assert.Nil(t, out.Reply)
}

// TestCredentialAuthOpt covers the user-level vs service-level
// credential branch in credentialAuthOpt.
func TestCredentialAuthOpt(t *testing.T) {
	t.Run("user-level (JWT+seed)", func(t *testing.T) {
		opt, err := credentialAuthOpt(Credential{Account: "alice", JWT: "x", NkeySeed: "y"})
		require.NoError(t, err)
		assert.NotNil(t, opt)
	})
	t.Run("service-level (CredsFile)", func(t *testing.T) {
		opt, err := credentialAuthOpt(Credential{Account: "backend", CredsFile: "/some/path"})
		require.NoError(t, err)
		assert.NotNil(t, opt)
	})
	t.Run("CredsFile wins over JWT+seed when both set", func(t *testing.T) {
		// Documents the precedence — order in credentialAuthOpt picks
		// CredsFile first. A real scenario would only set one.
		opt, err := credentialAuthOpt(Credential{Account: "x", JWT: "j", NkeySeed: "s", CredsFile: "/p"})
		require.NoError(t, err)
		assert.NotNil(t, opt)
	})
	t.Run("missing both → error", func(t *testing.T) {
		_, err := credentialAuthOpt(Credential{Account: "anonymous"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "missing credential")
	})
}

func TestJetStreamPublish_PopulatesRequestID_MissingCredential(t *testing.T) {
	exec := NewJetStreamPublish(map[string]string{"": "nats://nonexistent.invalid:4222"})
	out := exec.Execute(context.Background(), &Input{
		Subject:    "test.subject",
		Payload:    []byte("{}"),
		Credential: Credential{Account: "test"},
	})

	require.Error(t, out.Err)
	require.NotEmpty(t, out.RequestID)
	assert.True(t, idgen.IsValidUUID(out.RequestID), "RequestID %q not a valid UUID", out.RequestID)
}

func TestJetStreamPublish_PopulatesRequestID_TransportFailure(t *testing.T) {
	exec := &JetStreamPublish{
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
	assert.True(t, idgen.IsValidUUID(out.RequestID))
}

func TestJetStreamPublish_UniqueIDPerCall(t *testing.T) {
	exec := NewJetStreamPublish(map[string]string{"": "nats://nonexistent.invalid:4222"})
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
