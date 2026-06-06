// Package catalog holds the Go types and loader for the YAML catalogs
// under <repo-root>/catalogs/. Each type corresponds 1:1 to a YAML file.
package catalog

import (
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// Catalog is the in-memory union of every YAML file under catalogs/.
type Catalog struct {
	Verbs       []Verb
	Readers     []Reader
	Services    []Service
	Matchers    []Matcher
	Mishaps     []MishapDecl
	SeedEffects []SeedEffectDecl // Phase 3: closed catalog of seed-effect flags
}

// SeedEffectDecl is one catalogs/seed-effects/<flag>.yaml entry.
// Loaded by the runner at startup and used by both the scenario-load
// validator (rejecting unknown flags) and operator-facing documentation.
//
// The Effect Go implementation is registered separately in
// internal/seedeffect/builtins.go's RegisterBuiltins by name — the
// declared go_func: must match the Go-side Register(name, …) call.
type SeedEffectDecl struct {
	Effect           string   `yaml:"effect"`
	Description      string   `yaml:"description"`
	GoFunc           string   `yaml:"go_func"`
	RequiredServices []string `yaml:"required_services"`
}

// MishapDecl is one catalogs/mishaps/<kind>.yaml entry. Loaded by the
// runner at startup and consumed by buildFactoryByKind to populate the
// per-case mishap → factory lookup. The other YAML fields (axis,
// trigger, class, eligible, produces_noise) were v2 expander metadata
// and are no longer parsed by Phase 3.
type MishapDecl struct {
	Name    string `yaml:"mishap"`
	Factory string `yaml:"factory"`
}

// Verb describes one interface-level primitive (the verb catalog entries).
// `input_shape` and `transport_effect_template` were v2 documentation
// metadata; Phase 3 only needs Name + Executor.
type Verb struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Executor    string `yaml:"executor"`
}

// Reader describes one bounded observation location.
type Reader struct {
	Location        string        `yaml:"location"`
	Owners          []string      `yaml:"owners"`
	TimestampSource string        `yaml:"timestamp_source"`
	Shape           string        `yaml:"shape"`
	Executor        string        `yaml:"executor"`
	Events          []ReaderEvent `yaml:"events"`
	Ignore          []string      `yaml:"ignore"`
}

// ReaderEvent declares one classification rule for a reader.
// Readers evaluate Ignore first (drop on match), then Events
// first-match-wins (emit with the matched Type), then emit Type
// "unmatched" if nothing else matched.
type ReaderEvent struct {
	Pattern string            `yaml:"pattern"` // regex matched against event payload
	Type    readers.EventType `yaml:"type"`    // Type is one of readers.Event{Cascade,Failure,RestartNoise,DisconnectNoise,Background}.
}

// Service captures a service's container metadata. `mishap_eligible`
// and `triggers` were v2 expander metadata and are no longer parsed.
type Service struct {
	Name      string `yaml:"service"`
	Container string `yaml:"container"`
}

// Matcher is one named predicate function.
type Matcher struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}
