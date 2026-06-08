package matchers

import (
	"encoding/json"
	"fmt"
)

// MatchesShape checks that an observed JSON-decodable value satisfies
// every field constraint in expected. Expected is a map[string]any
// where keys are field paths (e.g. "name", "owner") and values are
// the required values for those fields.
//
// Semantics: SUBSET match at every level. The observed value may have
// extra fields not in expected; only the fields expected mentions must
// match. Nested maps recurse with the same subset semantics — so
// `expected: {body_json: {status: "accepted"}}` matches
// `observed.body_json: {status: "accepted", roomId: "…", roomType: "…"}`.
//
// Observed is unmarshaled into a generic map[string]any (so callers
// can pass raw JSON bytes, already-decoded maps, or typed structs —
// the struct fall-through marshals to JSON and unmarshals as a map).
type MatchesShape struct{}

func (MatchesShape) Match(observed, expected any) Result {
	want, ok := expected.(map[string]any)
	if !ok {
		return Result{Reason: fmt.Sprintf("matches_shape: expected must be map[string]any, got %T", expected)}
	}

	got, err := toMap(observed)
	if err != nil {
		return Result{Reason: err.Error()}
	}

	return matchMaps(got, want, "")
}

// matchMaps walks `want` and verifies every key is present in `got`
// with a matching value (subset semantics). Recurses into nested maps
// so the caller can express deep shape constraints without demanding
// full equality. Path is used in error messages to point at the
// failing field's location.
func matchMaps(got, want map[string]any, path string) Result {
	for k, v := range want {
		actual, present := got[k]
		if !present {
			return Result{Reason: fmt.Sprintf("matches_shape: field %q missing", path+k)}
		}
		// Nested map → recurse with subset semantics.
		if wantMap, ok := v.(map[string]any); ok {
			actualMap, ok := actual.(map[string]any)
			if !ok {
				return Result{Reason: fmt.Sprintf("matches_shape: field %q: want map, got %T", path+k, actual)}
			}
			if r := matchMaps(actualMap, wantMap, path+k+"."); !r.Matched {
				return r
			}
			continue
		}
		// Phase 4.4 — array → Relative Order Subset Match. The
		// matches_array.go layer (Gomega-delegated) handles every
		// per-element decision; this block just wires the observed
		// slice + dispatched matcher tree and surfaces the failure
		// reason with the field path prepended. See
		// docs/spec-array-aware-matcher.md.
		if wantSlice, ok := v.([]any); ok {
			actualSlice, ok := actual.([]any)
			if !ok {
				return Result{Reason: fmt.Sprintf("matches_shape: field %q: expected array, got %T", path+k, actual)}
			}
			rosm := Convert(wantSlice)
			matched, err := rosm.Match(any(actualSlice))
			if err != nil {
				return Result{Reason: fmt.Sprintf("matches_shape: field %q: %s", path+k, err.Error())}
			}
			if !matched {
				return Result{Reason: fmt.Sprintf("matches_shape: field %q: %s", path+k, rosm.FailureMessage(actualSlice))}
			}
			continue
		}
		// Scalar — fall back to deepEq.
		if !deepEq(actual, v) {
			return Result{Reason: fmt.Sprintf("matches_shape: field %q: got %#v want %#v", path+k, actual, v)}
		}
	}
	return Result{Matched: true}
}

func toMap(observed any) (map[string]any, error) {
	switch o := observed.(type) {
	case map[string]any:
		return o, nil
	case []byte:
		var got map[string]any
		if err := json.Unmarshal(o, &got); err != nil {
			return nil, fmt.Errorf("matches_shape: observed bytes not JSON: %v", err)
		}
		return got, nil
	case string:
		var got map[string]any
		if err := json.Unmarshal([]byte(o), &got); err != nil {
			return nil, fmt.Errorf("matches_shape: observed string not JSON: %v", err)
		}
		return got, nil
	default:
		// Struct / typed-payload fall-through: marshal to JSON, unmarshal
		// as a generic map so field assertions work against ReplyPayload
		// (and any future typed Event.Payload) without callers having to
		// pre-flatten. See internal/readers/reply_payload.go.
		raw, err := json.Marshal(observed)
		if err != nil {
			return nil, fmt.Errorf("matches_shape: observed %T not marshalable: %v", observed, err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			return nil, fmt.Errorf("matches_shape: observed %T marshals to non-object JSON: %v", observed, err)
		}
		return got, nil
	}
}

func deepEq(a, b any) bool {
	// JSON-unmarshal numbers come back as float64. Normalise integers.
	if af, ok := a.(float64); ok {
		if bi, ok := b.(int); ok {
			return af == float64(bi)
		}
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
