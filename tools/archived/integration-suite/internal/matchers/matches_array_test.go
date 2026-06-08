package matchers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Phase 4.4 — array-aware matcher tests. See
// docs/spec-array-aware-matcher.md §7. Twenty-three tests covering:
//
//	§7.1 Happy-path  (8 tests)  — TestROSM_*Match / TestConvert_*
//	§7.2 Mismatch    (8 tests)  — TestROSM_*Rejected
//	§7.3 Diagnostics (4 tests)  — TestROSM_Diagnostic*
//	§7.4 Back-compat (3 tests)  — TestMatchesShape_*WithArray
//
// All tests assert against MatchesShape{}.Match (the public surface)
// when the array is nested under a key, and directly against
// RelativeOrderSubset.Match when the assertion is on the matcher's
// own contract.

// ─── §7.1 Happy-path ───────────────────────────────────────────────

func TestROSM_SingleElementExactMatch(t *testing.T) {
	observed := []any{map[string]any{"a": 1}}
	expected := []any{map[string]any{"a": 1}}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestROSM_ElementSubsetTolerated(t *testing.T) {
	// gstruct.IgnoreExtras under the hood — extras MUST be tolerated.
	observed := []any{map[string]any{"a": 1, "b": 2, "c": 3}}
	expected := []any{map[string]any{"a": 1}}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched, "extras must be tolerated by gstruct.MatchKeys(IgnoreExtras)")
}

func TestROSM_SubsequenceWithGaps(t *testing.T) {
	observed := []any{
		map[string]any{"a": 1},
		map[string]any{"a": 2},
		map[string]any{"a": 3},
	}
	expected := []any{
		map[string]any{"a": 1},
		map[string]any{"a": 3},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched, "intervening observed[1] must be tolerated")
}

func TestROSM_FullArrayMatch(t *testing.T) {
	observed := []any{
		map[string]any{"a": 1},
		map[string]any{"a": 2},
		map[string]any{"a": 3},
	}
	expected := []any{
		map[string]any{"a": 1},
		map[string]any{"a": 2},
		map[string]any{"a": 3},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched)
}

func TestROSM_EmptyExpectedMatchesAnything(t *testing.T) {
	rosm := Convert([]any{})
	for _, observed := range []any{
		[]any{},
		[]any{map[string]any{"a": 1}},
		[]any{1, 2, 3, "extra"},
	} {
		matched, err := rosm.Match(observed)
		require.NoError(t, err)
		assert.True(t, matched, "empty expected must match any observed array (vacuous subset)")
	}
}

func TestROSM_PrimitivesSubsequence(t *testing.T) {
	// gomega.BeNumerically handles the JSON int↔float64 coercion every
	// YAML/JSON round-trip introduces. Test deliberately mixes the
	// YAML int(1) literal with the JSON-style float64(1.0) observed
	// to exercise that coercion.
	observed := []any{float64(1), float64(2), float64(3)}
	expected := []any{1, 3}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched, "BeNumerically must coerce int(1) ≡ float64(1.0)")
}

func TestROSM_DuplicateExpectedWithSufficientSupply(t *testing.T) {
	observed := []any{
		map[string]any{"id": "A"},
		map[string]any{"id": "B"},
		map[string]any{"id": "A"},
	}
	expected := []any{
		map[string]any{"id": "A"},
		map[string]any{"id": "A"},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched, "two greedy passes must commit to observed[0] and observed[2]")
}

func TestROSM_NestedArrayRecurses(t *testing.T) {
	// Convert recurses through []any → RelativeOrderSubset, so a
	// nested array inside an outer one composes uniformly.
	observed := []any{
		[]any{"A", "B", "X"},
	}
	expected := []any{
		[]any{"A", "B"},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.True(t, matched, "nested ROSM must compose")
}

// ─── §7.2 Mismatch ─────────────────────────────────────────────────

func TestROSM_MissingElementRejected(t *testing.T) {
	observed := []any{map[string]any{"a": 1}}
	expected := []any{map[string]any{"a": 2}}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "MISSING")
}

func TestROSM_WrongOrderDetected(t *testing.T) {
	// The fundamental Phase 4.4 use case: pagination assertion
	// regression. Observed flipped from [Newest, Middle, Oldest] to
	// [Oldest, Middle, Newest]. Greedy matches Newest at observed[2],
	// cursor=3, looking for Middle from observed[3..) — exhausted.
	// Second pass finds Middle at observed[1] < cursor=3 → wrong
	// order classifier fires.
	observed := []any{
		map[string]any{"messageId": "Oldest"},
		map[string]any{"messageId": "Middle"},
		map[string]any{"messageId": "Newest"},
	}
	expected := []any{
		map[string]any{"messageId": "Newest"},
		map[string]any{"messageId": "Middle"},
		map[string]any{"messageId": "Oldest"},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "RELATIVE-ORDER VIOLATION",
		"must emit the relative-order classifier")
	assert.Contains(t, msg, "observed index 1",
		"must name the index where the missing element does exist")
}

