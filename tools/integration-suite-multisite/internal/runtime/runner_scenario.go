package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gocql/gocql"
	"github.com/nats-io/nats.go"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/mishap"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/runtime/pollers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/scenario"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/seedeffect"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// Default per-assertion polling parameters. Scenarios can override per
// expected[] block; the defaults match spec §6.4.
const (
	defaultScenarioTimeout = 5 * time.Second
	defaultScenarioPolling = 100 * time.Millisecond
)

// runnerDeps bundles the runner-startup-built dependencies the case
// loop needs. Built once in Run() and reused for every scenario.
//
// Phase 4.0 universal-primitive design: the runner no longer carries
// per-stream / per-collection / per-container reader handles. Instead,
// it surfaces per-site backend maps (Mongo + AuthURL), a shared
// Cassandra connection, one admin NATS conn, one dispatcher-fed reply
// reader, and the bucket-window knob.
// The universal primitives (mongo_find / cassandra_select /
// jetstream_consume / logs_tail / reply) handle the rest from YAML
// args at assertion time.
type runnerDeps struct {
	Cfg            *Config
	MongoBySite    map[string]*mongo.Database
	AuthURLBySite  map[string]string
	Cassandra      *gocql.Session        // nil disables cassandra_select; shared across sites
	AdminConns     map[string]*nats.Conn // per-site; empty disables jetstream_consume
	Dispatcher     *Dispatcher
	SeedEffectReg  *seedeffect.Registry
	MatcherReg     *matchers.Registry
	MishapRegistry *mishap.Registry
	FactoryByKind  map[string]string
	DockerCLI      mishap.DockerCLI
	ChaosEngine    mishap.ChaosEngine

	// ReplyReader is the dispatcher-fed singleton that backs the
	// `reply` poller (the dispatcher injects directly into it on
	// Fire). Required.
	ReplyReader *readers.NATSReplyReader

	// Service-level credentials (e.g. ${service.backend}). Built from
	// cfg.NATSCredsFile by buildServiceCreds at runner startup.
	Services map[string]Credential

	// MessageBucketWindow tunes cassandra_select.BucketAt. Zero falls
	// back to 24h (production MESSAGE_BUCKET_HOURS).
	MessageBucketWindow time.Duration

	// Per-run sinks.
	Perf   *PerformanceStore
	Report *RunReport

	// afterSetup is a test hook called immediately after Sandbox.Setup
	// returns successfully. Production wiring leaves this nil. Tests
	// use it to mutate the live PollerReg (e.g. register a stub poller
	// so assertions can fire without real readers attached).
	afterSetup func(*Sandbox)
}

// toSandboxDeps converts the runnerDeps bundle into the SandboxDeps
// shape the sandbox and its Setup method consume.
func (d *runnerDeps) toSandboxDeps() *SandboxDeps {
	return &SandboxDeps{
		MongoBySite:         d.MongoBySite,
		AuthURLBySite:       d.AuthURLBySite,
		Cassandra:           d.Cassandra,
		CassandraKeyspace:   d.Cfg.CassandraKeyspace,
		AdminConns:          d.AdminConns,
		MessageBucketWindow: d.MessageBucketWindow,
		Chaos:               d.ChaosEngine,
		Dispatcher:          d.Dispatcher,
		SeedEffectReg:       d.SeedEffectReg,
		ReplyReader:         d.ReplyReader,
		MatcherReg:          d.MatcherReg,
		MishapRegistry:      d.MishapRegistry,
		FactoryByKind:       d.FactoryByKind,
		DockerCLI:           d.DockerCLI,
		Services:            d.Services,
	}
}

// scenarioAssertion is the Gomega FailHandler sink for a scenario run.
// Captures every failure message and a flag the assertion loop checks.
// Not concurrency-safe — Gomega Eventually/Consistently call the
// handler from the same goroutine.
type scenarioAssertion struct {
	failed   bool
	messages []string
}

func (sa *scenarioAssertion) Handler() gomegatypes.GomegaFailHandler {
	return func(msg string, _ ...int) {
		sa.failed = true
		sa.messages = append(sa.messages, msg)
	}
}

