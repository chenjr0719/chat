package seedeffect

import (
	"fmt"
	"sort"
)

// ValidateFlags rejects any seed-effect flag in `userFlags` that is
// not registered in `reg`. Called by the runner at scenario load
// time so unknown flags fail fast with the registered-set listed.
//
// `userFlags` is the {user-alias → {flag → bool}} shape produced by
// scenario.Scenario.Seed.Users (kept loosely typed here so the
// seedeffect package doesn't depend on the scenario package — the
// runner.go bridge converts).
//
// Flags with value `false` are NOT rejected even if unknown — a
// future scenario could declare a flag for documentation purposes
// while the effect itself stays disabled. Only true-valued unknown
// flags fail (since they're the only ones that would actually try
// to dispatch through the registry).
func ValidateFlags(reg *Registry, userFlags map[string]map[string]bool) error {
	if reg == nil {
		return fmt.Errorf("seedeffect.ValidateFlags: nil registry")
	}
	var bad []string
	for alias, flags := range userFlags {
		for name, val := range flags {
			if !val {
				continue
			}
			if _, err := reg.Get(name); err != nil {
				bad = append(bad, fmt.Sprintf("%s.%s", alias, name))
			}
		}
	}
	if len(bad) == 0 {
		return nil
	}
	sort.Strings(bad)
	return fmt.Errorf("seedeffect: %d unknown true-valued flag(s) in scenario: %v; available: %v",
		len(bad), bad, reg.Names())
}
