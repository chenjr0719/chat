package scenario

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario is the scenario shape. See SCENARIO-REFERENCE.md for the
// field-by-field grammar.
type Scenario struct {
	Name      string    `yaml:"scenario"`
	Source    string    `yaml:"source"`
	Status    string    `yaml:"status,omitempty"`
	Seed      SeedBlock `yaml:"seed,omitempty"`
	BaseInput BaseInput `yaml:"base_input"`
	Cases     []Case    `yaml:"cases"`
}

// SeedBlock declares scenario-local seed data.
//
// Phase 4.2 extension (see docs/spec-room-subscription-seed.md):
// added `Rooms` + `Memberships` to support pre-seeded room
// subscriptions so messaging tests can fire msg.send without the
// gatekeeper rejecting the request for missing subscription data.
//
// Implementation note: the spec's §4.1 sketched the membership data
// as a `member_of` field NESTED inside SeedUserFlags. Step 1 of the
// landing path hoists it to this sibling `Memberships` field instead
// to keep SeedUserFlags as `map[string]bool` and respect the
// "don't touch sandbox.go in Step 1" constraint. PR 2 (sandbox
// wiring) can rename if a different YAML shape is preferred; the
// validation rules, document shapes, and bare-string shorthand are
// identical either way.
type SeedBlock struct {
	Users       map[string]SeedUserFlags    `yaml:"users,omitempty"`
	Rooms       []SeedRoom                  `yaml:"rooms,omitempty"`
	Memberships map[string][]SeedMembership `yaml:"memberships,omitempty"`

	// Phase 4.3 — generic Cassandra row seed primitive. See
	// docs/spec-cassandra-seeding-engine.md. List preserves YAML
	// declaration order so the engine's INSERT order is the same
	// as the author's read order. Empty / nil is the no-op default
	// for every pre-Phase-4.3 scenario.
	CassandraData []SeedCassandraTable `yaml:"cassandra_data,omitempty"`
}

// SeedCassandraTable is one (table, rows) pair under
// seed.cassandra_data. Rows are inserted into Table in declaration
// order (PR 3 — sandbox wiring — issues one Session.Query per row).
type SeedCassandraTable struct {
	Table string             `yaml:"table"`
	Rows  []SeedCassandraRow `yaml:"rows"`
}

// SeedCassandraRow is a (column-name → value) map. Values stay as raw
// YAML scalars (string / int64 / float64 / bool / nil) so token
// strings like "${now - 2m}" and "${bucket(created_at)}" survive
// intact for the substitution layer (cassandra_subst.go) to
// recognise. The runtime layer (PR 3) walks one row twice — Pass 1
// resolves ${now ± d} tokens to time.Time, Pass 2 resolves
// ${bucket(<col>)} against the pass-1 result.
type SeedCassandraRow map[string]any

// SeedUserFlags is a map of effect-flag name → bool. Validation against
// the closed catalogs/seed-effects/ vocabulary happens at scenario load.
//
// Phase 4.2 keeps this as map[string]bool so sandbox.go's existing
// type conversion `map[string]bool(flags)` keeps compiling. Per-user
// non-bool data (memberships) lives on SeedBlock.Memberships, not here.
type SeedUserFlags map[string]bool

// SeedRoom is one pre-seeded room declared at the scenario level.
// One Mongo `rooms` document is materialized per entry at
// Sandbox.Setup. Members are attached via SeedBlock.Memberships,
// keyed by user alias.
//
// Field defaults applied by validation:
//   - Type: "channel" when empty
//   - Name: id (when empty; matches the room-service convention
//     for un-named DM rooms displaying via member HRInfo)
//   - CreatedAt: empty falls back to sb.StartTime at room-doc
//     insertion. Phase 4.5.1 addition — lets read-side scenarios
//     (e.g. history-service pagination) backdate a room's birth so
//     the service's bucket-walk floor sits below pre-seeded
//     messages. Accepts the same ${now ± duration} grammar Phase
//     4.3 introduced for Cassandra-seed timestamps; validated by
//     ValidateSeedBlock's rule 6.
type SeedRoom struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name,omitempty"`
	Type      RoomType `yaml:"type,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`
}

// RoomType is the closed enum mirrored from pkg/model.RoomType.
// Duplicated here (instead of imported) so the scenario package
// stays independent of pkg/model; the runtime side does the same
// (pkg/model/room.go:5-12 is the authoritative source).
type RoomType string

const (
	RoomTypeChannel    RoomType = "channel"
	RoomTypeDM         RoomType = "dm"
	RoomTypeBotDM      RoomType = "botDM"
	RoomTypeDiscussion RoomType = "discussion"
)

