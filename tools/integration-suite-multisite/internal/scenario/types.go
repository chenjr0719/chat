package scenario

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// Scenario is one test unit: one input fire + one set of assertions.
// No cases, no base_input/case.input distinction. Variants are
// separate scenario files.
type Scenario struct {
	Name           string               `yaml:"scenario"`
	Source         string               `yaml:"source"`
	Status         string               `yaml:"status,omitempty"`
	Tag            string               `yaml:"tag"` // "positive" | "negative"
	Sites          map[string]SiteBlock `yaml:"sites"`
	CassandraData  []SeedCassandraTable `yaml:"cassandra_data,omitempty"`
	PreFireScripts []string             `yaml:"pre_fire_scripts,omitempty"`
	Input          Input                `yaml:"input"`
	Expected       []Expected           `yaml:"expected"`

	// SourcePath is the absolute path of the YAML file this Scenario
	// was loaded from. Populated by LoadFile; not in the YAML itself.
	// Used by runScenario to resolve PreFireScripts paths relative to
	// the scenario file and to set the script working directory.
	SourcePath string `yaml:"-"`
}

// SiteBlock is the per-site seed data — exactly the single-site
// SeedBlock, but nested under sites.<site>.
type SiteBlock struct {
	Seed SiteSeed `yaml:"seed"`
}

// SiteSeed mirrors single-site's SeedBlock minus cassandra_data
// (which moves to scenario top level since Cassandra is shared).
//
// RemoteUsers declares users whose IDENTITY was minted by another
// site's seed.users block but whose user profile must also exist HERE
// (with siteId = home_site) so production code on this site can
// classify them as remote — required for cross-site federation tests.
// The author is explicit about which aliases to project, on which
// site, with which home_site. The engine writes exactly that and
// nothing more.
type SiteSeed struct {
	Users       map[string]SeedUserFlags    `yaml:"users,omitempty"`
	RemoteUsers map[string]SeedRemoteUser   `yaml:"remote_users,omitempty"`
	Rooms       []SeedRoom                  `yaml:"rooms,omitempty"`
	Memberships map[string][]SeedMembership `yaml:"memberships,omitempty"`
}

// SeedRemoteUser declares a remote-user stub. The alias MUST also
// appear in the home_site's seed.users block — that's where the user
// is minted; this block only projects a user-profile document into
// THIS site's `users` collection with siteId = home_site so the
// production data model on THIS site can see the user as remote.
type SeedRemoteUser struct {
	HomeSite string `yaml:"home_site"`
}

// SeedCassandraTable, SeedCassandraRow, SeedUserFlags, SeedRoom,
// RoomType, SeedMembership, role constants — preserved verbatim from
// single-site so the seed engine is identical.

type SeedCassandraTable struct {
	Table string             `yaml:"table"`
	Rows  []SeedCassandraRow `yaml:"rows"`
}

type SeedCassandraRow map[string]any

type SeedUserFlags map[string]bool

type SeedRoom struct {
	ID        string   `yaml:"id"`
	Name      string   `yaml:"name,omitempty"`
	Type      RoomType `yaml:"type,omitempty"`
	CreatedAt string   `yaml:"created_at,omitempty"`
}

type RoomType string

const (
	RoomTypeChannel    RoomType = "channel"
	RoomTypeDM         RoomType = "dm"
	RoomTypeBotDM      RoomType = "botDM"
	RoomTypeDiscussion RoomType = "discussion"
)

type SeedMembership struct {
	Room  string   `yaml:"room"`
	Roles []string `yaml:"roles,omitempty"`
}

func (m *SeedMembership) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Value == "" {
			return fmt.Errorf("scenario: empty membership scalar")
		}
		m.Room = node.Value
		m.Roles = nil
		return nil
	case yaml.MappingNode:
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

const (
	RoleOwner  = "owner"
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Input is the scenario's single fire.
type Input struct {
	Site       string         `yaml:"site"`
	Verb       string         `yaml:"verb"`
	Subject    string         `yaml:"subject"`
	Payload    map[string]any `yaml:"payload"`
	Credential string         `yaml:"credential,omitempty"`
}

// Expected is one assertion.
type Expected struct {
	Location string         `yaml:"location"`
	Site     string         `yaml:"site,omitempty"` // required for site-scoped pollers, forbidden for reply/cassandra_select
	Args     map[string]any `yaml:"args,omitempty"`
	Match    map[string]any `yaml:"match"`
	Timeout  Duration       `yaml:"timeout,omitempty"`
	Polling  Duration       `yaml:"polling,omitempty"`
	Not      bool           `yaml:"not,omitempty"`
}

// Duration unchanged from single-site.
type Duration time.Duration

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
