package matchers

import "fmt"

// Registry maps matcher names to Matcher implementations.
type Registry struct {
	m map[string]Matcher
}

// NewRegistry returns a Registry pre-populated with the only matcher
// the runtime actually retrieves by name: matches_shape (consumed by
// runtime/matchshape.go's MatchShape wrapper). Other matcher types
// can be registered by callers if needed; nothing else ships
// pre-registered today.
func NewRegistry() *Registry {
	r := &Registry{m: map[string]Matcher{}}
	r.Register("matches_shape", MatchesShape{})
	return r
}

// Register adds or replaces a named matcher.
func (r *Registry) Register(name string, m Matcher) { r.m[name] = m }

// Get retrieves a matcher by name, returning an error if not found.
func (r *Registry) Get(name string) (Matcher, error) {
	m, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("matchers: unknown matcher %q", name)
	}
	return m, nil
}
