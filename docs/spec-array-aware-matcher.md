# Spec: Array-Aware Extension to `matches_shape`

**Status:** Draft v2 for review (no code yet)
**Phase:** 4.4 — Read-Side Assertion Coverage
**Spec date:** 2026-06
**Revision history:** v1 (custom recursive greedy) → v2 (Gomega-delegated; this document)

## 1. Motivation

Phase 4.3 Draft #3 (`history-service-paginates-messages.yaml`) hit a
structural gap in the matcher: `matches_shape`
(`tools/integration-suite/internal/matchers/matches_shape.go`)
recurses into nested **maps** but falls back to a `fmt.Sprintf`
string-compare on **slices**. The reverse-chronological reply
assertion that the brief originally wanted —
"`messages[0].messageId == mNewest, [1] == mMiddle, [2] == mOldest`" —
could not be expressed in YAML.

This phase introduces **Relative Order Subset Matching** (ROSM) for
arrays: an expected list declares the elements that must exist in the
observed array, in the declared relative order, allowing intervening
elements.

## 2. Revision v2 — what changed and why

**v1 (rejected, but archived in git history)** proposed a fully
custom recursive greedy algorithm: ~480 LOC of bespoke matchers,
diagnostics, and tests. The maintenance debt was the obvious cost —
every map / array / scalar branch had to be implemented, tested, and
later kept aligned with JSON-coercion edge cases.

**v2 (this document)** pivots to a **Gomega-delegated composition**.
The framework already imports `github.com/onsi/gomega@v1.41.0` for
the Gomega matcher integration in `internal/runtime/matchshape.go`,
and the `gstruct` and core `matchers` sub-packages already supply
every per-element primitive we'd otherwise build from scratch:

| Need | v1 (custom) | v2 (Gomega) |
|---|---|---|
| Map subset deep-match | recursive `matchMaps` reimplementation | `gstruct.MatchKeys(IgnoreExtras, …)` |
| Numeric JSON-coercion (`int64 ↔ float64`) | custom `deepEq` arm | `gomega.BeNumerically("==", v)` |
| Scalar equality | custom `deepEq` arm | `gomega.Equal(v)` |
| Element-level failure diagnostics | hand-rolled Reason strings | Gomega's `FailureMessage` strings (pretty-printed, battle-tested) |
| **ROSM outer loop** | **custom** | **custom — Gomega has no equivalent** |

### 2.1 Honest disclosure — the irreducible custom surface

A check of `gomega@v1.41.0/matchers.go` confirms no built-in matcher
implements ROSM:

- `ContainElements(matchers…)` is **unordered** (loses the relative
  ordering this spec exists to enforce).
- `HaveExactElements(matchers…)` is **strict-length** (forbids
  intervening observed elements; defeats subset tolerance).
- `gstruct.MatchAllElements` requires a name-to-element map identifier
  function and is intended for keyed collections, not ordered ones.
- `ConsistOf` is set-equality (rejects extras AND is unordered).

So the **outer ROSM loop stays custom** — but as a thin Gomega-shape
wrapper (`Match` / `FailureMessage` / `NegatedFailureMessage`) of
~60 LOC that delegates every per-element decision to Gomega's
pre-built sub-matchers. The net custom surface drops from ~480 LOC to
~250 LOC, and the per-element subset / numeric-coercion logic is now
maintained upstream by the Gomega team rather than by us.

## 3. YAML authoring surface

**Unchanged from v1.** The author-facing grammar is the same; only
the engine behind it pivots to Gomega.

### 3.1 List-of-maps form

```yaml
match:
  body_json:
    messages:
      - messageId: mPastNewest0000000000
      - messageId: mPastMiddle0000000000
      - messageId: mPastOldest0000000000
```

YAML decodes `messages` as `[]any`, each entry as `map[string]any`.
The matcher recognises the array shape and switches from the
today-fallback `deepEq` to Relative Order Subset Match: each
expected element must appear in observed in the declared relative
order; unlisted observed elements between them are tolerated.

### 3.2 List-of-primitives form

```yaml
match:
  body_json:
    roomIds:
      - r-engineering
      - r-design
```

Primitives use `gomega.Equal` per element (or `gomega.BeNumerically`
for numbers — Gomega picks the right comparison automatically when
we call `Equal` on a number, or we use `BeNumerically` explicitly in
the conversion layer). Order constraint identical to §3.1.

### 3.3 Backward compatibility — zero current scenarios use array-valued match

