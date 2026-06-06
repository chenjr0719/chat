// Package seedeffect holds the closed catalog of scenario-local
// seed-data mutations. Each effect-flag in a scenario's
// `seed.users.<alias>` block resolves to an Effect implementation
// registered in this package. Unknown flags fail at scenario load.
package seedeffect

import (
	"context"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Effect applies one flag's mutation to a freshly-minted seedUser.
// Called per (user, flag) pair where the flag's value is true. Returns
// non-nil error to abort sandbox setup.
//
// Effects share an immutable Deps bundle (resource handles); the
// SeedUser is mutated in place (fields like JWT / NkeySeed populated
// by VerifiedEffect).
type Effect interface {
	Apply(ctx context.Context, u *SeedUser, deps Deps) error
}

// SeedUser is the materialized scenario-local user. Alias is the YAML
// key (e.g. "alice"); Account is the materialized account name (equal
// to Alias today); ID is the Mongo _id ("u-" + Account). JWT and
// NkeySeed are minted unconditionally by Sandbox.Setup (Phase 3.8 split
// — every seeded user gets a NATS credential, regardless of the
// `verified` flag). Verified is true iff VerifiedEffect ran for this
// user; it's persisted into the Mongo user-profile doc so scenarios
// can target real backend logic instead of "no credential" rejection.
type SeedUser struct {
	Alias    string
	Account  string
	ID       string
	JWT      string
	NkeySeed string
	Verified bool
}

// Deps is the immutable resource bundle every effect receives. Adding
// a new effect that needs a new resource → add a field here and wire
// it from cmd/runner/main.go via runner.go's seedEffect.Deps build site.
type Deps struct {
	AuthURL string          // auth-service base URL; used by VerifiedEffect to POST /auth
	Mongo   *mongo.Database // chat database handle; future effects writing to Mongo use it
}

// Registry maps effect-flag name → Effect implementation. Built once
// at runner startup; consulted at every sandbox Setup.
type Registry struct {
	effects map[string]Effect
}

// NewRegistry returns an empty Registry. RegisterBuiltins (Task 7)
// wires in the production effects.
func NewRegistry() *Registry {
	return &Registry{effects: map[string]Effect{}}
}

// Register adds or replaces an effect under the given flag name.
// Re-registration of the same name is allowed (idempotent for test
// harnesses that re-build the registry).
func (r *Registry) Register(name string, e Effect) {
	r.effects[name] = e
}

// Get returns the registered effect for the named flag. An unknown
// flag returns an error naming the unknown flag AND the available
// set (sorted) so the operator sees both immediately.
func (r *Registry) Get(name string) (Effect, error) {
	if e, ok := r.effects[name]; ok {
		return e, nil
	}
	available := make([]string, 0, len(r.effects))
	for k := range r.effects {
		available = append(available, k)
	}
	sort.Strings(available)
	return nil, fmt.Errorf("seedeffect: unknown flag %q; available: %v", name, available)
}

// Names returns the registered flag names in sorted order. Used by
// the scenario-load validator to check a scenario's flags against the
// catalog (Task 7).
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.effects))
	for k := range r.effects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