// runScenario implements spec §6.7: build Sandbox, Setup, fire the
// single scenario input, evaluate expected[], defer Teardown.
func runScenario(ctx context.Context, s *scenario.Scenario, deps *runnerDeps) error {
	sb := NewSandbox(s, deps.toSandboxDeps())
	if err := sb.Setup(ctx); err != nil {
		start := time.Now()
		recordScenario(deps.Perf, deps.Report, s,
			scenarioVerdict{Outcome: "fail", Reason: fmt.Sprintf("sandbox setup: %v", err)},
			time.Since(start))
		return nil
	}
	defer sb.Teardown(context.Background())

	if deps.afterSetup != nil {
		deps.afterSetup(sb)
	}

	start := time.Now()

	// Pre-fire scripts: author-declared hooks that run after Sandbox
	// setup completes and before any poller warming or the fire. The
	// harness executes whatever the scenario YAML declares; the
	// script's content and intent are the author's, not the tool's.
	// Non-zero exit fails the scenario with the script's output
	// captured into the report.
	if err := runPreFireScripts(ctx, s, deps.Cfg); err != nil {
		recordScenario(deps.Perf, deps.Report, s,
			scenarioVerdict{Outcome: "fail", Reason: err.Error()},
			time.Since(start))
		return nil
	}

	// Build the substitution context.
	subCtx := buildSubContext(sb)

	// Resolve the scenario credential + fire the verb.
	cred := resolveCredential(s.Input.Credential, sb.Users, sb.Deps.Services)

	// Warm any pollers that need pre-fire initialization (nats_subscribe).
	if sb.PollerReg != nil {
		for i := range s.Expected {
			exp := &s.Expected[i]
			poller, perr := sb.PollerReg.Get(exp.Location)
			if perr != nil {
				continue // unknown location surfaced at assertion time
			}
			w, ok := poller.(pollers.Warmer)
			if !ok {
				continue
			}
			warmArgs := map[string]any{}
			if len(exp.Args) > 0 {
				argsV, sterr := Substitute(copyMapAny(exp.Args), subCtx)
				if sterr == nil {
					if ra, ok := argsV.(map[string]any); ok {
						warmArgs = ra
					}
				}
			}
			if werr := w.Warm(warmArgs); werr != nil {
				dur := time.Since(start)
				recordScenario(deps.Perf, deps.Report, s,
					scenarioVerdict{Outcome: "fail", Reason: fmt.Sprintf("warm %q: %v", exp.Location, werr)},
					dur)
				return nil
			}
		}
	}

	inSpec := &InputSpec{
		Site:    s.Input.Site,
		Verb:    s.Input.Verb,
		Subject: s.Input.Subject,
		Payload: s.Input.Payload,
	}
	if err := sb.Deps.Dispatcher.Fire(ctx, inSpec, &subCtx, cred, ""); err != nil {
		dur := time.Since(start)
		recordScenario(deps.Perf, deps.Report, s,
			scenarioVerdict{Outcome: "fail", Reason: fmt.Sprintf("dispatcher.Fire: %v", err)},
			dur)
		return nil
	}

	// Evaluate each expected[] block. First failure is captured via Gomega;
	// loop short-circuits on first failure per spec §6.4.
	sa := &scenarioAssertion{}
	g := gomega.NewGomega(sa.Handler())
	for i := range s.Expected {
		evalScenarioExpected(ctx, g, sb, &subCtx, s.Expected[i])
		if sa.failed {
			break
		}
	}

	dur := time.Since(start)
	if sa.failed {
		recordScenario(deps.Perf, deps.Report, s,
			scenarioVerdict{Outcome: "fail", Reason: strings.Join(sa.messages, "; ")},
			dur)
	} else {
		recordScenario(deps.Perf, deps.Report, s,
			scenarioVerdict{Outcome: "pass"},
			dur)
	}
	return nil
}

// evalScenarioExpected runs one expected[] block through Gomega.
// Failures hit scenarioAssertion via the FailHandler attached to g.
func evalScenarioExpected(_ context.Context, g gomega.Gomega, sb *Sandbox, subCtx *Context, exp scenario.Expected) {
	poller, err := sb.PollerReg.Get(exp.Location)
	if err != nil {
		g.Expect(err).NotTo(gomega.HaveOccurred(), "poller registry lookup for location %q", exp.Location)
		return
	}

	resolvedV, err := Substitute(copyMapAny(exp.Match), *subCtx)
	if err != nil {
		g.Expect(err).NotTo(gomega.HaveOccurred(), "substitute match for location %q", exp.Location)
		return
	}
	resolved, ok := resolvedV.(map[string]any)
	if !ok {
		g.Expect(false).To(gomega.BeTrue(), "match for location %q must resolve to a map, got %T", exp.Location, resolvedV)
		return
	}

	resolvedArgs := map[string]any{}
	if len(exp.Args) > 0 {
		argsV, err := Substitute(copyMapAny(exp.Args), *subCtx)
		if err != nil {
			g.Expect(err).NotTo(gomega.HaveOccurred(), "substitute args for location %q", exp.Location)
			return
		}
		ra, ok := argsV.(map[string]any)
		if !ok {
			g.Expect(false).To(gomega.BeTrue(), "args for location %q must resolve to a map, got %T", exp.Location, argsV)
			return
		}
		resolvedArgs = ra
	}

	timeout := time.Duration(exp.Timeout)
	if timeout == 0 {
		timeout = defaultScenarioTimeout
	}
	polling := time.Duration(exp.Polling)
	if polling == 0 {
		polling = defaultScenarioPolling
	}

	pollFn := poller.PollFn(exp.Site, resolvedArgs, "")
	matcher := MatchShape(resolved, sb.Deps.MatcherReg)

	if exp.Not {
		g.Consistently(pollFn, timeout, polling).ShouldNot(matcher)
	} else {
		g.Eventually(pollFn, timeout, polling).Should(matcher)
	}
}

// buildSubContext constructs the substitution context from the sandbox.
func buildSubContext(sb *Sandbox) Context {
	return Context{
		Placeholders: sb.Placeholders,
		Services:     sb.Deps.Services,
	}
}

// resolveCredential resolves a credential reference against the
// sandbox's materialized users + service map.
func resolveCredential(ref string, users map[string]*seedeffect.SeedUser, services map[string]Credential) verbs.Credential {
	trimmed := strings.TrimPrefix(ref, "$")
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	if trimmed == "" {
		return verbs.Credential{}
	}
	parts := strings.SplitN(trimmed, ".", 3)
	if parts[0] == "service" && len(parts) >= 2 {
		c, ok := services[parts[1]]
		if !ok {
			return verbs.Credential{}
		}
		return verbs.Credential{
			Account:   c.Account,
			JWT:       c.JWT,
			NkeySeed:  c.NkeySeed,
			CredsFile: c.CredsFile,
		}
	}
	u, ok := users[parts[0]]
	if !ok {
		return verbs.Credential{}
	}
	return verbs.Credential{
		Account:  u.Account,
		JWT:      u.JWT,
		NkeySeed: u.NkeySeed,
	}
}

// copyMapAny copies a map[string]any so Substitute returns a fresh map
// without mutating the scenario's parsed payload.
func copyMapAny(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
