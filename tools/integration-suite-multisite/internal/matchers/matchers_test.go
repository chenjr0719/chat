package matchers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMatchesShape(t *testing.T) {
	observed := map[string]any{"name": "general", "owner": "alice", "extra": "ignored"}
	r := MatchesShape{}.Match(observed, map[string]any{"name": "general", "owner": "alice"})
	assert.True(t, r.Matched, r.Reason)

	r2 := MatchesShape{}.Match(observed, map[string]any{"name": "different"})
	assert.False(t, r2.Matched)
	assert.Contains(t, r2.Reason, "name")
}

// TestMatchesShape_StructFallThrough covers the typed-payload case
// (e.g. readers.ReplyPayload). The matcher marshals the struct to JSON
// and unmarshals as map[string]any so scenarios can assert on field
// names without callers pre-flattening.
func TestMatchesShape_StructFallThrough(t *testing.T) {
	// Mimics readers.ReplyPayload's shape via json tags — kept local
	// to avoid an internal/readers import cycle for a test.
	type replyPayload struct {
		BodyJSON  map[string]any `json:"body_json,omitempty"`
		Error     string         `json:"error,omitempty"`
		LatencyMs int64          `json:"latency_ms"`
	}
	observed := replyPayload{
		BodyJSON:  map[string]any{"status": "accepted", "roomType": "channel"},
		LatencyMs: 12,
	}

	// Happy path — assert on nested body_json fields
	r := MatchesShape{}.Match(observed, map[string]any{
		"body_json": map[string]any{"status": "accepted", "roomType": "channel"},
	})
	assert.True(t, r.Matched, r.Reason)

	// Negative path — transport-error assertion against a struct with Error set
	failed := replyPayload{Error: "nats_request: request: nats: no responders available for request"}
	r2 := MatchesShape{}.Match(failed, map[string]any{
		"error": "nats_request: request: nats: no responders available for request",
	})
	assert.True(t, r2.Matched, r2.Reason)
}

// TestMatchesShape_NestedMapSubset is the regression for the bug that
// failed every positive scenario in the 2026-05-24 first run: the
// matcher previously did fmt.Sprintf("%v", ...) equality on nested
// maps, demanding full key-set equality at every level. The fix
// recurses into nested maps with subset semantics.
func TestMatchesShape_NestedMapSubset(t *testing.T) {
	// Observed = full CreateRoomReply after JSON-marshal of ReplyPayload.
	observed := map[string]any{
		"body_json": map[string]any{
			"status":   "accepted",
			"roomId":   "ENlpaBNmxd6Pf0ZgM",
			"roomType": "channel",
		},
		"latency_ms": 242,
		"header":     map[string]any{"X-Request-Id": []any{"01970…"}},
	}
	// Scenario expects only the two semantically meaningful fields.
	want := map[string]any{
		"body_json": map[string]any{
			"status":   "accepted",
			"roomType": "channel",
		},
	}
	r := MatchesShape{}.Match(observed, want)
	assert.True(t, r.Matched, "nested-map subset match should pass, got reason: %s", r.Reason)
}

func TestMatchesShape_NestedMapMissingField(t *testing.T) {
	observed := map[string]any{"body_json": map[string]any{"status": "accepted"}}
	want := map[string]any{"body_json": map[string]any{"status": "accepted", "roomType": "channel"}}
	r := MatchesShape{}.Match(observed, want)
	assert.False(t, r.Matched)
	assert.Contains(t, r.Reason, "body_json.roomType")
	assert.Contains(t, r.Reason, "missing")
}

func TestMatchesShape_NestedMapWrongValue(t *testing.T) {
	observed := map[string]any{"body_json": map[string]any{"status": "rejected"}}
	want := map[string]any{"body_json": map[string]any{"status": "accepted"}}
	r := MatchesShape{}.Match(observed, want)
	assert.False(t, r.Matched)
	assert.Contains(t, r.Reason, "body_json.status")
}

// TestMatchesShape_NestedMapButObservedScalar guards against a quietly
// failing match when the expected expects a nested object but the
// observed field is a scalar at that path.
func TestMatchesShape_NestedMapButObservedScalar(t *testing.T) {
	observed := map[string]any{"body_json": "not a map"}
	want := map[string]any{"body_json": map[string]any{"status": "accepted"}}
	r := MatchesShape{}.Match(observed, want)
	assert.False(t, r.Matched)
	assert.Contains(t, r.Reason, "body_json")
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	m, err := r.Get("matches_shape")
	assert.NoError(t, err)
	assert.NotNil(t, m)
	_, err = r.Get("unknown")
	assert.Error(t, err)
}
