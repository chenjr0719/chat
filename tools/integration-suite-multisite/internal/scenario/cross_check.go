package scenario

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// CrossScenarioCheck validates a SET of scenarios for cross-scenario
// cache-key conflicts. The chat-app services hold in-process caches
// (sub-cache keyed `(roomID, account)`, room-meta-cache keyed `roomID`,
// user-cache keyed `userID`) with multi-minute TTLs that survive
// across scenarios in the same run. The sandbox truncates Mongo and
// Cassandra between scenarios — it does NOT (and cannot) reset
// service in-process state.
//
// When two scenarios touch the same cache key with conflicting state,
// the second scenario's verdict is silently wrong. This is finding
// F-009 in docs/integration-suite-multisite-findings.md; the
// author-discipline mitigation is documented at plan-ahead §2.10 and
// AUTHORING.md "Cross-scenario cache discipline".
//
// This function enforces the discipline at load time so the operator
// gets a loud error before any container boots, not a silent wrong
// verdict an hour into the smoke run.
//
// Two checks today (both fire on cache-key conflicts observed in
// the f8e5345 hardening commit):
//
//  1. (alias, room) ROLE conflict — same alias declared as a member
//     of the same room in two scenarios with DIFFERENT role sets.
//     Example: alice@r-busy=[member] in one scenario,
//     alice@r-busy=[owner] in another. Gatekeeper's sub-cache caches
//     the first projection; the second scenario reads the stale
//     value.
//
//  2. alias HOME-SITE conflict — same alias declared as a LOCAL
//     user (seed.users) on different sites across scenarios, OR
//     declared LOCAL in one scenario and REMOTE (remote_users) in
//     another. Example: bob in seed.users on site-a in one scenario
//     and as a remote_users stub (home_site=site-b) on site-a in
//     another. The user-cache projects the first siteID; the second
//     scenario's worker classifies bob with the wrong locality.
//
// Returns one error per detected conflict. Empty slice = clean.
//
// Filenames in the error messages are base names only (no leading
// path) for readability; the operator already knows the directory.
func CrossScenarioCheck(scenarios []*Scenario) []error {
	var out []error
	out = append(out, checkAliasRoomRoleConflicts(scenarios)...)
	out = append(out, checkAliasHomeSiteConflicts(scenarios)...)
	return out
}

// aliasRoomKey is a cache-key fingerprint for the sub-cache class.
type aliasRoomKey struct {
	alias string
	room  string
}

// checkAliasRoomRoleConflicts walks every scenario's memberships,
// collects per (alias, room) the set of distinct roles-fingerprints,
// and flags any (alias, room) seen with two or more distinct
// fingerprints across scenarios.
func checkAliasRoomRoleConflicts(scenarios []*Scenario) []error {
	// (alias, room) → set of (roles-fingerprint → list of scenarios)
	seen := map[aliasRoomKey]map[string][]string{}
	for _, s := range scenarios {
		short := filepath.Base(s.SourcePath)
		for siteName, site := range s.Sites {
			_ = siteName // reserved for future per-site cache scoping
			for alias, memberships := range site.Seed.Memberships {
				for _, m := range memberships {
					if m.Room == "" {
						continue
					}
					key := aliasRoomKey{alias: alias, room: m.Room}
					rolesFp := canonicalRolesFingerprint(m.Roles)
					if seen[key] == nil {
						seen[key] = map[string][]string{}
					}
					seen[key][rolesFp] = append(seen[key][rolesFp], short)
				}
			}
		}
	}

	// Flag any (alias, room) where more than one distinct
	// roles-fingerprint appears.
	var errs []error
	for key, fpMap := range seen {
		if len(fpMap) < 2 {
			continue
		}
		// Sort fingerprints for deterministic error output.
		fps := sortedFingerprintEntries(fpMap)
		var parts []string
		for _, fp := range fps {
			files := uniqueSorted(fpMap[fp])
			parts = append(parts, fmt.Sprintf("roles=%s in [%s]", fp, strings.Join(files, ", ")))
		}
		errs = append(errs, fmt.Errorf(
			"cross-scenario cache conflict: (alias=%q, room=%q) declared with DIFFERENT role sets across scenarios — %s; "+
				"this trips the gatekeeper sub-cache (F-009) — give each scenario a unique room id (see AUTHORING.md §Cross-scenario cache discipline)",
			key.alias, key.room, strings.Join(parts, " vs "),
		))
	}
	// Stable error order — operators want reproducible reports.
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}

