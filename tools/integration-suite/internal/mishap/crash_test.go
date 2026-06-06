package mishap

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCrashExecutor_FiresOnTriggerThenRestarts(t *testing.T) {
	fake := NewFakeDockerCLI()
	e := &crashExecutor{docker: fake, target: "room-worker"}
	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}

	ctx := context.Background()
	assert.NoError(t, e.Apply(ctx, trigger))
	assert.Equal(t, []string{"Restart(room-worker)"}, fake.Calls)
}

func TestCrashExecutor_CleanupIsIdempotent(t *testing.T) {
	fake := NewFakeDockerCLI()
	e := &crashExecutor{docker: fake, target: "room-worker"}

	assert.NoError(t, e.Cleanup(context.Background()))
	assert.Equal(t, []string{"StartIfStopped(room-worker)"}, fake.Calls)
}

func TestCrashExecutor_TriggerNeverFiresReturnsContextErr(t *testing.T) {
	fake := NewFakeDockerCLI()
	e := &crashExecutor{docker: fake, target: "room-worker"}
	trigger := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := e.Apply(ctx, trigger)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, fake.Calls, "no docker calls when trigger never fires")
}

func TestCrashFactory_RegistersAndProducesExecutor(t *testing.T) {
	r := NewRegistry()
	RegisterCrash(r)

	f, err := r.GetFactory("CrashFactory")
	assert.NoError(t, err)

	fctx := FactoryContext{DockerCLI: NewFakeDockerCLI()}
	e, err := f(fctx, "room-worker")
	assert.NoError(t, err)
	assert.NotNil(t, e)
}
