package seed

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodedRoomKeys_ReturnsList(t *testing.T) {
	ids, err := DecodedRoomKeys()
	require.NoError(t, err)
	assert.NotEmpty(t, ids, "room-keys.json must declare at least one roomID")
	for _, id := range ids {
		assert.NotEmpty(t, id, "every roomID in room-keys.json must be non-empty")
	}
}