// SeedMembership is one (user alias, room id) pair with optional
// roles. The bare-string YAML form ("r-engineering") is sugar for
// the default-member case; the full-object form supports custom
// roles. Both forms decode through the custom UnmarshalYAML below.
//
// Field defaults applied by validation:
//   - Roles: [RoleMember] when empty
type SeedMembership struct {
	Room  string   `yaml:"room"`
	Roles []string `yaml:"roles,omitempty"`
}

// UnmarshalYAML accepts both the bare-string shorthand and the full
// object form. Mixed lists work cleanly because every list element is
// decoded individually:
//
//	memberships:
//	  alice:
//	    - r-engineering                        # bare string → {Room: "r-engineering"}
//	    - room: r-design                       # object → {Room: "r-design"}
//	    - room: r-product                      # object with roles
//	      roles: [owner]
func (m *SeedMembership) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		// Bare string form: the scalar IS the room id; roles stay empty
		// (default-member applied later).
		if node.Value == "" {
			return fmt.Errorf("scenario: empty membership scalar")
		}
		m.Room = node.Value
		m.Roles = nil
		return nil
	case yaml.MappingNode:
		// Full object form. Decode into an anonymous shadow so we don't
		// recurse into this UnmarshalYAML.
		var raw struct {
			Room  string   `yaml:"room"`
			Roles []string `yaml:"roles,omitempty"`
		}
		if err := node.Decode(&raw); err != nil {
			return fmt.Errorf("scenario: parse membership object: %w", err)
		}
		m.Room = raw.Room
		m.Roles = raw.Roles
		return nil
	default:
		return fmt.Errorf("scenario: membership must be string or object, got kind=%d", node.Kind)
	}
}

// Role is the closed enum mirrored from pkg/model.Role. See
// pkg/model/subscription.go:11-20 for the authoritative source.
const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// BaseInput mirrors v2 scenario.Input minus the placeholders block —
// the seed.users block replaces it.
type BaseInput struct {
	Verb       string         `yaml:"verb"`
	Subject    string         `yaml:"subject"`
	Payload    map[string]any `yaml:"payload"`
	Credential string         `yaml:"credential,omitempty"`
}

// Case is one explicit experiment. Cases within a scenario run
// sequentially against the same Sandbox state.
type Case struct {
	Name     string             `yaml:"name"`
	Tag      string             `yaml:"tag"` // "positive" | "negative"
	Input    *CaseInputOverride `yaml:"input,omitempty"`
	Mishap   string             `yaml:"mishap,omitempty"`
	Expected []Expected         `yaml:"expected"`
}

// CaseInputOverride is the shallow-merge override for base_input.
// Pointer fields distinguish "unset" (nil) from "explicit empty string".
type CaseInputOverride struct {
	Subject    *string        `yaml:"subject,omitempty"`
	Payload    map[string]any `yaml:"payload,omitempty"`
	Credential *string        `yaml:"credential,omitempty"`
}

// Expected is one Gomega assertion.
//
//	Default semantic: poll Location every Polling for an event matching
//	Match, succeed on first hit, fail on Timeout.
//	Not=true: use Consistently(...).ShouldNot(...) instead — negative
//	assertion (the matching event MUST NOT happen).
//
// Args carries primitive-poller parameters (the table for
// `cassandra_select`, the stream + filter_subject for
// `jetstream_consume`, the collection + filter for `mongo_find`, the
// container for `logs_tail`). Substitution tokens inside args
// resolve via the same path as Match (Phase A + Phase B). Pollers
// that need no args (e.g. `reply`) ignore the field.
type Expected struct {
	Location string         `yaml:"location"`
	Args     map[string]any `yaml:"args,omitempty"`
	Match    map[string]any `yaml:"match"`
	Timeout  Duration       `yaml:"timeout,omitempty"`
	Polling  Duration       `yaml:"polling,omitempty"`
	Not      bool           `yaml:"not,omitempty"`
}

// Duration is a time.Duration with a YAML unmarshaler that accepts
// Go duration strings ("5s", "100ms", "1m"). A missing or empty field
// decodes to 0; callers supply defaults at the use site (RunCase).
type Duration time.Duration

// UnmarshalYAML accepts scalar duration strings. An empty/missing
// scalar yields zero (the caller's default kicks in).
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Value == "" {
		*d = 0
		return nil
	}
	parsed, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("scenario: parse duration %q: %w", value.Value, err)
	}
	*d = Duration(parsed)
	return nil
}
