// Package mishap holds the perturbation catalog and the typed
// Executor system that runs perturbations during a single test case.
//
// Ships three event-triggered kinds:
//
//	crash                       — docker restart of a target pod
//	mongo-partition-500ms       — toxiproxy disable MongoProxy 500ms
//	cassandra-partition-500ms   — toxiproxy disable CassandraProxy 500ms
//
// Each case is its own sequence-level fire point and fires the
// trigger immediately — there is no Cartesian case-grid expander or
// gather-and-fire trigger logic. See ARCHITECTURE.md §5 for the
// chaos-engine model.
package mishap

import "context"

// Executor performs a perturbation during a single test case.
//
// Apply BLOCKS on its trigger channel; the case runner closes the
// trigger immediately so the perturbation fires as soon as Apply is
// scheduled. Apply runs in a goroutine alongside the case's
// assertion loop; Cleanup runs at end of case via defer.
//
// Cleanup MUST be idempotent — multiple Apply goroutines may share an
// underlying docker / proxy action.
type Executor interface {
	Apply(ctx context.Context, trigger <-chan struct{}) error
	Cleanup(ctx context.Context) error
}

// Factory builds an Executor for one case. The gatherPoint argument
// is preserved for Phase 1 partition/crash callsite compatibility —
// crash uses it (target pod name); partition kinds ignore it.
type Factory func(fctx FactoryContext, gatherPoint string) (Executor, error)

// FactoryContext is the slice of per-case state every Factory needs.
// DockerCLI powers crash; ChaosEngine powers the partition family.
//
// Keep the struct concrete (not interface{}) so future fields are
// reviewable diffs.
type FactoryContext struct {
	DockerCLI   DockerCLI   // crash
	ChaosEngine ChaosEngine // partition kinds
}

// DockerCLI is the toolkit primitive for the `crash` mishap.
// Phase 1 scoped this down from a pause/network/restart surface to
// just restart-family — network faults moved to ChaosEngine.
// Defined here so FactoryContext can reference it without an
// import cycle.
type DockerCLI interface {
	// Restart is the active perturbation — docker restart <container>.
	Restart(ctx context.Context, container string) error
	// StartIfStopped is the Cleanup safety net. Idempotent.
	StartIfStopped(ctx context.Context, container string) error
}
