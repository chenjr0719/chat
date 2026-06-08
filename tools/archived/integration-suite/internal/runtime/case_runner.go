package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/mishap"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/runtime/pollers"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/scenario"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/seedeffect"
	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/verbs"
)

// CaseVerdict is the per-case outcome returned to runScenario. The
// reporter glue (Task 20) adapts it into the existing CaseReport shape.
type CaseVerdict struct {
	Outcome  string   // "pass" | "fail"
	Reason   string   // joined Failures with "; " — convenient single-line summary
	Failures []string // individual Gomega failure messages, in order
}

// Default per-assertion polling parameters. Cases can override per
// expected[] block; the defaults match spec §6.4.
const (
	defaultCaseTimeout   = 5 * time.Second
	defaultCasePolling   = 100 * time.Millisecond
	mishapCleanupTimeout = 30 * time.Second // §6.4 step 3
)

// RunCase executes one case against an existing Sandbox per spec
// §6.4. It returns a CaseVerdict capturing pass/fail and any Gomega
// failure messages. Per-case mishap injection lands in Task 18.
//
// A non-nil error is returned ONLY for non-assertion runtime failures
// (substitution error, executor missing, dispatcher panic). Assertion
// failures are captured via a Gomega fail handler and surfaced through
// the returned Verdict so the runner can record the case as
// `fail` without aborting the scenario.
func RunCase(ctx context.Context, sb *Sandbox, c *scenario.Case) (CaseVerdict, error) {
	// Step 1: merge c.Input over sb.Scenario.BaseInput (shallow:
	// pointer-nil = inherit). Passed straight to dispatcher.Fire as
	// an InputSpec — Phase 3 dispatcher consumes the merged input
	// directly (no synthetic v2 Scenario wrapper).
	in := mergeInput(sb.Scenario, c)

	// Step 2: build the substitution context. Placeholders are already
	// materialized by Sandbox.Setup; Services flow through from the
	// runner-built service credentials map (today: only "backend" from
	// cfg.NATSCredsFile).
	subCtx := &Context{
		Site:         sb.Deps.SiteID,
		Placeholders: sb.Placeholders,
		Services:     sb.Deps.Services,
	}

	// Step 3: build the Gomega assertion context. NewGomega's
	// FailHandler captures failures into ca without panicking, so the
	// expected[] loop can short-circuit on the first failure rather
	// than aborting the whole goroutine (Ginkgo's default).
	ca := &caseAssertion{}
	g := gomega.NewGomega(ca.Handler())

	// Step 3.5 (§6.4 step 3): if c.Mishap is set, build its Executor
	// via the registered factory, fire the trigger immediately (the
	// case is itself the sequence-level "fire" point — there's no
	// gather-and-fire wait), spawn Apply in a goroutine, and defer
	// Cleanup on a fresh 30s context so chaos teardown still runs
	// even if the parent ctx was canceled.
	if c.Mishap != "" {
		factoryName, ok := sb.Deps.FactoryByKind[c.Mishap]
		if !ok {
			return CaseVerdict{}, fmt.Errorf("RunCase %q: mishap kind %q has no factory in catalog", c.Name, c.Mishap)
		}
		factory, ferr := sb.Deps.MishapRegistry.GetFactory(factoryName)
		if ferr != nil {
			return CaseVerdict{}, fmt.Errorf("RunCase %q: get factory %q: %w", c.Name, factoryName, ferr)
		}
		fctx := mishap.FactoryContext{
			DockerCLI:   sb.Deps.DockerCLI,
			ChaosEngine: sb.Deps.Chaos,
		}
		executor, eerr := factory(fctx, "")
		if eerr != nil {
			return CaseVerdict{}, fmt.Errorf("RunCase %q: build executor %q: %w", c.Name, factoryName, eerr)
		}

		// Pre-closed trigger: Apply unblocks on the first receive.
		// There's no gather-and-fire — the case itself is the trigger
		// boundary.
		trigger := make(chan struct{})
		close(trigger)
		go func() { _ = executor.Apply(ctx, trigger) }()
		defer func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), mishapCleanupTimeout)
			defer cancel()
			_ = executor.Cleanup(cleanupCtx)
		}()
	}

	// Step 3b — Phase 4.5: warm pollers that need pre-Fire initialization.
	// Core NATS subscriptions (nats_subscribe primitive) MUST be live
	// at publish time — there is no DeliverByStartTime replay analog.
	// Pollers that don't need warmup don't implement Warmer and the
	// type assertion misses (no-op). Warm errors hard-fail per
	// docs/spec-nats-subscribe-primitive.md §8 Q2: operator misconfig
	// surfaces as a real error rather than a silent assertion timeout.
	if sb.PollerReg != nil {
		for i := range c.Expected {
			exp := &c.Expected[i]
			poller, perr := sb.PollerReg.Get(exp.Location)
			if perr != nil {
				continue // unknown location is surfaced at Step 5
			}
			w, ok := poller.(pollers.Warmer)
			if !ok {
				continue
			}
			warmArgs := map[string]any{}
			if len(exp.Args) > 0 {
				argsV, sterr := Substitute(copyMap(exp.Args), *subCtx)
				if sterr != nil {
					return CaseVerdict{}, fmt.Errorf("RunCase %q: substitute warm args for %q: %w",
						c.Name, exp.Location, sterr)
				}
				if ra, ok := argsV.(map[string]any); ok {
					warmArgs = ra
				}
			}
			if werr := w.Warm(warmArgs); werr != nil {
				return CaseVerdict{}, fmt.Errorf("RunCase %q: warm %q: %w", c.Name, exp.Location, werr)
			}
		}
	}

	// Step 4: resolve credential + traceparent and fire the verb.
	cred := pickCredential(in.credential, sb.Users, sb.Deps.Services)
	traceparent := NewTraceparent()

	if err := sb.Deps.Dispatcher.Fire(ctx, &in.spec, subCtx, cred, traceparent); err != nil {
		return CaseVerdict{}, fmt.Errorf("RunCase %q: dispatcher fire: %w", c.Name, err)
	}

	// Step 5: evaluate each expected[] block in declaration order.
	// First failure short-circuits the loop per spec §6.4 step 6.
	for i := range c.Expected {
		evalExpected(ctx, g, sb, subCtx, c.Expected[i])
		if ca.failed {
			break
		}
	}

	if ca.failed {
		return CaseVerdict{
			Outcome:  "fail",
			Reason:   strings.Join(ca.messages, "; "),
			Failures: ca.messages,
		}, nil
	}
	return CaseVerdict{Outcome: "pass"}, nil
}