func TestROSM_ExpectedLargerThanObservedRejected(t *testing.T) {
	observed := []any{map[string]any{"a": 1}}
	expected := []any{
		map[string]any{"a": 1},
		map[string]any{"a": 2},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "element [1]", "missing element is the second expected")
	assert.Contains(t, msg, "MISSING")
}

func TestROSM_EmptyObservedNonEmptyExpectedRejected(t *testing.T) {
	observed := []any{}
	expected := []any{map[string]any{"a": 1}}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "cursor advanced through 0/0",
		"diagnostic must reflect zero-length observed")
}

func TestROSM_DuplicateExpectedInsufficientSupplyRejected(t *testing.T) {
	observed := []any{map[string]any{"id": "A"}}
	expected := []any{
		map[string]any{"id": "A"},
		map[string]any{"id": "A"},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched, "two expected, one supply → mismatch")
}

func TestROSM_ElementFieldMismatchEmbedsChildDiagnostic(t *testing.T) {
	// Expected element exists at the right field name but with the
	// wrong value. The closest-candidate FailureMessage from
	// gstruct.MatchKeys should be embedded in our diagnostic.
	observed := []any{map[string]any{"messageId": "X", "extra": "ignored"}}
	expected := []any{map[string]any{"messageId": "Y"}}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "closest candidate's diff:",
		"must surface the Gomega FailureMessage from the closest candidate")
	// gstruct.MatchKeys's failure embeds the expected key name.
	assert.Contains(t, msg, "messageId",
		"closest-candidate diff must mention the failing field name")
}

func TestROSM_TypeMismatchMapVsPrimitiveRejected(t *testing.T) {
	// Expected is a map; observed element is a primitive. gstruct.MatchKeys
	// returns (false, err) for non-map actuals — RelativeOrderSubset
	// demotes per-element errors to "try next" rather than fatal.
	observed := []any{float64(5)}
	expected := []any{map[string]any{"a": 1}}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err, "per-element type errors must not bubble fatally")
	assert.False(t, matched)
}

func TestROSM_TypeMismatchPrimitiveVsMapRejected(t *testing.T) {
	// Symmetric: expected is a primitive; observed has a map. gomega.Equal
	// returns (false, nil) — also rejected via the standard "no match"
	// branch.
	observed := []any{map[string]any{"a": 1}}
	expected := []any{"literal-string"}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
}

// ─── §7.3 Diagnostics ──────────────────────────────────────────────

func TestROSM_DiagnosticEmitsCursorPosition(t *testing.T) {
	// Cursor reflects how far greedy advanced before exhaustion.
	observed := []any{
		map[string]any{"id": "A"},
		map[string]any{"id": "B"},
	}
	expected := []any{
		map[string]any{"id": "A"}, // matches obs[0]; cursor → 1
		map[string]any{"id": "Z"}, // not found anywhere
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "cursor advanced through 1/2",
		"diagnostic must encode (cursor, observed_len)")
}

