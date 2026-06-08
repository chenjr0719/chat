package scenario

import (
	"fmt"
	"sort"
	"time"
)

// ValidateSeedBlock enforces the five Phase 4.2 rules from
// docs/spec-room-subscription-seed.md §2.2:
//
//  1. seed.rooms[].id values are unique within the block.
//  2. seed.rooms[].type (when set) is one of the closed RoomType
//     enum (channel | dm | botDM | discussion).
//  3. Every seed.memberships.<alias>[].room resolves to a declared
//     seed.rooms[].id.
//  4. Every seed.memberships.<alias>[].roles[] entry is in the
//     closed role enum (owner | admin | member).
//  5. Every seed.rooms[].type=dm room is referenced by exactly two
//     distinct aliases.
//
// Returns nil for empty / partial seed blocks so the eight pre-Phase-4.2
// scenarios (which declare no rooms or memberships) keep validating.
//
// Errors are emitted in deterministic order (sorted by alias / room id
// where applicable) so test assertions are stable. The error prefix is
// neutral — callers (Sandbox.Setup in PR 2) wrap with their own
// "sandbox.Setup: ..." context.
func ValidateSeedBlock(seed SeedBlock) error {
	if len(seed.Rooms) == 0 && len(seed.Memberships) == 0 {
		// Nothing to validate. Existing scenarios short-circuit here.
		return nil
	}

	// Rule 1: unique room ids. Build the lookup set in one pass so the
	// rules that follow can use it cheaply.
	declaredRooms := map[string]SeedRoom{}
	for i := range seed.Rooms {
		r := &seed.Rooms[i]
		if r.ID == "" {
			return fmt.Errorf(`seed.rooms[%d]: missing id`, i)
		}
		if _, dup := declaredRooms[r.ID]; dup {
			return fmt.Errorf(`seed.rooms: duplicate id %q`, r.ID)
		}
		declaredRooms[r.ID] = *r
	}

	// Rule 2: every declared type is in the closed enum (empty allowed —
	// defaults to channel at room-insertion time).
	for _, id := range sortedRoomIDs(declaredRooms) {
		r := declaredRooms[id]
		if r.Type == "" {
			continue
		}
		if !isValidRoomType(r.Type) {
			return fmt.Errorf(`seed.rooms[%s]: type %q must be one of channel|dm|botDM|discussion`, r.ID, r.Type)
		}
	}

	// Rule 6 (Phase 4.5.1): seed.rooms[].created_at, when set, must
	// be a well-formed ${now ± duration} token. Reuses the Phase 4.3
	// cassandra-seed parser so the YAML grammar is identical across
	// surfaces. Empty falls back to sb.StartTime at room-insertion
	// time. The anchor time is irrelevant for validation (we discard
	// the resolved time.Time); we only assert the grammar.
	probe := time.Unix(0, 0).UTC()
	for _, id := range sortedRoomIDs(declaredRooms) {
		r := declaredRooms[id]
		if r.CreatedAt == "" {
			continue
		}
		if !IsNowToken(r.CreatedAt) {
			return fmt.Errorf(`seed.rooms[%s]: created_at %q must be a ${now ± duration} token`, r.ID, r.CreatedAt)
		}
		expr := unwrapToken(r.CreatedAt)
		if _, err := ResolveNowExpr(expr, probe); err != nil {
			return fmt.Errorf(`seed.rooms[%s]: created_at: %w`, r.ID, err)
		}
	}

	// Rules 3 + 4: walk memberships in sorted-alias order. Cross-checks
	// rooms exist + role enum compliance per membership entry.
	for _, alias := range sortedAliases(seed.Memberships) {
		entries := seed.Memberships[alias]
		for idx, m := range entries {
			if m.Room == "" {
				return fmt.Errorf(`seed.memberships[%s][%d]: missing room`, alias, idx)
			}
			if _, ok := declaredRooms[m.Room]; !ok {
				return fmt.Errorf(`seed.memberships[%s][%d]: references undeclared room %q`, alias, idx, m.Room)
			}
			for _, role := range m.Roles {
				if !isValidRole(role) {
					return fmt.Errorf(`seed.memberships[%s][%d]: role %q must be one of owner|admin|member`, alias, idx, role)
				}
			}
		}
	}

	// Rule 5: DM rooms must have exactly two distinct member aliases.
	// Walk declared rooms in sorted order so the error is deterministic
	// when multiple DMs violate the rule.
	memberAliasesByRoom := buildMemberAliasesByRoom(seed.Memberships)
	for _, id := range sortedRoomIDs(declaredRooms) {
		r := declaredRooms[id]
		if r.Type != RoomTypeDM {
			continue
		}
		aliases := memberAliasesByRoom[id]
		if len(aliases) != 2 {
			return fmt.Errorf(`seed.rooms[%s]: dm room needs exactly 2 distinct member aliases, got %d`, id, len(aliases))
		}
	}

	return nil
}

// isValidRoomType reports whether t is in the closed RoomType enum.
// Mirrors pkg/model.RoomType's exhaustive constant set.
func isValidRoomType(t RoomType) bool {
	switch t {
	case RoomTypeChannel, RoomTypeDM, RoomTypeBotDM, RoomTypeDiscussion:
		return true
	}
	return false
}

// isValidRole reports whether r is in the closed Role enum. Mirrors
// pkg/model.Role's exhaustive constant set.
func isValidRole(r string) bool {
	switch r {
	case RoleOwner, RoleAdmin, RoleMember:
		return true
	}
	return false
}

// sortedAliases returns the keys of m in lexical order so error
// messages are deterministic across runs.
func sortedAliases(m map[string][]SeedMembership) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedRoomIDs returns the keys of m in lexical order.
func sortedRoomIDs(m map[string]SeedRoom) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// buildMemberAliasesByRoom inverts the memberships map into
// room id → set of distinct aliases. The set is materialized as a
// slice (sorted) so callers can take len() for arity checks and
// surface the alias list for diagnostic messages without re-walking.
func buildMemberAliasesByRoom(memberships map[string][]SeedMembership) map[string][]string {
	uniq := map[string]map[string]struct{}{}
	for alias, entries := range memberships {
		for _, m := range entries {
			if m.Room == "" {
				continue
			}
			set, ok := uniq[m.Room]
			if !ok {
				set = map[string]struct{}{}
				uniq[m.Room] = set
			}
			set[alias] = struct{}{}
		}
	}
	out := make(map[string][]string, len(uniq))
	for room, set := range uniq {
		aliases := make([]string, 0, len(set))
		for a := range set {
			aliases = append(aliases, a)
		}
		sort.Strings(aliases)
		out[room] = aliases
	}
	return out
}
