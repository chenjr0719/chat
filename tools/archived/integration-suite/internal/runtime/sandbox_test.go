package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
)

func TestNewSandbox_AttachesScenarioWithEmptyUsers(t *testing.T) {
	s := &scenario.Scenario{
		Name:   "test",
		Source: "doc#x",
		Seed: scenario.SeedBlock{
			Users: map[string]scenario.SeedUserFlags{
				"alice": {"verified": true},
			},
		},
	}

	sb := NewSandbox(s, &SandboxDeps{})
	require.NotNil(t, sb)
	assert.Equal(t, s, sb.Scenario)
	assert.Empty(t, sb.Users, "users map starts empty; populated by Setup")
	assert.Empty(t, sb.Placeholders, "placeholders empty until Setup")
}

func TestNewSandbox_NilScenarioReturnsNil(t *testing.T) {
	// Defensive — defer sb.Teardown patterns after a failed setup
	// must not panic.
	assert.Nil(t, NewSandbox(nil, &SandboxDeps{}))
}

func TestSandboxTeardown_CallsChaosReset(t *testing.T) {
	chaos := mishap.NewFakeChaosEngine()
	sb := NewSandbox(&scenario.Scenario{Name: "x"}, &SandboxDeps{Chaos: chaos})
	sb.Teardown(context.Background())
	assert.Contains(t, chaos.Calls, "Reset()")
}

func TestSandboxTeardown_IdempotentSecondCallIsNoOp(t *testing.T) {
	chaos := mishap.NewFakeChaosEngine()
	sb := NewSandbox(&scenario.Scenario{Name: "x"}, &SandboxDeps{Chaos: chaos})
	sb.Teardown(context.Background())
	sb.Teardown(context.Background())
	count := 0
	for _, c := range chaos.Calls {
		if c == "Reset()" {
			count++
		}
	}
	assert.Equal(t, 1, count, "Reset called exactly once across two Teardown calls")
}

func TestSandboxTeardown_NilReceiverIsNoOp(t *testing.T) {
	var sb *Sandbox
	assert.NotPanics(t, func() { sb.Teardown(context.Background()) })
}

func TestSandboxTeardown_NilChaosIsNoOp(t *testing.T) {
	sb := NewSandbox(&scenario.Scenario{Name: "x"}, &SandboxDeps{Chaos: nil})
	assert.NotPanics(t, func() { sb.Teardown(context.Background()) })
}

// TestSandboxSetup_PopulatesPollerRegistry asserts that Setup builds a
// pollers.Registry on the sandbox. With all reader handles nil + Mongo
// nil, no pollers are actually registered but the registry must still
// be non-nil so RunCase can call Get on it (and surface the
// "available: []" hint when the scenario references an unregistered
// location).
func TestSandboxSetup_PopulatesPollerRegistry(t *testing.T) {
	s := &scenario.Scenario{Name: "x"}
	reg := seedeffect.NewRegistry()
	sb := NewSandbox(s, &SandboxDeps{
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})

	require.NoError(t, sb.Setup(context.Background()))
	require.NotNil(t, sb.PollerReg, "Setup must construct a poller registry")

	// No readers/Mongo were provided, so Get on any location returns the
	// "available locations" error from registry.Get.
	_, err := sb.PollerReg.Get("mongo.rooms")
	require.Error(t, err)
}

// TestSandboxSetup_RoomsAndMembershipsEmptyIsNoOp confirms the
// pre-Phase-4.2 scenario shape (no seed.Rooms / no seed.Memberships)
// drives Setup cleanly without invoking insertSeededRooms — the
// eight existing scenarios MUST keep passing.
func TestSandboxSetup_RoomsAndMembershipsEmptyIsNoOp(t *testing.T) {
	s := &scenario.Scenario{
		Name: "phase4-empty-rooms",
		Seed: scenario.SeedBlock{
			Users: map[string]scenario.SeedUserFlags{"alice": {"verified": true}},
		},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})
	require.NoError(t, sb.Setup(context.Background()))
	assert.False(t, sb.StartTime.IsZero(), "StartTime must be set even when no rooms are declared")
}

// TestSandboxSetup_MongoNilSkipsRoomSeed pins the nil-tolerant gate so
// scenario YAMLs that declare seed.Rooms still drive Setup cleanly when
// the runner is run without a real Mongo (unit-test deps). insertSeededRooms
// is short-circuited via the `Deps.Mongo != nil` guard.
func TestSandboxSetup_MongoNilSkipsRoomSeed(t *testing.T) {
	s := &scenario.Scenario{
		Name: "phase4-mongo-nil",
		Seed: scenario.SeedBlock{
			Rooms: []scenario.SeedRoom{{ID: "r-engineering", Name: "Engineering", Type: scenario.RoomTypeChannel}},
			Memberships: map[string][]scenario.SeedMembership{
				"alice": {{Room: "r-engineering", Roles: []string{scenario.RoleOwner}}},
			},
			Users: map[string]scenario.SeedUserFlags{"alice": {"verified": true}},
		},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
		// Mongo intentionally nil — Step 6 must skip cleanly.
	})
	require.NoError(t, sb.Setup(context.Background()))
}

// TestSandboxSetup_InvalidSeedBlockRejected confirms ValidateSeedBlock
// is wired into Setup's Step 1b: a duplicate room id surfaces as a
// wrapped error before any Mongo write is attempted.
func TestSandboxSetup_InvalidSeedBlockRejected(t *testing.T) {
	s := &scenario.Scenario{
		Name: "phase4-bad-seed",
		Seed: scenario.SeedBlock{
			Rooms: []scenario.SeedRoom{
				{ID: "r-engineering"},
				{ID: "r-engineering"}, // duplicate → rule 1
			},
		},
	}
	reg := seedeffect.NewRegistry()
	seedeffect.RegisterBuiltins(reg)
	sb := NewSandbox(s, &SandboxDeps{
		Chaos:         mishap.NewFakeChaosEngine(),
		SeedEffectReg: reg,
	})
	err := sb.Setup(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "sandbox.Setup: validate seed block")
	assert.Contains(t, err.Error(), `duplicate id "r-engineering"`)
}

// TestSandboxTeardown_RunsPollerCleanup asserts pollerCleanup is
// invoked exactly once across multiple Teardown calls (sync.Once
// guards the cleanup as well as the chaos reset).
func TestSandboxTeardown_RunsPollerCleanup(t *testing.T) {
	sb := NewSandbox(&scenario.Scenario{Name: "x"}, &SandboxDeps{Chaos: nil})
	calls := 0
	sb.pollerCleanup = func() { calls++ }
	sb.Teardown(context.Background())
	sb.Teardown(context.Background())
	assert.Equal(t, 1, calls, "pollerCleanup must run exactly once")
}
