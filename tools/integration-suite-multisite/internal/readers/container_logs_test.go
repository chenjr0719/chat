package readers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNewContainerLogsReaderWithRules_EmptyLists confirms the baseline
// behavior preserved by §4.6.0: with no events and no ignore patterns,
// classify returns (EventUnmatched, true) for any line.
func TestNewContainerLogsReaderWithRules_EmptyLists(t *testing.T) {
	r, err := NewContainerLogsReaderWithRules("c", "svc", "loc", nil, nil)
	require.NoError(t, err)
	typ, emit := r.classify(`{"msg":"anything"}`)
	assert.True(t, emit)
	assert.Equal(t, EventUnmatched, typ)
}

// TestNewContainerLogsReaderWithRules_Classify drives the matching
// algorithm with table-driven cases covering every rule documented in
// §4.6.0: ignore-first-then-events, first-match-wins, unmatched
// fallthrough.
func TestNewContainerLogsReaderWithRules_Classify(t *testing.T) {
	events := []ContainerLogEventRule{
		{Pattern: `connection refused`, Type: EventDisconnectNoise},
		{Pattern: `starting `, Type: EventRestartNoise},
		{Pattern: `level":"ERROR`, Type: EventFailure},
		{Pattern: `room (created|persisted)`, Type: EventCascade},
	}
	ignore := []string{
		`GET /healthz`,
		`prometheus_exporter`,
	}
	r, err := NewContainerLogsReaderWithRules("c", "svc", "loc", events, ignore)
	require.NoError(t, err)

	cases := []struct {
		name     string
		line     string
		wantEmit bool
		wantType EventType
	}{
		{"ignore drops healthz", `{"msg":"GET /healthz 200"}`, false, ""},
		{"ignore drops prometheus", `{"msg":"prometheus_exporter scrape"}`, false, ""},
		{"events first match wins (disconnect before error)", `{"level":"ERROR","msg":"connection refused"}`, true, EventDisconnectNoise},
		{"events match restart", `{"msg":"starting room-service v1.2"}`, true, EventRestartNoise},
		{"events match failure", `{"level":"ERROR","msg":"unexpected panic"}`, true, EventFailure},
		{"events match cascade", `{"msg":"room created","roomId":"r1"}`, true, EventCascade},
		{"events match cascade alt", `{"msg":"room persisted"}`, true, EventCascade},
		{"no match falls through to unmatched", `{"msg":"some random chatter"}`, true, EventUnmatched},
		{"ignore wins over events (line matches both)", `{"msg":"prometheus_exporter starting up"}`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ, emit := r.classify(tc.line)
			assert.Equal(t, tc.wantEmit, emit, "line=%s", tc.line)
			if tc.wantEmit {
				assert.Equal(t, tc.wantType, typ, "line=%s", tc.line)
			}
		})
	}
}

// TestNewContainerLogsReaderWithRules_InvalidEventRegex confirms the
// reader fails construction when an events[].pattern is not a valid
// regex. The returned error MUST contain the pattern and the verbatim
// regexp compile error so the engineer can fix the YAML.
func TestNewContainerLogsReaderWithRules_InvalidEventRegex(t *testing.T) {
	events := []ContainerLogEventRule{
		{Pattern: `[unterminated`, Type: EventFailure},
	}
	r, err := NewContainerLogsReaderWithRules("c", "svc", "loc", events, nil)
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), `[unterminated`)
	// regexp.Compile reports "error parsing regexp" — verbatim wrapping.
	assert.Contains(t, err.Error(), "error parsing regexp")
}

// TestNewContainerLogsReaderWithRules_InvalidIgnoreRegex mirrors the
// invalid-events case for the ignore list.
func TestNewContainerLogsReaderWithRules_InvalidIgnoreRegex(t *testing.T) {
	r, err := NewContainerLogsReaderWithRules("c", "svc", "loc", nil, []string{`(unbalanced`})
	require.Error(t, err)
	assert.Nil(t, r)
	assert.Contains(t, err.Error(), `(unbalanced`)
	assert.Contains(t, err.Error(), "error parsing regexp")
}

// TestNewContainerLogsReader_DefaultsToEmptyRules confirms the legacy
// no-rules constructor preserves the baseline EventUnmatched behavior
// and never errors (empty pattern lists cannot fail to compile).
func TestNewContainerLogsReader_DefaultsToEmptyRules(t *testing.T) {
	r := NewContainerLogsReader("c", "svc", "loc")
	typ, emit := r.classify(`{"msg":"hello"}`)
	assert.True(t, emit)
	assert.Equal(t, EventUnmatched, typ)
}

// TestContainerLogEventRule_OrderDeterministic explicitly verifies
// first-match-wins with two overlapping patterns — swapping their
// declaration order swaps the assigned Type.
func TestContainerLogEventRule_OrderDeterministic(t *testing.T) {
	line := `{"msg":"connection refused while restarting"}`

	r1, err := NewContainerLogsReaderWithRules("c", "svc", "loc",
		[]ContainerLogEventRule{
			{Pattern: `connection refused`, Type: EventDisconnectNoise},
			{Pattern: `restarting`, Type: EventRestartNoise},
		}, nil)
	require.NoError(t, err)
	typ1, _ := r1.classify(line)
	assert.Equal(t, EventDisconnectNoise, typ1)

	r2, err := NewContainerLogsReaderWithRules("c", "svc", "loc",
		[]ContainerLogEventRule{
			{Pattern: `restarting`, Type: EventRestartNoise},
			{Pattern: `connection refused`, Type: EventDisconnectNoise},
		}, nil)
	require.NoError(t, err)
	typ2, _ := r2.classify(line)
	assert.Equal(t, EventRestartNoise, typ2)
}