Grep across the 10 currently-drafted scenarios confirms ZERO use of
array values under `match:` blocks. The slice-branch swap from
`deepEq` to ROSM is a one-shot change with no migration cost.

## 4. ROSM algorithm

Given expected `e = [e_0, e_1, …, e_n]` and observed
`o = [o_0, …, o_m]`:

```
cursor = 0
for k in 0..n:
    childMatcher = convert(e_k)    # Gomega matcher; pre-computed once
    found = false
    for j in cursor..m:
        ok, _ := childMatcher.Match(o[j])
        if ok:
            cursor = j + 1
            found = true
            break
    if not found:
        return mismatch(k, cursor, childMatcher)
return success
```

`convert(value)` is the single new pure function (§5.2). The inner
loop calls `gomega.types.GomegaMatcher.Match(actual)` — a public
Gomega API — and consumes the boolean result. **Every per-element
comparison decision is Gomega's**.

### 4.1 Greedy correctness for the targeted use case

Greedy is provably correct when expected elements are
**uniquely-distinguishing** — every expected element matches at most
one observed element. Pagination assertions specify distinct
`messageId` per element, so greedy is correct by construction. A §8
open question proposes falling back to backtracking IF a real
scenario surfaces the edge case.

### 4.2 Empty-array and duplicate edge cases

| Expected | Observed | Result | Rationale |
|---|---|---|---|
| `[]` | `[]` | match | vacuous |
| `[]` | `[any…]` | match | subset semantics — empty constraint matches anything |
| `[e]` | `[]` | mismatch | `e` has no candidate |
| `[A, A]` | `[A, B, A]` | match | two greedy passes commit to indices 0 and 2 |
| `[A, A]` | `[A]` | mismatch | only one supply for two demands |
| `[[A,B]]` | `[[A,B,X]]` | match | nested arrays recurse — free side effect of §5.2 |

## 5. Implementation

### 5.1 File layout

| File | Status | Role |
|---|---|---|
| `internal/matchers/matches_array.go` | **new (~80 LOC)** | `Convert` (value → `gomega.OmegaMatcher`); `RelativeOrderSubset` matcher struct with `Match` / `FailureMessage` / `NegatedFailureMessage`. |
| `internal/matchers/matches_shape.go` | **edit (~15 LOC delta)** | Replace the slice fallback in `matchMaps` with `RelativeOrderSubset` dispatch. |
| `internal/matchers/matches_array_test.go` | **new (~280 LOC)** | The 23 unit tests from §7. |

The matcher's public API (`MatchesShape.Match(observed, expected any) Result`)
is unchanged. The Gomega-backed ROSM is internal.

### 5.2 `Convert` — value-to-Gomega-matcher dispatch

```go
// Convert lifts a YAML-decoded value (any) into the equivalent
// gomega.OmegaMatcher. Recursion handles nested maps and nested
// arrays uniformly. Pure dispatch — no per-call state.
func Convert(v any) types.GomegaMatcher {
    switch t := v.(type) {
    case map[string]any:
        keys := gstruct.Keys{}
        for k, sub := range t {
            keys[k] = Convert(sub)            // recurse
        }
        return gstruct.MatchKeys(gstruct.IgnoreExtras, keys)

    case []any:
        children := make([]types.GomegaMatcher, len(t))
        for i, e := range t {
            children[i] = Convert(e)           // recurse
        }
        return &RelativeOrderSubset{children: children}

    case int, int64, int32, float64, float32:
        return gomega.BeNumerically("==", t)  // JSON-coerced numbers compare cleanly

    case nil:
        return gomega.BeNil()

    default:
        // string, bool, []byte, time.Time, etc.
        return gomega.Equal(t)
    }
}
```

**Total custom dispatch logic: ~25 LOC.** Every match decision past
this point is a Gomega-maintained primitive.

### 5.3 `RelativeOrderSubset` — the thin custom matcher

