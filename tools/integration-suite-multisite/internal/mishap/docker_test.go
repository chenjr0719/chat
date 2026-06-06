package mishap

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFakeDockerCLI_RecordsCalls(t *testing.T) {
	d := NewFakeDockerCLI()
	ctx := context.Background()

	assert.NoError(t, d.Restart(ctx, "room-worker"))
	assert.NoError(t, d.StartIfStopped(ctx, "room-worker"))

	assert.Equal(t, []string{
		"Restart(room-worker)",
		"StartIfStopped(room-worker)",
	}, d.Calls)
}

func TestFakeDockerCLI_ErrorInjection(t *testing.T) {
	d := NewFakeDockerCLI()
	d.ErrOn = map[string]error{
		"Restart(room-worker)": errors.New("docker daemon unreachable"),
	}

	err := d.Restart(context.Background(), "room-worker")
	assert.EqualError(t, err, "docker daemon unreachable")
}

func TestDockerCLI_InterfaceShape(t *testing.T) {
	var _ DockerCLI = (*dockerExec)(nil)
	var _ DockerCLI = (*FakeDockerCLI)(nil)
}
