package matchers

import (
	"fmt"
	"strings"

	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gstruct"
	"github.com/onsi/gomega/types"
)

// Phase 4.4 — array-aware extension to matches_shape. See
// docs/spec-array-aware-matcher.md for the design.
//
// Public surface:
//   - Convert(value any) → gomega.OmegaMatcher
//     Recursive type-lift router that maps any YAML-decoded value to
//     its equivalent Gomega matcher. Delegates EVERY per-element
//     decision to upstream-maintained primitives (gstruct.MatchKeys,
//     gomega.BeNumerically, gomega.Equal, gomega.BeNil). The only
//     custom branch is the []any case, which constructs a
//     RelativeOrderSubset.
//
//   - RelativeOrderSubset
//     The thin custom matcher we own — implements gomega.OmegaMatcher.
//     Greedy forward pass over observed; per-element decisions delegate
//     to child matchers; second pass on failure to classify "missing"
//     vs "wrong-order violation" for the diagnostic surface.

// Convert lifts a YAML-decoded value into the equivalent Gomega
// matcher tree. Pure dispatch; no per-call state.
//
//   - map[string]any → gstruct.MatchKeys(IgnoreExtras, …)
//     Recurses on each value. IgnoreExtras enforces the same subset
//     semantics matchMaps does today: only declared keys must match,
//     extras tolerated.
//   - []any → *RelativeOrderSubset
//     Recurses on each element. ROSM semantics (§4 of the spec).
//   - numeric (int/int64/int32/float64/float32/uint/uint64/uint32)
//     → gomega.BeNumerically("==", v)
//     Handles the JSON int↔float64 coercion every YAML/JSON round-trip
//     introduces.
//   - nil → gomega.BeNil()
//   - default (string, bool, etc.) → gomega.Equal(v)
//
// Exported so future composite matchers (a hypothetical
// `_match_strict: true` wrapping gomega.HaveExactElements, or a
// `matches_array_strict` registered name) can compose the same
// conversion chain without re-implementing it.
func Convert(v any) types.GomegaMatcher {
	switch t := v.(type) {
	case map[string]any:
		keys := gstruct.Keys{}
		for k, sub := range t {
			keys[k] = Convert(sub)
		}
		return gstruct.MatchKeys(gstruct.IgnoreExtras, keys)
	case []any:
		children := make([]types.GomegaMatcher, len(t))
		for i, e := range t {
			children[i] = Convert(e)
		}
		return &RelativeOrderSubset{children: children}
	case nil:
		return gomega.BeNil()
	case int, int64, int32, float64, float32, uint, uint64, uint32:
		// BeNumerically performs cross-type numeric comparison so YAML
		// int(42) matches JSON float64(42.0). gomega.Equal would fail.
		return gomega.BeNumerically("==", t)
	default:
		// string, bool, []byte, time.Time, etc. — pass through to Equal.
		return gomega.Equal(t)
	}
}

// RelativeOrderSubset is the Phase 4.4 custom matcher. Greedy forward
// pass; child matchers (built by Convert) handle every per-element
// decision. On failure, runs a second pass to distinguish
// "missing entirely" from "wrong-order violation" (the §6.2
// diagnostic split).
//
// Implements gomega.types.GomegaMatcher so it composes uniformly
// inside Convert's recursion (nested arrays just work — the outer
// Convert produces a RelativeOrderSubset, the inner ones do the
// same).
//
// Stateful within a single Match → FailureMessage call chain (the
// failure metadata is captured on Match; FailureMessage reads it).
// Gomega documents matchers as single-goroutine; rebuilt by Convert
// per assertion so concurrent use is safe.
type RelativeOrderSubset struct {
	children []types.GomegaMatcher

	// Failure state populated by Match for FailureMessage to read.
	failedIdx    int    // index in children that couldn't be placed
	cursor       int    // observed cursor at failure
	observedLen  int    // total observed length
	wrongOrderAt int    // index in observed[0..cursor) where the failed child WOULD match; -1 if missing entirely
	childMsg     string // captured closest-candidate FailureMessage from the failed child
}

// Match implements types.GomegaMatcher. Returns (true, nil) on
// success; (false, nil) on mismatch (with state captured for
// FailureMessage); (false, err) only when actual isn't a []any at all
// — per-element type errors are demoted to "no match, try next
// observed element" rather than fatal.
func (r *RelativeOrderSubset) Match(actual any) (success bool, err error) {
	observed, ok := actual.([]any)
	if !ok {
		return false, fmt.Errorf("RelativeOrderSubset: expected []any, got %T", actual)
	}
	r.observedLen = len(observed)
	r.childMsg = ""

	cursor := 0
	for k, child := range r.children {
		found := false
		for j := cursor; j < len(observed); j++ {
			ok, perr := child.Match(observed[j])
			if perr == nil && ok {
				cursor = j + 1
				found = true
				break
			}
			// Demote per-element type errors / mismatches to
			// "diagnostic info, continue searching". Capture the
			// LAST candidate's message — for the typical use case
			// where the wrong-order element is near the end, this
			// surfaces the most relevant diff.
			if perr != nil {
				r.childMsg = perr.Error()
			} else {
				r.childMsg = child.FailureMessage(observed[j])
			}
		}
		if !found {
			r.failedIdx = k
			r.cursor = cursor
			r.wrongOrderAt = r.findInPrefix(observed, child, cursor)
			return false, nil
		}
	}
	return true, nil
}

// findInPrefix returns the lowest index < cursor where child would
// match, or -1 if it doesn't match anywhere in observed[0..cursor).
// Drives the §6.2 "wrong-order violation" diagnostic classifier.
func (r *RelativeOrderSubset) findInPrefix(observed []any, child types.GomegaMatcher, cursor int) int {
	for j := 0; j < cursor; j++ {
		if ok, perr := child.Match(observed[j]); perr == nil && ok {
			return j
		}
	}
	return -1
}

// FailureMessage composes the §6.2 four-piece diagnostic. The path
// coordinate (`field "<path>"[k]`) is prepended by matches_shape.go's
// slice branch — RelativeOrderSubset is path-agnostic so it composes
// in nested contexts.
func (r *RelativeOrderSubset) FailureMessage(_ any) string {
	var b strings.Builder
	fmt.Fprintf(&b,
		"expected element [%d] not found at or after observed[%d] (cursor advanced through %d/%d elements)",
		r.failedIdx, r.cursor, r.cursor, r.observedLen,
	)
	if r.wrongOrderAt >= 0 {
		fmt.Fprintf(&b,
			"\nRELATIVE-ORDER VIOLATION: this element exists at observed index %d, but the cursor already advanced past it",
			r.wrongOrderAt,
		)
	} else {
		b.WriteString("\nMISSING: this element does not appear anywhere in observed")
	}
	if r.childMsg != "" {
		fmt.Fprintf(&b, "\nclosest candidate's diff:\n%s", r.childMsg)
	}
	return b.String()
}

// NegatedFailureMessage implements gomega.types.GomegaMatcher. The
// runtime/matchshape.go layer uses RelativeOrderSubset positively
// (every matches_shape consumer is positive-match; the scenario-level
// `not: true` flips the OUTER Consistently/Eventually wrapper, not
// the inner matcher). Defensive implementation regardless.
func (r *RelativeOrderSubset) NegatedFailureMessage(_ any) string {
	return "expected RelativeOrderSubset NOT to match, but every expected element was found in observed in the declared relative order"
}
