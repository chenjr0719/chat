package seedeffect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegisterBuiltins_RegistersVerified(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r)

	e, err := r.Get("verified")
	require.NoError(t, err)
	assert.NotNil(t, e)
}

func TestRegisterBuiltins_OnlyVerifiedRegistered(t *testing.T) {
	// Option B ruling 2026-05-31: BannedEffect deferred; verified is
	// the sole production effect in Phase 3.
	r := NewRegistry()
	RegisterBuiltins(r)
	assert.Equal(t, []string{"verified"}, r.Names())
}
