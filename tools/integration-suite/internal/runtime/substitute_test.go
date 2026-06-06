package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestCtx() Context {
	return Context{
		Site: "site-local",
		Placeholders: map[string]map[string]any{
			"requester": {
				"id":          "u-alice",
				"account":     "alice",
				"engName":     "Alice",
				"chineseName": "爱丽丝",
				"siteId":      "site-local",
			},
			"other": {
				"id":      "u-bob",
				"account": "bob",
			},
		},
		Input: InputSnapshot{
			Subject: "chat.user.alice.request.room.site-local.create",
			Payload: map[string]any{
				"name":  "it-r-xyz-room-auto-abc",
				"users": []any{"bob"},
				"count": 2,
			},
			RequestID: "01970000-0000-7000-8000-000000000001",
		},
	}
}

func TestSubstitute_SiteToken(t *testing.T) {
	got, err := Substitute("${site}", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "site-local", got)
}

func TestSubstitute_PlaceholderField(t *testing.T) {
	got, err := Substitute("${requester.id}", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "u-alice", got)
}

func TestSubstitute_MixedStringAndToken(t *testing.T) {
	got, err := Substitute("from ${requester.account} to ${other.account}", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "from alice to bob", got)
}

func TestSubstitute_InputSubject(t *testing.T) {
	got, err := Substitute("${input.subject}", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "chat.user.alice.request.room.site-local.create", got)
}

func TestSubstitute_InputPayloadKey(t *testing.T) {
	got, err := Substitute("${input.payload.name}", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "it-r-xyz-room-auto-abc", got)
}

func TestSubstitute_InputRequestID(t *testing.T) {
	got, err := Substitute("${input.requestId}", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "01970000-0000-7000-8000-000000000001", got)
}

func TestSubstitute_NestedMap(t *testing.T) {
	in := map[string]any{
		"createdBy": "${requester.id}",
		"type":      "channel",
		"siteId":    "${requester.siteId}",
	}
	got, err := Substitute(in, newTestCtx())
	require.NoError(t, err)
	want := map[string]any{
		"createdBy": "u-alice",
		"type":      "channel",
		"siteId":    "site-local",
	}
	assert.Equal(t, want, got)
}

func TestSubstitute_TypeFidelity_IntStaysInt(t *testing.T) {
	in := map[string]any{
		"count":   2,                        // literal int, no substitution
		"asToken": "${input.payload.count}", // resolves to native int(2)
	}
	got, err := Substitute(in, newTestCtx())
	require.NoError(t, err)
	gotMap := got.(map[string]any)
	assert.Equal(t, 2, gotMap["count"], "literal int should pass through unchanged")
	assert.Equal(t, 2, gotMap["asToken"], "whole-string token resolving to int should keep native int type, not stringify")
}

func TestSubstitute_TypeFidelity_BoolStaysBool(t *testing.T) {
	in := map[string]any{"flag": true}
	got, err := Substitute(in, newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, true, got.(map[string]any)["flag"])
}

func TestSubstitute_SliceRecurses(t *testing.T) {
	in := []any{"${requester.account}", "literal", "${other.account}"}
	got, err := Substitute(in, newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, []any{"alice", "literal", "bob"}, got)
}

func TestSubstitute_NoTokenIsPassthrough(t *testing.T) {
	got, err := Substitute("plain string", newTestCtx())
	require.NoError(t, err)
	assert.Equal(t, "plain string", got)
}

func TestSubstitute_UnknownPlaceholder_HardError(t *testing.T) {
	_, err := Substitute("${unknown.field}", newTestCtx())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown.field")
	assert.Contains(t, err.Error(), "resolved placeholders")
}

func TestSubstitute_UnknownPlaceholderField_HardError(t *testing.T) {
	_, err := Substitute("${requester.nonexistent}", newTestCtx())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requester")
	assert.Contains(t, err.Error(), "nonexistent")
	assert.Contains(t, err.Error(), "available")
	// Available fields list must include known field names
	assert.True(t, strings.Contains(err.Error(), "account") || strings.Contains(err.Error(), "id"),
		"error message should list available fields; got %q", err.Error())
}

func TestSubstitute_PlaceholderWithoutSubfield_HardError(t *testing.T) {
	_, err := Substitute("${requester}", newTestCtx())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a subfield")
}

func TestSubstitute_SiteWithSubfield_HardError(t *testing.T) {
	_, err := Substitute("${site.foo}", newTestCtx())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "site")
	assert.Contains(t, err.Error(), "no subfields")
}

func TestSubstitute_InputUnknownKey_HardError(t *testing.T) {
	_, err := Substitute("${input.bogus}", newTestCtx())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "input")
	assert.Contains(t, err.Error(), "available")
}

func TestSubstitute_NowReturnsInt64Millis(t *testing.T) {
	before := time.Now().UTC().UnixMilli()
	got, err := Substitute("${now}", newTestCtx())
	require.NoError(t, err)
	after := time.Now().UTC().UnixMilli()
	n, ok := got.(int64)
	require.True(t, ok, "${now} should return int64, got %T (%v)", got, got)
	assert.GreaterOrEqual(t, n, before)
	assert.LessOrEqual(t, n, after)
}

func TestSubstitute_NowWithSubfield_HardError(t *testing.T) {
	_, err := Substitute("${now.foo}", newTestCtx())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no subfields")
}

func TestSubstitute_Auto_StillWorks(t *testing.T) {
	SetRunID("testrun")
	got, err := Substitute("$auto", newTestCtx())
	require.NoError(t, err)
	gotStr := got.(string)
	assert.True(t, strings.HasPrefix(gotStr, "it-testrun-room-auto-"), "auto value should follow the runID-prefixed convention, got %q", gotStr)
}

// TestSubstitute_Auto_UniquePerCall regresses the collision bug found
// in the 2026-05-24 end-to-end run: two $auto resolutions in the same
// run used to share the calendar-month prefix because the suffix was
// time.Now().String()[:8] ("2026-05-"). Now each call increments an
// atomic counter, so suffixes diverge.
func TestSubstitute_Auto_UniquePerCall(t *testing.T) {
	SetRunID("testrun")
	seen := map[string]struct{}{}
	for i := 0; i < 10; i++ {
		got, err := Substitute("$auto", newTestCtx())
		require.NoError(t, err)
		s := got.(string)
		if _, dup := seen[s]; dup {
			t.Fatalf("$auto produced duplicate value %q on iteration %d", s, i)
		}
		seen[s] = struct{}{}
	}
}

func TestSubstitute_ScalarsPassThrough(t *testing.T) {
	for _, tc := range []any{1, 1.5, true, false, nil} {
		got, err := Substitute(tc, newTestCtx())
		require.NoError(t, err)
		assert.Equal(t, tc, got)
	}
}
