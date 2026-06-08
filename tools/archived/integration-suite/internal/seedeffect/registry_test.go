package seedeffect

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeEffect struct {
	calls []string
	err   error
}

func (f *fakeEffect) Apply(_ context.Context, u *SeedUser, _ Deps) error {
	f.calls = append(f.calls, u.Account)
	return f.err
}

func TestNewRegistry_IsEmpty(t *testing.T) {
	r := NewRegistry()
	_, err := r.Get("anything")
	require.Error(t, err)
}

func TestRegistry_RegisterAndGet(t *testing.T) {
	r := NewRegistry()
	e := &fakeEffect{}
	r.Register("verified", e)

	got, err := r.Get("verified")
	require.NoError(t, err)
	assert.NotNil(t, got)

	// Apply through the registered handle.
	require.NoError(t, got.Apply(context.Background(), &SeedUser{Account: "alice"}, Deps{}))
	assert.Equal(t, []string{"alice"}, e.calls)
}

func TestRegistry_UnknownFlagNamesAvailableSet(t *testing.T) {
	r := NewRegistry()
	r.Register("verified", &fakeEffect{})
	r.Register("admin", &fakeEffect{})

	_, err := r.Get("banned")
	require.Error(t, err)
	msg := err.Error()
	assert.Contains(t, msg, "banned", "error must name the unknown flag")
	assert.Contains(t, msg, "verified", "error must list available flags")
	assert.Contains(t, msg, "admin", "error must list available flags")
}

func TestRegistry_EffectErrorPropagates(t *testing.T) {
	r := NewRegistry()
	boom := errors.New("auth-service unreachable")
	r.Register("verified", &fakeEffect{err: boom})

	e, _ := r.Get("verified")
	err := e.Apply(context.Background(), &SeedUser{Account: "alice"}, Deps{})
	require.ErrorIs(t, err, boom)
}

func TestSeedUser_ZeroValueFields(t *testing.T) {
	u := SeedUser{Alias: "alice", Account: "alice", ID: "u-alice"}
	assert.Empty(t, u.JWT)
	assert.Empty(t, u.NkeySeed)
}
