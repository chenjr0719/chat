package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

func TestMatchShape_SingleEventMatchingExpectedMatches(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok, "subset shape match must succeed")
}

func TestMatchShape_ZeroEventsFailsWithPolledZero(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	ok, err := m.Match([]readers.Event{})
	require.NoError(t, err)
	require.False(t, ok)
	msg := m.FailureMessage([]readers.Event{})
	assert.Contains(t, msg, "events polled:     0")
}

func TestMatchShape_MultipleEventsOneMatches(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
		{Location: "reply", Payload: map[string]any{"foo": "bar"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMatchShape_MultipleEventsNoneMatchFailureNamesClosest(t *testing.T) {
	reg := matchers.NewRegistry()
	m := MatchShape(map[string]any{"status": "accepted"}, reg)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "rejected"}},
		{Location: "reply", Payload: map[string]any{"foo": "bar"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	require.False(t, ok)
	msg := m.FailureMessage(events)
	assert.Contains(t, msg, "events polled:     2")
	// Must surface at least one of the mismatch reasons so the operator
	// can triage which assertion failed.
	assert.Contains(t, msg, "status")
	// And must surface the actual reply payload verbatim, separate from
	// the tool's framing.
	assert.Contains(t, msg, "reply from system:")
}

func TestMatchShape_NilRegistryFallsBackToBuiltins(t *testing.T) {
	// Passing nil reg should default to the built-in matchers.NewRegistry
	// so callers can use MatchShape inline without wiring a registry.
	m := MatchShape(map[string]any{"status": "accepted"}, nil)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted"}},
	}
	ok, err := m.Match(events)
	require.NoError(t, err)
	assert.True(t, ok)
}

func TestMatchShape_ActualMustBeEventSlice(t *testing.T) {
	m := MatchShape(map[string]any{"x": 1}, nil)
	_, err := m.Match("not an event slice")
	require.Error(t, err)
}

func TestMatchShape_NegatedFailureMessageNamesMatchingEvent(t *testing.T) {
	// For Consistently().ShouldNot(...) — when an event DOES match,
	// the negated message must point at the matching payload so the
	// operator sees why the "must not happen" assertion fired.
	m := MatchShape(map[string]any{"status": "accepted"}, nil)
	events := []readers.Event{
		{Location: "reply", Payload: map[string]any{"status": "accepted", "roomId": "r1"}},
	}
	// Drive Match first so the matcher captures matchedIdx for the
	// negated message — mirrors how Gomega itself sequences Match →
	// NegatedFailureMessage on a Consistently().ShouldNot(...) fail.
	_, _ = m.Match(events)
	msg := m.NegatedFailureMessage(events)
	assert.Contains(t, msg, "expected no event to match")
	assert.Contains(t, msg, "r1", "must surface the matching payload for triage")
}
