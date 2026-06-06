package mishap

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFakeChaosEngine_RecordsCalls(t *testing.T) {
	f := NewFakeChaosEngine()
	ctx := context.Background()

	assert.NoError(t, f.Partition(ctx, "MongoProxy"))
	assert.NoError(t, f.Heal(ctx, "MongoProxy"))
	assert.NoError(t, f.Reset(ctx))

	assert.Equal(t, []string{
		"Partition(MongoProxy)",
		"Heal(MongoProxy)",
		"Reset()",
	}, f.Calls)
}

func TestFakeChaosEngine_ErrOn(t *testing.T) {
	f := NewFakeChaosEngine()
	boom := errors.New("boom")
	f.ErrOn["Partition(MongoProxy)"] = boom

	err := f.Partition(context.Background(), "MongoProxy")
	assert.ErrorIs(t, err, boom)

	assert.NoError(t, f.Heal(context.Background(), "CassandraProxy"))
	assert.Equal(t, []string{"Partition(MongoProxy)", "Heal(CassandraProxy)"}, f.Calls)
}
