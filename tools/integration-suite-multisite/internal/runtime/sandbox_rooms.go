package runtime

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/seedeffect"
)

// insertSeededRooms materializes the scenario's seed.Rooms +
// seed.Memberships block into three Mongo collections: `rooms`,
// `subscriptions`, and `room_members`, one site at a time. Field shapes
// mirror the production Go structs (pkg/model.Room / .Subscription /
// .RoomMember) per docs/spec-room-subscription-seed.md §3 — written via
// bson.M so the harness stays black-box and pkg/model is untouched.
//
// Each site's rooms are inserted into the per-site Mongo database from
// sb.Deps.MongoBySite. A site block with no rooms is a no-op.
func insertSeededRooms(ctx context.Context, sb *Sandbox) error {
	tOpen := sb.StartTime.UTC()

	for siteName, siteBlock := range sb.Scenario.Sites {
		if len(siteBlock.Seed.Rooms) == 0 {
			continue
		}

		db, ok := sb.Deps.MongoBySite[siteName]
		if !ok || db == nil {
			return fmt.Errorf("sandbox: no Mongo for site %q (scenario expects it)", siteName)
		}

		rooms := siteBlock.Seed.Rooms
		memberships := siteBlock.Seed.Memberships

		roomsByID := indexRoomsByID(rooms)
		membersByRoom := membershipsByRoom(memberships)

		roomDocs, err := buildRoomDocs(rooms, membersByRoom, sb.Users, siteName, tOpen)
		if err != nil {
			return fmt.Errorf("build room docs for %s: %w", siteName, err)
		}
		if _, err := db.Collection("rooms").InsertMany(ctx, roomDocs); err != nil {
			return fmt.Errorf("insert rooms for %s: %w", siteName, err)
		}

		subDocs, memDocs := buildSubscriptionAndMemberDocs(memberships, roomsByID, sb.Users, siteName, tOpen)
		if len(subDocs) > 0 {
			if _, err := db.Collection("subscriptions").InsertMany(ctx, subDocs); err != nil {
				return fmt.Errorf("insert subscriptions for %s: %w", siteName, err)
			}
		}
		if len(memDocs) > 0 {
			if _, err := db.Collection("room_members").InsertMany(ctx, memDocs); err != nil {
				return fmt.Errorf("insert room_members for %s: %w", siteName, err)
			}
		}
	}
	return nil
}

// indexRoomsByID returns a roomID → SeedRoom lookup. Validation already
// guarantees uniqueness, so the map is a faithful 1:1 reflection of the
// input slice.
func indexRoomsByID(rooms []scenario.SeedRoom) map[string]scenario.SeedRoom {
	out := make(map[string]scenario.SeedRoom, len(rooms))
	for _, r := range rooms {
		out[r.ID] = r
	}
	return out
}

