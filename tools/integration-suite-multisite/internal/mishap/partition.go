package mishap

import (
	"context"
	"time"
)

// partitionExecutor implements the network-partition family
// (mongo-partition-500ms, cassandra-partition-500ms). Disables the
// named Toxiproxy proxy for `duration` after the gather-and-fire
// trigger; re-enables it before returning.
type partitionExecutor struct {
	engine    ChaosEngine
	proxyName string
	duration  time.Duration
}

// Apply blocks on the trigger, partitions the named proxy for
// `duration`, then heals it. On ctx cancel mid-window, best-effort
// heal on context.Background() so the proxy doesn't stay disabled
// past the case boundary.
func (e *partitionExecutor) Apply(ctx context.Context, trigger <-chan struct{}) error {
	select {
	case <-trigger:
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := e.engine.Partition(ctx, e.proxyName); err != nil {
		return err
	}
	select {
	case <-time.After(e.duration):
	case <-ctx.Done():
		_ = e.engine.Heal(context.Background(), e.proxyName)
		return ctx.Err()
	}
	return e.engine.Heal(ctx, e.proxyName)
}

// Cleanup defensively heals the proxy. Idempotent — Heal on an
// already-enabled proxy is a no-op at the toxiproxy admin layer.
// Suite-level Reset between cases (runner_phases.go) is the second
// backstop.
func (e *partitionExecutor) Cleanup(ctx context.Context) error {
	return e.engine.Heal(ctx, e.proxyName)
}

// RegisterMongoPartition wires the mongo-partition-500ms kind into
// the registry. The factory ignores its gather-point argument — the
// Toxiproxy proxy name is fixed; the gather point only determines
// WHEN the trigger fires.
func RegisterMongoPartition(r *Registry) {
	r.RegisterFactory("MongoPartitionFactory", func(fctx FactoryContext, _ string) (Executor, error) {
		return &partitionExecutor{
			engine:    fctx.ChaosEngine,
			proxyName: "MongoProxy",
			duration:  500 * time.Millisecond,
		}, nil
	})
}

// RegisterCassandraPartition wires the cassandra-partition-500ms
// kind into the registry. Same shape as Mongo, different proxy.
func RegisterCassandraPartition(r *Registry) {
	r.RegisterFactory("CassandraPartitionFactory", func(fctx FactoryContext, _ string) (Executor, error) {
		return &partitionExecutor{
			engine:    fctx.ChaosEngine,
			proxyName: "CassandraProxy",
			duration:  500 * time.Millisecond,
		}, nil
	})
}