// caseAssertion is the Gomega FailHandler sink. Captures every
// failure message and a flag the case loop checks to short-circuit.
// Not concurrency-safe — Gomega Eventually/Consistently call the
// handler from the same goroutine.
type caseAssertion struct {
	failed   bool
	messages []string
}

func (ca *caseAssertion) Handler() gomegatypes.GomegaFailHandler {
	return func(msg string, _ ...int) {
		ca.failed = true
		ca.messages = append(ca.messages, msg)
	}
}

// evalExpected runs one expected[] block through Gomega. Failures
// hit caseAssertion via the FailHandler attached to g.
func evalExpected(_ context.Context, g gomega.Gomega, sb *Sandbox, subCtx *Context, exp scenario.Expected) {
	// Resolve poller. registry.Get returns the "available locations"
	// hint when the location is unknown — surface that through Gomega
	// rather than returning a Go error so it lands as a normal case
	// failure with the operator-helpful list.
	poller, err := sb.PollerReg.Get(exp.Location)
	if err != nil {
		g.Expect(err).NotTo(gomega.HaveOccurred(), "poller registry lookup for location %q", exp.Location)
		return
	}

	// Substitute the match map against subCtx — supports ${input.…}
	// and ${alias.…} tokens. Use a private copy so the scenario's
	// authored YAML isn't mutated for repeated case runs.
	resolvedV, err := Substitute(copyMap(exp.Match), *subCtx)
	if err != nil {
		g.Expect(err).NotTo(gomega.HaveOccurred(), "substitute match for location %q", exp.Location)
		return
	}
	resolved, ok := resolvedV.(map[string]any)
	if !ok {
		g.Expect(false).To(gomega.BeTrue(), "match for location %q must resolve to a map, got %T", exp.Location, resolvedV)
		return
	}

	// Phase 4.0: substitute the same way over args so authors can use
	// ${alice.account}, ${site}, ${input.payload.name}, etc. inside
	// args.filter / args.query / args.filter_subject. Empty args is
	// fine — primitives that need args will warn at PollFn time.
	resolvedArgs := map[string]any{}
	if len(exp.Args) > 0 {
		argsV, err := Substitute(copyMap(exp.Args), *subCtx)
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
		timeout = defaultCaseTimeout
	}
	polling := time.Duration(exp.Polling)
	if polling == 0 {
		polling = defaultCasePolling
	}

	// No per-case trace propagation today — pollers ignore the arg
	// (none of the six primitives consume it). The hook stays in the
	// Poller signature for future trace-aware filtering.
	pollFn := poller.PollFn(resolvedArgs, "")
	matcher := MatchShape(resolved, sb.Deps.MatcherReg)

	if exp.Not {
		g.Consistently(pollFn, timeout, polling).ShouldNot(matcher)
	} else {
		g.Eventually(pollFn, timeout, polling).Should(matcher)
	}
}

// mergedInput is the case's resolved input — base_input shallow-merged
// with the case's input override. The spec field travels through the
// dispatcher; credential resolves to the case's actor.
type mergedInput struct {
	spec       InputSpec
	credential string
}

// mergeInput shallow-merges c.Input over s.BaseInput. Pointer-nil
// fields on c.Input inherit base_input.
func mergeInput(s *scenario.Scenario, c *scenario.Case) mergedInput {
	subject := s.BaseInput.Subject
	payload := s.BaseInput.Payload
	credential := s.BaseInput.Credential
	if c.Input != nil {
		if c.Input.Subject != nil {
			subject = *c.Input.Subject
		}
		if c.Input.Payload != nil {
			payload = c.Input.Payload
		}
		if c.Input.Credential != nil {
			credential = *c.Input.Credential
		}
	}
	return mergedInput{
		spec: InputSpec{
			Verb:    s.BaseInput.Verb,
			Subject: subject,
			Payload: payload,
		},
		credential: credential,
	}
}

// pickCredential resolves a credential reference against the
// sandbox's materialized users + service map. Recognised forms:
//
//	${<alias>.credential}     → users[alias]            (user-level)
//	${<alias>}                → users[alias]            (shorthand)
//	${service.<name>.credential} → services[name]       (service-level)
//	${service.<name>}            → services[name]       (shorthand)
//
// Unknown references return a zero Credential — the dispatcher's
// executor will then surface "no creds" via its transport error,
// which the case's `expected` blocks can assert against.
func pickCredential(ref string, users map[string]*seedeffect.SeedUser, services map[string]Credential) verbs.Credential {
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

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