// membershipsByRoom inverts the (alias → memberships) map into a
// (roomID → aliases) map. Distinct aliases per room (validation rule 5
// for DMs depends on this being a set, not a multiset).
func membershipsByRoom(memberships map[string][]scenario.SeedMembership) map[string][]string {
	uniq := map[string]map[string]struct{}{}
	for alias, list := range memberships {
		for _, m := range list {
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

// buildRoomDocs returns one bson.M per declared room. The
// (uids, accounts) pair is sorted lexically by uid so reads — including
// the production room-service code path that ships sorted dm participant
// lists via pkg/model.BuildDMParticipants — see the same canonical order.
//
// Phase 4.5.1 — per-room CreatedAt resolution: when SeedRoom.CreatedAt
// is set, the ${now ± duration} token is resolved against tOpen (the
// sandbox session boundary) via the Phase 4.3 cassandra-seed parser.
// Empty falls back to tOpen for backward compat with the 10
// pre-4.5.1 scenarios. Returns the first parse error verbatim so the
// outer caller can wrap it with the room coordinate.
func buildRoomDocs(
	rooms []scenario.SeedRoom,
	membersByRoom map[string][]string,
	users map[string]*seedeffect.SeedUser,
	siteID string,
	tOpen time.Time,
) ([]any, error) {
	docs := make([]any, 0, len(rooms))
	for _, r := range rooms {
		aliases := membersByRoom[r.ID]
		uids, accounts := pairedUIDsAccounts(aliases, users)
		createdAt, err := resolveRoomCreatedAt(r, tOpen)
		if err != nil {
			return nil, fmt.Errorf("seed.rooms[%s]: %w", r.ID, err)
		}
		// userCount defaults to the derived membership count (mirrors
		// what room-worker would write at room-create time). Explicit
		// SeedRoom.UserCount overrides the derive — see the struct's
		// docstring for the large-room-cap scenario rationale.
		userCount := len(uids)
		if r.UserCount != nil {
			userCount = *r.UserCount
		}
		doc := bson.M{
			"_id":       r.ID,
			"name":      r.Name,
			"type":      effectiveRoomType(r.Type),
			"siteId":    siteID,
			"userCount": userCount,
			"appCount":  0,
			"uids":      uids,
			"accounts":  accounts,
			"lastMsgId": "",
			"createdAt": createdAt,
			"updatedAt": tOpen,
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// resolveRoomCreatedAt returns the time.Time the room's createdAt
// column should carry. Empty SeedRoom.CreatedAt → tOpen (the
// pre-Phase-4.5.1 default; ten existing scenarios rely on this).
// Non-empty must be a ${now ± duration} token (grammar shared with
// the Phase 4.3 cassandra-seed engine — validated up-front by
// ValidateSeedBlock's rule 6, so the parse error here is purely
// defensive).
func resolveRoomCreatedAt(r scenario.SeedRoom, tOpen time.Time) (time.Time, error) {
	if r.CreatedAt == "" {
		return tOpen, nil
	}
	if !scenario.IsNowToken(r.CreatedAt) {
		return time.Time{}, fmt.Errorf("created_at %q must be a ${now ± duration} token", r.CreatedAt)
	}
	return scenario.ResolveCreatedAtToken(r.CreatedAt, tOpen)
}

// buildSubscriptionAndMemberDocs returns the parallel slices of
// subscriptions + room_members documents. Iteration is sorted by alias
// first, then by membership-list index, so each invocation produces a
// byte-identical document order.
func buildSubscriptionAndMemberDocs(
	memberships map[string][]scenario.SeedMembership,
	roomsByID map[string]scenario.SeedRoom,
	users map[string]*seedeffect.SeedUser,
	siteID string,
	tOpen time.Time,
) (subs, members []any) {
	aliases := make([]string, 0, len(memberships))
	for a := range memberships {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)

	for _, alias := range aliases {
		u, ok := users[alias]
		if !ok {
			// Validation does not (currently) require every membership
			// alias to be a declared seed.user. Skip silently — a future
			// rule may make this a hard error.
			continue
		}
		for _, m := range memberships[alias] {
			room, ok := roomsByID[m.Room]
			if !ok {
				continue // validation should have rejected this earlier
			}
			roles := effectiveRoles(m.Roles)
			subs = append(subs, bson.M{
				"_id": "sub-" + alias + "-" + room.ID,
				"u": bson.M{
					"_id":     u.ID,
					"account": u.Account,
					"isBot":   false,
				},
				"roomId":       room.ID,
				"siteId":       siteID,
				"roles":        roles,
				"name":         room.Name,
				"roomType":     effectiveRoomType(room.Type),
				"isSubscribed": true,
				"joinedAt":     tOpen,
				"hasMention":   false,
				"alert":        false,
				"muted":        false,
			})
			members = append(members, bson.M{
				"_id": "rm-" + alias + "-" + room.ID,
				"rid": room.ID,
				"ts":  tOpen,
				"member": bson.M{
					"id":      u.ID,
					"type":    "individual",
					"account": u.Account,
				},
			})
		}
	}
	return subs, members
}

// pairedUIDsAccounts returns the (uids, accounts) pair sorted by uid.
// uids[i] and accounts[i] always describe the same user — mirrors the
// invariant pkg/model.BuildDMParticipants enforces for DM rooms, and
// extends it to N-member channels for the same write-time stability.
func pairedUIDsAccounts(aliases []string, users map[string]*seedeffect.SeedUser) (uids, accounts []string) {
	type pair struct{ uid, account string }
	pairs := make([]pair, 0, len(aliases))
	for _, alias := range aliases {
		u, ok := users[alias]
		if !ok {
			continue
		}
		pairs = append(pairs, pair{uid: u.ID, account: u.Account})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].uid < pairs[j].uid })
	uids = make([]string, 0, len(pairs))
	accounts = make([]string, 0, len(pairs))
	for _, p := range pairs {
		uids = append(uids, p.uid)
		accounts = append(accounts, p.account)
	}
	return uids, accounts
}

// effectiveRoomType applies the "empty defaults to channel" rule from
// docs/spec-room-subscription-seed.md §3.1.
func effectiveRoomType(t scenario.RoomType) string {
	if t == "" {
		return string(scenario.RoomTypeChannel)
	}
	return string(t)
}

// effectiveRoles applies the "empty defaults to [member]" rule from
// docs/spec-room-subscription-seed.md §3.2.
func effectiveRoles(in []string) []string {
	if len(in) == 0 {
		return []string{scenario.RoleMember}
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
