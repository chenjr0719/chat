package mishap

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewRegistry_StartsEmpty(t *testing.T) {
	r := NewRegistry()
	_, err := r.GetFactory("crash")
	assert.Error(t, err)
}

func TestRegistry_RegisterAndGetFactory(t *testing.T) {
	r := NewRegistry()
	var called bool
	f := Factory(func(_ FactoryContext, _ string) (Executor, error) {
		called = true
		return nil, nil
	})

	r.RegisterFactory("crash", f)

	got, err := r.GetFactory("crash")
	assert.NoError(t, err)

	_, _ = got(FactoryContext{}, "")
	assert.True(t, called, "registered factory should be the one returned")
}

func TestRegistry_GetUnknownReturnsError(t *testing.T) {
	r := NewRegistry()
	_, err := r.GetFactory("nonexistent")
	assert.ErrorContains(t, err, "nonexistent")
}

func TestRegistry_ReregisterReplaces(t *testing.T) {
	r := NewRegistry()
	r.RegisterFactory("crash", func(FactoryContext, string) (Executor, error) {
		return nil, mkErr("first")
	})
	r.RegisterFactory("crash", func(FactoryContext, string) (Executor, error) {
		return nil, mkErr("second")
	})

	f, _ := r.GetFactory("crash")
	_, err := f(FactoryContext{}, "")
	assert.EqualError(t, err, "second")
}

// mkErr is a tiny helper that wraps a string as an error for test fixtures.
func mkErr(s string) error { return testErr(s) }

type testErr string

func (e testErr) Error() string { return string(e) }
