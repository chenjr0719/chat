package mishap

import "context"

// crashExecutor implements the `crash` kind (§3.2.1): docker restart of
// the target pod, fired when the gather-and-fire trigger completes.
type crashExecutor struct {
	docker DockerCLI
	target string
}

// Apply blocks on trigger then runs docker restart on the target pod.
// Returns ctx.Err() if the trigger never fires (gather timed out).
func (e *crashExecutor) Apply(ctx context.Context, trigger <-chan struct{}) error {
	select {
	case <-trigger:
	case <-ctx.Done():
		return ctx.Err()
	}
	return e.docker.Restart(ctx, e.target)
}

// Cleanup ensures the target pod is up after the case ends. Idempotent.
func (e *crashExecutor) Cleanup(ctx context.Context) error {
	return e.docker.StartIfStopped(ctx, e.target)
}

// RegisterCrash wires the crash kind into a Registry.
func RegisterCrash(r *Registry) {
	r.RegisterFactory("CrashFactory", func(fctx FactoryContext, gatherPoint string) (Executor, error) {
		return &crashExecutor{docker: fctx.DockerCLI, target: gatherPoint}, nil
	})
}