// checkAliasHomeSiteConflicts walks every scenario's seed.users and
// remote_users, builds an alias → set of home-sites map, and flags
// aliases that resolve to more than one distinct home-site claim.
//
// "Home site" is the source-of-truth site the alias represents — for
// a seed.users declaration on site X, home_site = X; for a
// remote_users stub on site Y with explicit home_site=Z, home_site
// = Z. The legitimate remote-stub pattern declares the SAME alias
// in BOTH seed.users on the home site AND remote_users on the peer
// site with home_site = the home — both projections resolve to the
// same home site, so they collapse correctly.
//
// Conflict shape (what this check flags): the same alias appears
// with DIFFERENT home sites across scenarios. Example: bob in
// seed.users on site-a in one scenario AND in seed.users on site-b
// in another. The materialized user ID is deterministic from alias
// ("u-bob" per CLAUDE.md §6 — UUIDv7 hex for some entities, but
// users are alias-based for repeatability), so the same alias in
// two scenarios → same userID → same cache slot → the first
// scenario's home-site projection wins for the rest of the run.
func checkAliasHomeSiteConflicts(scenarios []*Scenario) []error {
	// alias → home_site → list of scenarios
	seen := map[string]map[string][]string{}
	for _, s := range scenarios {
		short := filepath.Base(s.SourcePath)
		for siteName, site := range s.Sites {
			for alias := range site.Seed.Users {
				if seen[alias] == nil {
					seen[alias] = map[string][]string{}
				}
				seen[alias][siteName] = append(seen[alias][siteName], short)
			}
			for alias, spec := range site.Seed.RemoteUsers {
				if seen[alias] == nil {
					seen[alias] = map[string][]string{}
				}
				seen[alias][spec.HomeSite] = append(seen[alias][spec.HomeSite], short)
			}
		}
	}

	var errs []error
	for alias, siteMap := range seen {
		if len(siteMap) < 2 {
			continue
		}
		sites := make([]string, 0, len(siteMap))
		for site := range siteMap {
			sites = append(sites, site)
		}
		sort.Strings(sites)
		var parts []string
		for _, site := range sites {
			files := uniqueSorted(siteMap[site])
			parts = append(parts, fmt.Sprintf("home_site=%s in [%s]", site, strings.Join(files, ", ")))
		}
		errs = append(errs, fmt.Errorf(
			"cross-scenario cache conflict: alias %q declared with DIFFERENT home sites across scenarios — %s; "+
				"this trips the worker user-cache (F-009) — rename the alias in one of the scenarios (e.g. `bob_a` vs `bob_b`, or `remotebob` for a remote-stub variant; see AUTHORING.md §Cross-scenario cache discipline)",
			alias, strings.Join(parts, " vs "),
		))
	}
	sort.Slice(errs, func(i, j int) bool { return errs[i].Error() < errs[j].Error() })
	return errs
}

// canonicalRolesFingerprint sorts + comma-joins a roles slice so two
// scenarios declaring [member, owner] vs [owner, member] are treated
// as equal.
func canonicalRolesFingerprint(roles []string) string {
	if len(roles) == 0 {
		// SeedMembership with no Roles defaults to [member] at
		// runtime; canonicalize so a bare-string membership entry
		// matches an explicit `roles: [member]` for cache-conflict
		// detection.
		return "member"
	}
	cp := append([]string{}, roles...)
	sort.Strings(cp)
	return strings.Join(cp, ",")
}

// sortedFingerprintEntries returns the keys of a fingerprint map in
// lexical order for deterministic error output.
func sortedFingerprintEntries(m map[string][]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// uniqueSorted returns the input with duplicates removed and the
// remaining entries lex-sorted.
func uniqueSorted(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