```go
type RelativeOrderSubset struct {
    children []types.GomegaMatcher

    // Failure-side state captured by Match for FailureMessage.
    failedIdx    int      // index in `children` that couldn't be placed
    cursor       int      // observed cursor at point of failure
    observedLen  int
    wrongOrderAt int      // index in observed where failedIdx WOULD match (set by second pass; -1 = missing entirely)
    childMsg     string   // FailureMessage from the failed child matcher against its closest candidate
}

func (r *RelativeOrderSubset) Match(actual any) (bool, error) {
    observed, ok := actual.([]any)
    if !ok {
        return false, fmt.Errorf("RelativeOrderSubset: expected []any, got %T", actual)
    }
    r.observedLen = len(observed)
    cursor := 0
    for k, child := range r.children {
        found := false
        for j := cursor; j < len(observed); j++ {
            ok, err := child.Match(observed[j])
            if err != nil { return false, err }
            if ok {
                cursor = j + 1
                found = true
                break
            }
            // Capture the closest candidate's failure message
            // (Gomega's pretty-printed string) for diagnostic use.
            r.childMsg = child.FailureMessage(observed[j])
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
// match — i.e. the second-pass §6.3 wrong-order detection. Returns
// -1 if child doesn't match anywhere in observed[0..cursor).
func (r *RelativeOrderSubset) findInPrefix(observed []any, child types.GomegaMatcher, cursor int) int {
    for j := 0; j < cursor; j++ {
        if ok, _ := child.Match(observed[j]); ok {
            return j
        }
    }
    return -1
}
```

`Match` body: ~25 LOC. `findInPrefix`: ~8 LOC. `FailureMessage`:
~20 LOC of error-text composition that includes the failing child's
Gomega `FailureMessage` (so the diagnostic carries Gomega's
pretty-printed expected/actual). `NegatedFailureMessage`: ~5 LOC.

