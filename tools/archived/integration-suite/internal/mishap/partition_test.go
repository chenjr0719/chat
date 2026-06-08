package mishap

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPartitionExecutor_HappyPath(t *testing.T) {
	fake := NewFakeChaosEngine()
	e := &partitionExecutor{engine: fake, proxyName: "MongoProxy", duration: 10 * time.Millisecond}
	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}

	require.NoError(t, e.Apply(context.Background(), trigger))
	assert.Equal(t, []string{"Partition(MongoProxy)", "Heal(MongoProxy)"}, fake.Calls)
}

func TestPartitionExecutor_TriggerNeverFires(t *testing.T) {
	fake := NewFakeChaosEngine()
	e := &partitionExecutor{engine: fake, proxyName: "MongoProxy", duration: time.Second}
	trigger := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := e.Apply(ctx, trigger)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, fake.Calls, "no engine calls when trigger never fires")
}

func TestPartitionExecutor_CtxCancelMidPartition(t *testing.T) {
	fake := NewFakeChaosEngine()
	e := &partitionExecutor{engine: fake, proxyName: "MongoProxy", duration: time.Second}
	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err := e.Apply(ctx, trigger)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	// Best-effort Heal on background ctx must fire so we don't leave
	// the proxy disabled across cases.
	assert.Equal(t, []string{"Partition(MongoProxy)", "Heal(MongoProxy)"}, fake.Calls)
}

func TestPartitionExecutor_PartitionFails(t *testing.T) {
	fake := NewFakeChaosEngine()
	boom := errors.New("partition boom")
	fake.ErrOn["Partition(MongoProxy)"] = boom
	e := &partitionExecutor{engine: fake, proxyName: "MongoProxy", duration: 10 * time.Millisecond}
	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}

	err := e.Apply(context.Background(), trigger)
	assert.ErrorIs(t, err, boom)
	// Heal must NOT be called when Partition failed — nothing to heal.
	assert.Equal(t, []string{"Partition(MongoProxy)"}, fake.Calls)
}

func TestPartitionExecutor_HealFails(t *testing.T) {
	fake := NewFakeChaosEngine()
	boom := errors.New("heal boom")
	fake.ErrOn["Heal(MongoProxy)"] = boom
	e := &partitionExecutor{engine: fake, proxyName: "MongoProxy", duration: 10 * time.Millisecond}
	trigger := make(chan struct{}, 1)
	trigger <- struct{}{}

	err := e.Apply(context.Background(), trigger)
	assert.ErrorIs(t, err, boom)
}

func TestPartitionExecutor_CleanupCallsHeal(t *testing.T) {
	fake := NewFakeChaosEngine()
	e := &partitionExecutor{engine: fake, proxyName: "CassandraProxy", duration: 10 * time.Millisecond}

	require.NoError(t, e.Cleanup(context.Background()))
	assert.Equal(t, []string{"Heal(CassandraProxy)"}, fake.Calls)
}

func TestMongoPartitionFactory_RegistersAndBuilds(t *testing.T) {
	r := NewRegistry()
	RegisterMongoPartition(r)

	f, err := r.GetFactory("MongoPartitionFactory")
	require.NoError(t, err)

	exec, err := f(FactoryContext{ChaosEngine: NewFakeChaosEngine()}, "some-pod")
	require.NoError(t, err)
	assert.NotNil(t, exec)
}

func TestCassandraPartitionFactory_RegistersAndBuilds(t *testing.T) {
	r := NewRegistry()
	RegisterCassandraPartition(r)

	f, err := r.GetFactory("CassandraPartitionFactory")
	require.NoError(t, err)

	exec, err := f(FactoryContext{ChaosEngine: NewFakeChaosEngine()}, "some-pod")
	require.NoError(t, err)
	assert.NotNil(t, exec)
}
