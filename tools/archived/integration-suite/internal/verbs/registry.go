package verbs

import "fmt"

// Registry maps a verb name (matching `executor:` in YAML) to its Executor.
type Registry struct {
	m map[string]Executor
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{m: map[string]Executor{}}
}

// Register stores an executor under a name. Idempotent: replaces.
func (r *Registry) Register(name string, e Executor) {
	r.m[name] = e
}

// Get returns the executor or an error if name is unknown.
func (r *Registry) Get(name string) (Executor, error) {
	e, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("verbs: unknown executor %q", name)
	}
	return e, nil
}