Per-element decisions (Gomega's responsibility): NONE in our code.
Greedy outer loop (our responsibility): the 6-line for-for-if block.

### 5.4 Hook into `matchMaps`

`matches_shape.go` change is minimal:

```go
// before:
//   // Scalar / slice — fall back to deepEq.
//   if !deepEq(actual, v) { … }
//
// after:
if wantSlice, ok := v.([]any); ok {
    actSlice, ok := actual.([]any)
    if !ok {
        return Result{Reason: fmt.Sprintf(
            `matches_shape: field %q: expected array, got %T`,
            path+k, actual)}
    }
    m := &RelativeOrderSubset{children: convertChildren(wantSlice)}
    if ok, _ := m.Match(any(actSlice)); !ok {
        return Result{Reason: fmt.Sprintf(
            `matches_shape: field %q: %s`, path+k, m.FailureMessage(actSlice))}
    }
    continue
}
if !deepEq(actual, v) { … }   // scalar fallback unchanged
```

Total edit: **~15 LOC** in `matches_shape.go`. The rest of `matchMaps`
(map subset semantics, scalar `deepEq`) is unchanged.

## 6. Diagnostics

### 6.1 The chain

`RelativeOrderSubset.FailureMessage` → captured into `Result.Reason`
(via `matches_shape.go`) → `shapeMatcher.bestReason` →
`shapeMatcher.FailureMessage` → `reporter.renderFailureDetails` →
the `## Failure Details` section of `last-run.md`.

Same chain as today; only the upstream `Result.Reason` payload changes.

### 6.2 Message taxonomy

`RelativeOrderSubset.FailureMessage` composes four pieces:

1. The expected-element index and the chosen cursor:
   `expected element [k]: cursor advanced to j=cursor/observed_len`
2. The "wrong order vs missing" classifier from the second-pass scan:
   - `wrongOrderAt >= 0` → `RELATIVE-ORDER VIOLATION: this element exists at observed index <wrongOrderAt>, but the cursor already advanced past it`
   - `wrongOrderAt == -1` → `MISSING: this element does not appear anywhere in observed`
3. The failing child matcher's own `FailureMessage` against its
   closest candidate — Gomega gives us this verbatim, pretty-printed
   with `format.Object`:
   `closest candidate at observed[j-1]:\n<gomega's multi-line Expected/Actual diff>`
4. The element-level path (`field "messages"[k]`) prepended by
   `matches_shape.go`.

### 6.3 Worked example

Expected:

```yaml
match:
  body_json:
    messages:
      - messageId: mNewest
      - messageId: mMiddle
      - messageId: mOldest
```

Observed (a regression that flipped the ordering):

```json
{"messages": [
  {"messageId": "mOldest"},
  {"messageId": "mMiddle"},
  {"messageId": "mNewest"}
]}
```

Greedy matches `mNewest` at observed[2], cursor advances to 3.
Looking for `mMiddle` in observed[3..) — exhausted. Second pass
finds `mMiddle` at observed[1] (< cursor=3). Wrong order.

`## Failure Details` renders:

```
matches_shape: field "messages"[1]: expected element not found at or after observed[3] (cursor advanced through 3/3 elements)
RELATIVE-ORDER VIOLATION: this element exists at observed index 1, but the cursor already advanced past it
closest candidate at observed[2]:
  Expected
      <map[string]interface {} | len:1>: {
          "messageId": "mNewest",
      }
  to match keys
      <gstruct.Keys | len:1>: {
          "messageId": <*matchers.EqualMatcher | 0xc0...>: {
              Expected: "mMiddle",
          },
      }
```

The Gomega chunk in the bottom three lines is Gomega's own
`format.Object` output — we don't author or maintain it.

## 7. Test plan — same 23 tests, now executed against the Gomega-backed matcher

**Crucially, the 23-test scenario inventory from spec v1 is preserved
in full.** The tests assert against `MatchesShape{}.Match(...)` —
which v2 implements via the Gomega delegation. So the test surface
proves the **behavior** is correct without coupling to the
implementation strategy.

### 7.1 Happy-path (8 tests)

| # | Scenario | Expected | Observed | Outcome |
|---|---|---|---|---|
| 1 | Single-element exact match | `[{a:1}]` | `[{a:1}]` | match |
| 2 | Element subset | `[{a:1}]` | `[{a:1,b:2}]` | match (gstruct.IgnoreExtras) |
| 3 | Subsequence with gaps | `[{a:1},{a:3}]` | `[{a:1},{a:2},{a:3}]` | match |
| 4 | Full array | `[{a:1},{a:2},{a:3}]` | `[{a:1},{a:2},{a:3}]` | match |
| 5 | Empty expected | `[]` | `[anything]` | match |
| 6 | Primitives subsequence | `[1,3]` | `[1,2,3]` | match (BeNumerically) |
| 7 | Duplicate expected | `[A,A]` | `[A,B,A]` | match |
| 8 | Nested array | `[[A,B]]` | `[[A,B,X]]` | match |

### 7.2 Mismatch (8 tests)

| # | Scenario | Reason class |
|---|---|---|
| 9 | Missing element | element missing |
| 10 | Wrong order | relative-order violation (second-pass hit) |
| 11 | Expected larger than observed | element missing |
| 12 | Empty observed, non-empty expected | element missing |
| 13 | Duplicate expected, single supply | element missing |
| 14 | Element-level field mismatch | nearest-candidate Gomega FailureMessage embedded |
| 15 | Type mismatch (map vs primitive) | gstruct.MatchKeys's "is type X, expected map" error |
| 16 | Type mismatch (primitive vs map) | symmetric |

### 7.3 Diagnostics (4 tests)

| # | Scenario | Assertion on Reason |
|---|---|---|
| 17 | Element-missing emits cursor position | contains "cursor advanced through k/m" |
| 18 | Relative-order emits prior index | contains "RELATIVE-ORDER VIOLATION" + the lower index |
| 19 | Element-level path | path includes `[k]` |
| 20 | Determinism — same inputs → byte-identical reasons across 10 runs | exact-string assertion |

### 7.4 Back-compat (3 tests)

| # | Scenario | Notes |
|---|---|---|
| 21 | Existing map-only matchshape paths | every existing `matches_shape_test.go` test re-runs unmodified |
| 22 | Existing scalar deepEq path | unchanged |
| 23 | Mixed map + array expected | both branches fire in the same matchMaps walk |

### 7.5 Pagination unblocker

The §6.3 worked-example pair becomes one test in green form and one
in red, asserting both the match success on correctly-ordered data
and the exact "RELATIVE-ORDER VIOLATION" string on the regression.

### 7.6 Coverage target

95%+ on `matches_array.go`. Every branch in `Convert` exercised
(map / slice / int / float / nil / string fallback); every branch in
`Match` exercised (greedy hit, greedy miss with wrongOrderAt, greedy
miss without). The Gomega sub-matchers themselves come pre-tested by
upstream.

## 8. Open questions for review

| # | Question | Default if no input |
|---|---|---|
| Q1 | Greedy vs. backtracking when expected elements aren't uniquely distinguishing? | **Greedy** — §4.1 rationale. Filed as gap if a real scenario hits the edge. |
| Q2 | Should we introduce `_match_strict: true` to opt into `HaveExactElements` semantics? | **No (Phase 4.4)** — defer until a scenario can't be expressed without it. |
| Q3 | Path notation in Reason: `[k]` vs `.k`? | **`[k]`** — matches JSON-Path / Cassandra conventions. |
| Q4 | Should `Convert` cache the converted matcher tree across re-runs? | **No** — Gomega matchers are sometimes stateful across `Match` calls; safer to rebuild per-assertion. (RelativeOrderSubset itself is stateful — proves the point.) |
| Q5 | Should we deprecate `internal/matchers/contains.go` now that ROSM subsumes its primary use? | **No** — registered under "contains" name; keep for back-compat. |
| Q6 | Should we also migrate `matchMaps` (current map subset path) to `gstruct.MatchKeys`? | **Defer** — out of scope; this PR is array-only. Filed as a v4.4.1 opportunity. |
| Q7 | Should `Convert` register itself as a public reusable utility for future matchers? | **Yes — exported from `internal/matchers/matches_array.go`** so a future `_match_strict` matcher composes the same conversion chain. |

## 9. Non-goals (unchanged from v1)

- Strict-equality / length-exact assertions (`HaveExactElements`-style).
- Element-count operators (`_match_count_gte: N`).
- Per-element negation (scenario-level `not: true` covers the
  whole-array case).
- Set semantics (out-of-order subset).
- Sorting hints.

## 10. Backward compatibility (unchanged from v1)

- Audited the 10 current scenarios: **zero** use array-valued fields
  under `match:` blocks.
- `MatchesShape.Match(observed, expected any) Result` signature
  unchanged.
- The slice branch in `matchMaps` is the only edited surface; every
  other path (map recursion, scalar `deepEq`, the registered matcher
  catalog) stays bit-identical.
- The downstream `## Failure Details` reporter is unchanged — it
  reads `shapeMatcher.bestReason`, which v2 populates the same way
  v1 would.

## 11. Net change estimate (revised v1 → v2)

| Layer | v1 (custom) | v2 (Gomega-backed) | Delta |
|---|---|---|---|
| `matches_shape.go` slice branch + helpers | ~140 | ~15 | **−125** |
| `matches_array.go` (new) | n/a | ~80 | +80 |
| `matches_shape_test.go` | ~340 | n/a | (renamed/moved) |
| `matches_array_test.go` | n/a | ~280 | (moved tests, ~60 saved on conversion-tree assertions Gomega owns upstream) |
| **Total** | **~480** | **~375** | **−105 (~22% reduction)** |

The reduction would be larger if §8 Q6 (migrate `matchMaps` to
`gstruct.MatchKeys`) were in scope — likely another −80 LOC and
zero behavioral change. Deferred to keep this PR focused on the
array gap.

## 12. Recommended landing path

1. **Land this spec v2** (commit + push).
2. **Single PR — array matcher.**
   - Add `internal/matchers/matches_array.go` (`Convert` + `RelativeOrderSubset`).
   - Replace the slice fallback in `matches_shape.go`.
   - Add `internal/matchers/matches_array_test.go` with the 23 tests.
   - Coverage 95%+ on the new file; existing matchers test pass unchanged.
3. **Demonstration PR — Draft #3 refactor.**
   - Open `scenarios/drafts/history-service-paginates-messages.yaml`.
   - Replace the temporary `body_json: {}` block with:

     ```yaml
     - location: reply
       match:
         body_json:
           messages:
             - messageId: mPastNewest0000000000
             - messageId: mPastMiddle0000000000
             - messageId: mPastOldest0000000000
     ```

   - Collapse the "Known harness limitation (1)" header comment to a
     one-line note: `# Phase 4.4 array-aware matcher (spec-array-aware-matcher.md) closed the limitation. Reverse-chronological ordering is now asserted directly via ROSM.`
   - Leave Limitation (2) — the `meta.createdAt` workaround — in
     place; that's a separate sandbox_rooms.go follow-up.
   - Run `cmd/validator` (asserts 10 drafts still parse) and
     `USE_INFRA=true make local` (asserts the new ROSM block
     evaluates correctly against the live history-service reply).

PR 2 ships the matcher; PR 3 demonstrates the unblock end-to-end.

---

**Approval needed before any code lands.** Specifically:

- §2 the Gomega-delegation pivot (vs reverting to v1's full custom).
- §4.1 greedy vs. backtracking.
- §6 diagnostic taxonomy — confirm Gomega's pretty-printed embed is
  what we want surfacing in `## Failure Details`.
- §8 open questions — particularly Q6 (defer the matchMaps migration
  to a follow-up).
- §12 PR sequencing (matcher first, demonstration second).
