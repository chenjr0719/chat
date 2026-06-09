package scenario

import (
	"fmt"
	"sort"
)

// CheckScenarioNameUniqueness returns one error per duplicated
// `scenario:` field across the loaded set. The scenario field is the
// perf-key in performance.json AND the identity used by the menu /
// reporter for "rerun failed" semantics; duplicates would silently
// overwrite each other's history and confuse interactive picks.
//
// Surfaces as a hard validate/runner error (plan-ahead §2.8 — when
// arbitrary subdirectory nesting was unlocked, perf-key collisions
// became possible across directories that previously couldn't share a
// filename).
//
// Empty slice = clean.
func CheckScenarioNameUniqueness(scenarios []*Scenario) []error {
	seen := make(map[string][]string, len(scenarios)) // name -> paths
	for _, s := range scenarios {
		if s == nil || s.Name == "" {
			continue
		}
		seen[s.Name] = append(seen[s.Name], s.SourcePath)
	}
	names := make([]string, 0, len(seen))
	for name, paths := range seen {
		if len(paths) > 1 {
			names = append(names, name)
		}
	}
	sort.Strings(names) // deterministic error order
	var errs []error
	for _, name := range names {
		paths := append([]string(nil), seen[name]...)
		sort.Strings(paths)
		errs = append(errs, fmt.Errorf(
			"duplicate scenario name %q across files %v — `scenario:` is the perf-history key and the menu identity; rename one",
			name, paths,
		))
	}
	return errs
}
