package readers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEventType_Constants(t *testing.T) {
	assert.Equal(t, EventType("cascade"), EventCascade)
	assert.Equal(t, EventType("failure"), EventFailure)
	assert.Equal(t, EventType("restart_noise"), EventRestartNoise)
	assert.Equal(t, EventType("disconnect_noise"), EventDisconnectNoise)
	assert.Equal(t, EventType("background"), EventBackground)
	assert.Equal(t, EventType("unmatched"), EventUnmatched)
}
