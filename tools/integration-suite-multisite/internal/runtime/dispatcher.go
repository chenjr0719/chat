package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// Dispatcher resolves a case's merged input + placeholders into a
// concrete verb call, fires the verb, and pushes the reply (if any)
// into the NATSReplyReader so the per-scenario PollerReg's "reply"
// poller sees it.
type Dispatcher struct {
	VerbReg  *verbs.Registry
	ReplyRdr *readers.NATSReplyReader
}

// InputSpec is the post-substitution shape the runner hands the
// dispatcher. Site routes the verb to the correct per-site backend
// (per spec §1 row 5 — explicit site on input).
type InputSpec struct {
	Site    string
	Verb    string
	Subject string
	Payload map[string]any
}

// Fire executes the verb under the supplied credential + traceparent.
// subCtx MUST have Site, Placeholders, Services populated; Fire
// substitutes them into in.Subject + in.Payload and writes the resolved
// subject/payload/requestId back into subCtx.Input so assertions can
// use ${input.…} tokens.
func (d *Dispatcher) Fire(ctx context.Context, in *InputSpec, subCtx *Context, cred verbs.Credential, traceparent string) error {
	executor, err := d.VerbReg.Get(verbExecutorName(in.Verb))
	if err != nil {
		return fmt.Errorf("dispatcher: %w", err)
	}

	subjectV, err := Substitute(in.Subject, *subCtx)
	if err != nil {
		return fmt.Errorf("dispatcher: substitute subject: %w", err)
	}
	subject, ok := subjectV.(string)
	if !ok {
		return fmt.Errorf("dispatcher: subject must resolve to a string, got %T", subjectV)
	}

	payloadV, err := Substitute(toAnyMap(in.Payload), *subCtx)
	if err != nil {
		return fmt.Errorf("dispatcher: substitute payload: %w", err)
	}
	payloadMap, ok := payloadV.(map[string]any)
	if !ok {
		return fmt.Errorf("dispatcher: payload must resolve to a map, got %T", payloadV)
	}
	payloadBytes, err := json.Marshal(payloadMap)
	if err != nil {
		return fmt.Errorf("dispatcher: payload marshal: %w", err)
	}

	fireMoment := time.Now()
	out := executor.Execute(ctx, &verbs.Input{
		Site:        in.Site,
		Subject:     subject,
		Payload:     payloadBytes,
		Credential:  cred,
		Traceparent: traceparent,
	})
	latency := time.Since(fireMoment)

	subCtx.Input = InputSnapshot{
		Subject:   subject,
		Payload:   payloadMap,
		RequestID: out.RequestID,
	}

	// Only inject into the reply reader for verbs with a synchronous
	// request/reply shape. JetStream publishes return an ack — not a
	// "reply" the assertion mechanism cares about.
	if d.ReplyRdr != nil && verbProducesReply(in.Verb) {
		d.ReplyRdr.Inject(&out, latency, traceparent, time.Now(), "")
	}
	return nil
}

// verbExecutorName maps a scenario's `verb` to the registry key the
// executor was registered under. Hardcoded for the two production
// verbs; a catalog-driven version is future work.
func verbExecutorName(verb string) string {
	switch verb {
	case "nats_request":
		return "NATSRequestExecutor"
	case "jetstream_publish":
		return "JetStreamPublishExecutor"
	}
	return verb
}

// verbProducesReply reports whether the given verb has a synchronous
// request/reply shape whose outcome should flow into the reply reader.
func verbProducesReply(verb string) bool {
	return verb == "nats_request"
}

// toAnyMap copies a map[string]any so Substitute returns a fresh map
// without mutating the scenario's parsed payload.
func toAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

var currentRunIDValue string

func currentRunID() string { return currentRunIDValue }

// SetRunID is called by the runner before dispatch.
func SetRunID(id string) { currentRunIDValue = id }