func TestROSM_DiagnosticEmitsWrongOrderClassifier(t *testing.T) {
	// Symmetric to TestROSM_WrongOrderDetected but pins the wording.
	observed := []any{
		map[string]any{"id": "B"},
		map[string]any{"id": "A"},
	}
	expected := []any{
		map[string]any{"id": "A"},
		map[string]any{"id": "B"},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	assert.False(t, matched)
	msg := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	assert.Contains(t, msg, "RELATIVE-ORDER VIOLATION")
	assert.Contains(t, msg, "observed index 0",
		"must name the index where the missing expected does exist")
	assert.NotContains(t, msg, "MISSING:",
		"missing-classifier must NOT fire when wrong-order is the cause")
}

func TestROSM_ElementPathInMatchesShapeReason(t *testing.T) {
	// When ROSM is invoked via matches_shape (the production path),
	// the field "<path>"[k] coordinate must be prepended.
	observed := map[string]any{
		"messages": []any{map[string]any{"id": "X"}},
	}
	expected := map[string]any{
		"messages": []any{map[string]any{"id": "Y"}},
	}
	r := MatchesShape{}.Match(observed, expected)
	require.False(t, r.Matched)
	assert.Contains(t, r.Reason, `field "messages"`,
		"matches_shape must prepend the field path")
}

func TestROSM_DeterministicErrorAcrossRuns(t *testing.T) {
	// Same inputs → byte-identical Reason across 10 runs. Gomega's
	// format.Object iterates maps in sorted order (Go 1.12+), so the
	// embedded child diagnostic is deterministic too.
	observed := []any{
		map[string]any{"id": "B"},
		map[string]any{"id": "A"},
	}
	expected := []any{
		map[string]any{"id": "A"},
		map[string]any{"id": "B"},
	}
	rosm := Convert(expected)
	matched, err := rosm.Match(any(observed))
	require.NoError(t, err)
	require.False(t, matched)
	first := rosm.(*RelativeOrderSubset).FailureMessage(observed)
	for i := 0; i < 10; i++ {
		// Rebuild the matcher each iteration so we exercise the full
		// Convert → Match → FailureMessage chain.
		next := Convert(expected)
		ok, _ := next.Match(any(observed))
		require.False(t, ok)
		assert.Equal(t, first, next.(*RelativeOrderSubset).FailureMessage(observed),
			"FailureMessage must be deterministic across runs")
	}
}

// ─── §7.4 Back-compat ──────────────────────────────────────────────

func TestMatchesShape_ExistingMapOnlyPathsUnchanged(t *testing.T) {
	// Pin-test mirrors the existing matchers_test.go TestMatchesShape
	// to ensure the slice-branch insertion doesn't perturb the
	// map-only fast path.
	observed := map[string]any{"name": "general", "owner": "alice"}
	r := MatchesShape{}.Match(observed, map[string]any{"name": "general", "owner": "alice"})
	assert.True(t, r.Matched, r.Reason)
}

func TestMatchesShape_ScalarDeepEqPathUnchanged(t *testing.T) {
	// Strings, bools, numbers still go through deepEq. The slice branch
	// only triggers on []any expected values.
	observed := map[string]any{"status": "accepted", "count": 42}
	r := MatchesShape{}.Match(observed, map[string]any{"status": "accepted", "count": 42})
	assert.True(t, r.Matched, r.Reason)
}

func TestMatchesShape_MixedMapAndArrayExpectedBothBranchesFire(t *testing.T) {
	// One outer map subset + one array subset under it. Exercises
	// matchMaps recursion AND ROSM dispatch in the same Match call.
	observed := map[string]any{
		"body_json": map[string]any{
			"status": "ok",
			"items": []any{
				map[string]any{"id": "X"},
				map[string]any{"id": "Y"},
				map[string]any{"id": "Z"},
			},
		},
		"latency_ms": 12,
	}
	expected := map[string]any{
		"body_json": map[string]any{
			"items": []any{
				map[string]any{"id": "X"},
				map[string]any{"id": "Z"},
			},
		},
	}
	r := MatchesShape{}.Match(observed, expected)
	assert.True(t, r.Matched, r.Reason)
}

// ─── §7.5 The pagination unblock — paired Green/Red ────────────────

func TestMatchesShape_ReverseChronologicalPaginationUnblock(t *testing.T) {
	// Mirrors history-service's LoadHistoryResponse shape exactly:
	// {messages: [{messageId, msg, ...}, ...]}.
	observed := map[string]any{
		"messages": []any{
			map[string]any{"messageId": "mPastNewest0000000000", "msg": "Newest"},
			map[string]any{"messageId": "mPastMiddle0000000000", "msg": "Middle"},
			map[string]any{"messageId": "mPastOldest0000000000", "msg": "Oldest"},
		},
	}
	expected := map[string]any{
		"messages": []any{
			map[string]any{"messageId": "mPastNewest0000000000"},
			map[string]any{"messageId": "mPastMiddle0000000000"},
			map[string]any{"messageId": "mPastOldest0000000000"},
		},
	}
	r := MatchesShape{}.Match(observed, expected)
	assert.True(t, r.Matched, "Draft #3's intended assertion must pass: %s", r.Reason)
}

func TestMatchesShape_PaginationRegressionEmitsRelativeOrderViolation(t *testing.T) {
	// The same expected against a regression that flipped the
	// ordering. The matchshape Reason must carry the wrong-order
	// classifier through to ## Failure Details.
	observed := map[string]any{
		"messages": []any{
			map[string]any{"messageId": "mPastOldest0000000000", "msg": "Oldest"},
			map[string]any{"messageId": "mPastMiddle0000000000", "msg": "Middle"},
			map[string]any{"messageId": "mPastNewest0000000000", "msg": "Newest"},
		},
	}
	expected := map[string]any{
		"messages": []any{
			map[string]any{"messageId": "mPastNewest0000000000"},
			map[string]any{"messageId": "mPastMiddle0000000000"},
			map[string]any{"messageId": "mPastOldest0000000000"},
		},
	}
	r := MatchesShape{}.Match(observed, expected)
	require.False(t, r.Matched, "ordering regression must fail the assertion")
	assert.True(t, strings.Contains(r.Reason, "RELATIVE-ORDER VIOLATION"),
		"reason must carry the wrong-order classifier; got: %s", r.Reason)
}
