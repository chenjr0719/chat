package mishap

import (
	"context"
	"errors"
	"fmt"

	toxiproxy "github.com/Shopify/toxiproxy/v2/client"
)

// ErrUnknownProxy is returned when an executor names a proxy that
// wasn't part of the pre-provisioned set passed to NewToxiproxyEngine.
// Surfaced as a fast-fail so a typo in a factory's proxy name doesn't
// silently no-op.
var ErrUnknownProxy = errors.New("mishap: unknown toxiproxy proxy name")

// ChaosEngine is the toolkit primitive for Toxiproxy-managed network
// faults. One instance per suite run, shared by every executor.
//
// Implementations MUST be safe for concurrent calls from multiple Apply
// goroutines. The toxiproxyEngine implementation relies on the
// Toxiproxy admin API's per-request serialization for this guarantee.
type ChaosEngine interface {
	// Partition disables the named proxy. Existing TCP connections are
	// closed; new dials get "connection refused" until Heal is called.
	// Returns ErrUnknownProxy if name is not in the pre-provisioned set.
	Partition(ctx context.Context, name string) error

	// Heal re-enables the named proxy. Idempotent — Heal on an already-
	// enabled proxy is a no-op.
	Heal(ctx context.Context, name string) error

	// Reset returns every pre-provisioned proxy to enabled + toxic-free.
	// Wraps client.ResetState(). Called at suite startup and between
	// cases as the second backstop (Cleanup is the first).
	Reset(ctx context.Context) error
}

// toxiproxyEngine is the production ChaosEngine backed by the Shopify
// Toxiproxy Go client. proxies is the static expected-name set; lookups
// outside the set fail fast with ErrUnknownProxy.
type toxiproxyEngine struct {
	client  *toxiproxy.Client
	proxies map[string]struct{}
}

// NewToxiproxyEngine connects to the Toxiproxy admin API at adminURL,
// verifies every name in expectedProxies is present, and returns a
// ready-to-use engine. Returns a non-nil error on admin-unreachable or
// missing-proxy — cmd/runner/main.go panics on this error per the
// design ruling §8 ("startup unreachable → panic").
func NewToxiproxyEngine(adminURL string, expectedProxies []string) (ChaosEngine, error) {
	c := toxiproxy.NewClient(adminURL)
	present, err := c.Proxies()
	if err != nil {
		return nil, fmt.Errorf("toxiproxy admin %s unreachable: %w", adminURL, err)
	}
	known := make(map[string]struct{}, len(expectedProxies))
	for _, name := range expectedProxies {
		if _, ok := present[name]; !ok {
			return nil, fmt.Errorf("toxiproxy: expected proxy %q not pre-provisioned", name)
		}
		known[name] = struct{}{}
	}
	return &toxiproxyEngine{client: c, proxies: known}, nil
}

func (e *toxiproxyEngine) Partition(_ context.Context, name string) error {
	if _, ok := e.proxies[name]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownProxy, name)
	}
	p, err := e.client.Proxy(name)
	if err != nil {
		return fmt.Errorf("fetch proxy %s: %w", name, err)
	}
	if err := p.Disable(); err != nil {
		return fmt.Errorf("disable proxy %s: %w", name, err)
	}
	return nil
}

func (e *toxiproxyEngine) Heal(_ context.Context, name string) error {
	if _, ok := e.proxies[name]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownProxy, name)
	}
	p, err := e.client.Proxy(name)
	if err != nil {
		return fmt.Errorf("fetch proxy %s: %w", name, err)
	}
	if err := p.Enable(); err != nil {
		return fmt.Errorf("enable proxy %s: %w", name, err)
	}
	return nil
}

func (e *toxiproxyEngine) Reset(_ context.Context) error {
	if err := e.client.ResetState(); err != nil {
		return fmt.Errorf("toxiproxy reset state: %w", err)
	}
	return nil
}
