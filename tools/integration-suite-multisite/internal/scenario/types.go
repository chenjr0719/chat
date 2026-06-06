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
	Name          string               `yaml:"scenario"`
	Source        string               `yaml:"source"`
	Status        string               `yaml:"status,omitempty"`
	Tag           string               `yaml:"tag"` // "positive" | "negative"
	Sites         map[string]SiteBlock `yaml:"sites"`
	CassandraData []SeedCassandraTable `yaml:"cassandra_data,omitempty"`
	Input         Input                `yaml:"input"`
	Expected      []Expected           `yaml:"expected"`
}

// SiteBlock is the per-site seed data — exactly the single-site
// SeedBlock, but nested under sites.<site>.
type SiteBlock struct {
	Seed SiteSeed `yaml:"seed"`
}

// SiteSeed mirrors single-site's SeedBlock minus cassandra_data
// (which moves to scenario top level since Cassandra is shared).
type SiteSeed struct {
	Users       map[string]SeedUserFlags    `yaml:"users,omitempty"`
	Rooms       []SeedRoom                  `yaml:"rooms,omitempty"`
	Memberships map[string][]SeedMembership `yaml:"memberships,omitempty"`
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
