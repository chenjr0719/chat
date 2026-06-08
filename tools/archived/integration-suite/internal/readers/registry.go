package readers

import "fmt"

// Registry holds named Reader instances and provides lookup by name.
type Registry struct {
	m map[string]Reader
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry { return &Registry{m: map[string]Reader{}} }

// Register adds a reader under the given name, replacing any previous entry.
func (r *Registry) Register(name string, reader Reader) { r.m[name] = reader }

// Get returns the reader registered under name, or an error if not found.
func (r *Registry) Get(name string) (Reader, error) {
	rd, ok := r.m[name]
	if !ok {
		return nil, fmt.Errorf("readers: unknown reader %q", name)
	}
	return rd, nil
}

// All returns every registered (name, reader) for the observer to iterate.
func (r *Registry) All() map[string]Reader {
	out := map[string]Reader{}
	for k, v := range r.m {
		out[k] = v
	}
	return out
}
