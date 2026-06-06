package mishap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExecutor_InterfaceShape(t *testing.T) {
	// Compile-time check: anything matching this signature satisfies Executor.
	var _ Executor = (*nullExecutor)(nil)
	e := &nullExecutor{}
	assert.NoError(t, e.Apply(context.Background(), nil))
	assert.NoError(t, e.Cleanup(context.Background()))
}

type nullExecutor struct{}

func (*nullExecutor) Apply(context.Context, <-chan struct{}) error { return nil }
func (*nullExecutor) Cleanup(context.Context) error                { return nil }
