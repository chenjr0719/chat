package pollers

import (
	"log/slog"
	"sync"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// LogsTailPoller is the universal `logs_tail` primitive. Per-poll
// args (parsed each call):
//
//	args.container  string  required — Docker container name to tail
//	args.service    string  optional — owner attribution string;
//	                                   defaults to the container name
//
// Stateful: each unique container is tailed by exactly one underlying
// ContainerLogsReader + StreamPoller pair. First PollFn call for a
// container opens the tail goroutine; subsequent calls (same container)
// reuse the shared buffer. Cleanup closes every cached tail.
//
// Phase 4.0 universal-primitive design: no service-name knowledge
// in Go. The same primitive serves logs.room-service, logs.room-worker,
// logs.message-worker, logs.<any-future-service> from YAML alone.
type LogsTailPoller struct {
	startTime time.Time

	mu    sync.Mutex
	cache map[string]*StreamPoller
}

// NewLogsTailPoller builds the singleton primitive. Register under
// "logs_tail".
func NewLogsTailPoller(startTime time.Time) *LogsTailPoller {
	return &LogsTailPoller{
		startTime: startTime,
		cache:     map[string]*StreamPoller{},
	}
}

// PollFn returns a closure that polls the per-container log buffer.
// First call for a given container opens the tail goroutine; the
// underlying StreamPoller fills the buffer concurrently with the
// Gomega Eventually loop.
//
// site is appended to args["container"] when non-empty to form the
// resolved container name (e.g. "room-service" + "site-a" →
// "room-service-site-a"). This keeps YAML scenarios site-agnostic —
// the site on the expected block drives routing without requiring the
// author to hard-code site suffixes in every container arg. If
// args["container"] already carries a site suffix, the author should
// leave site blank in the expected block to avoid double-suffixing.
// Cache key is the resolved container name.
func (p *LogsTailPoller) PollFn(site string, args map[string]any, tp string) func() []readers.Event {
	container, _ := args["container"].(string)
	if container == "" {
		return func() []readers.Event {
			slog.Warn("logs_tail: args.container is required and must be a string", "got", args["container"])
			return nil
		}
	}
	// Resolve container name: append site suffix when site is non-empty.
	resolved := container
	if site != "" {
		resolved = container + "-" + site
	}

	service, _ := args["service"].(string)
	if service == "" {
		service = resolved
	}

	inner, err := p.getOrOpen(resolved, service)
	if err != nil {
		slog.Warn("logs_tail: open tail", "container", resolved, "err", err)
		return func() []readers.Event { return nil }
	}
	return inner.PollFn("", args, tp)
}

// getOrOpen returns the cached StreamPoller for container or opens a
// fresh one. Holds the poller's mutex for the duration of the open
// call so concurrent assertions on the same container don't race to
// create two tails.
func (p *LogsTailPoller) getOrOpen(container, service string) (*StreamPoller, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if sp, ok := p.cache[container]; ok {
		return sp, nil
	}
	rdr := readers.NewContainerLogsReader(container, service, "logs_tail")
	sp, err := NewStreamPoller(rdr, "", p.startTime)
	if err != nil {
		return nil, err
	}
	p.cache[container] = sp
	return sp, nil
}

// Close terminates every cached tail. Called by Sandbox.Teardown via
// the cleanup func that RegisterBuiltinPollers returns.
func (p *LogsTailPoller) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, sp := range p.cache {
		sp.Close()
	}
	p.cache = map[string]*StreamPoller{}
}

// Compile-time interface check.
var _ Poller = (*LogsTailPoller)(nil)
