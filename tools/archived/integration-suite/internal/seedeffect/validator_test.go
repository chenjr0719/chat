package seedeffect

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateFlags_AllKnownPasses(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r)
	err := ValidateFlags(r, map[string]map[string]bool{
		"alice":    {"verified": true},
		"eve":      {"verified": false},
		"no_flags": {},
	})
	require.NoError(t, err)
}

func TestValidateFlags_UnknownTrueFlagFails(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r)
	err := ValidateFlags(r, map[string]map[string]bool{
		"alice":      {"verified": true},
		"bob_banned": {"banned": true},
	})
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "bob_banned.banned", "error must name the offending alias.flag")
	assert.Contains(t, msg, "verified", "error must list the registered set")
}

func TestValidateFlags_UnknownFalseFlagAllowed(t *testing.T) {
	// `false` is a no-op even for unknown flags — the runner never
	// dispatches them. Allowed so scenarios can document future flags
	// before they're implemented.
	r := NewRegistry()
	RegisterBuiltins(r)
	err := ValidateFlags(r, map[string]map[string]bool{
		"alice": {"verified": true, "admin": false},
	})
	require.NoError(t, err)
}

func TestValidateFlags_NilRegistryRejected(t *testing.T) {
	err := ValidateFlags(nil, map[string]map[string]bool{"alice": {"verified": true}})
	require.Error(t, err)
}

func TestValidateFlags_EmptyInputPasses(t *testing.T) {
	r := NewRegistry()
	RegisterBuiltins(r)
	require.NoError(t, ValidateFlags(r, nil))
	require.NoError(t, ValidateFlags(r, map[string]map[string]bool{}))
}
