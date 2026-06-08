package mishap

import "fmt"

// Registry holds named Factories registered at runner startup. The
// validator cross-checks every mishap catalog YAML's `factory:` name
// against this registry. Each case explicitly names its mishap
// kind — there's no Cartesian eligibility filtering or predicate
// machinery.
//
// Registry is NOT safe for concurrent Register / Get calls. The
// intended pattern is: all Register* happen once at runner startup
// (from a single goroutine), before any Get is called.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns an empty Registry. Concrete kinds register
// themselves via RegisterCrash / RegisterMongoPartition /
// RegisterCassandraPartition at runner startup.
func NewRegistry() *Registry {
	return &Registry{factories: map[string]Factory{}}
}

// RegisterFactory adds or replaces a named Factory.
func (r *Registry) RegisterFactory(name string, f Factory) {
	r.factories[name] = f
}

// GetFactory looks up a Factory by name.
func (r *Registry) GetFactory(name string) (Factory, error) {
	f, ok := r.factories[name]
	if !ok {
		return nil, fmt.Errorf("mishap: unknown factory %q", name)
	}
	return f, nil
}
