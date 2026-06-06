package pollers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// Phase 4.0 universal primitives: each one registers unconditionally
// so YAML-side resolution always succeeds. Backend availability is
// checked at PollFn time, not at registration.

func TestRegisterBuiltinPollers_AllFivePrimitivesRegistered(t *testing.T) {
	r := NewRegistry()
	deps := BuiltinDeps{
		ReplyReader: readers.NewNATSReplyReader(),
		StartTime:   time.Now(),
		// All other deps nil — universal primitives register anyway.
	}
	cleanup, err := RegisterBuiltinPollers(r, &deps)
	require.NoError(t, err)
	defer cleanup()

	for _, loc := range []string{"mongo_find", "cassandra_select", "jetstream_consume", "logs_tail", "reply"} {
		p, err := r.Get(loc)
		require.NoError(t, err, "expected %s registered", loc)
		assert.NotNil(t, p)
	}
}

func TestRegisterBuiltinPollers_NoLegacyLocationsLeak(t *testing.T) {
	// Phase 4.0 negative: the legacy table-bound location names (from
	// Phase 3.9) must NOT be registered any more. Authors who haven't
	// migrated their scenarios get a clean "unknown location" error
	// pointing at the new primitives.
	r := NewRegistry()
	deps := BuiltinDeps{
		ReplyReader: readers.NewNATSReplyReader(),
		StartTime:   time.Now(),
	}
	cleanup, err := RegisterBuiltinPollers(r, &deps)
	require.NoError(t, err)
	defer cleanup()

	for _, legacyLoc := range []string{
		"mongo.rooms",
		"cassandra.messages_by_room",
		"jetstream.rooms-canonical",
		"jetstream.messages-canonical",
		"logs.room-service",
		"logs.room-worker",
		"logs.message-worker",
		"logs.message-gatekeeper",
	} {
		_, err := r.Get(legacyLoc)
		require.Error(t, err, "legacy location %q must NOT be registered (Phase 4.0)", legacyLoc)
	}
}

func TestRegisterBuiltinPollers_ReplyMissingIsNotFatal(t *testing.T) {
	// If a test passes nil ReplyReader, the other four primitives must
	// still come up so the rest of the assertion surface stays usable.
	r := NewRegistry()
	deps := BuiltinDeps{StartTime: time.Now()}
	cleanup, err := RegisterBuiltinPollers(r, &deps)
	require.NoError(t, err)
	defer cleanup()

	_, err = r.Get("reply")
	require.Error(t, err, "reply skipped when ReplyReader is nil")

	for _, loc := range []string{"mongo_find", "cassandra_select", "jetstream_consume", "logs_tail"} {
		_, err := r.Get(loc)
		require.NoError(t, err, "%s must register even when ReplyReader is nil", loc)
	}
}

func TestRegistryCleanup_TerminatesStatefulPollersIdempotent(t *testing.T) {
	r := NewRegistry()
	deps := BuiltinDeps{
		ReplyReader: readers.NewNATSReplyReader(),
		StartTime:   time.Now(),
	}
	cleanup, err := RegisterBuiltinPollers(r, &deps)
	require.NoError(t, err)
	// Cleanup must not panic and must be safe to call twice.
	cleanup()
	cleanup()
}
