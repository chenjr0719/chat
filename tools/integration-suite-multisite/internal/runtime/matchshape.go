package runtime

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/onsi/gomega/types"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

// outboxPayloadKey is the magic match-shape key that triggers
// base64-decode-then-JSON-parse of body_json.payload before
// subset-matching its contents. Reserved for the OutboxEvent /
// InboxEvent shape where pkg/model serializes the inner event as
// `[]byte` (base64 on the JSON wire) so a normal subset-match can
// only reach the envelope (type, siteId, destSiteId), never the
// inner roomId / newName / member_added contents.
//
// Authors write:
//
//	match:
//	  body_json:
//	    type: room_renamed
//	    siteId: site-a
//	  outbox_payload:
//	    roomId: r-shared
//	    newName: SharedChannelRenamed
//
// The `body_json:` block continues to subset-match envelope fields
// normally; `outbox_payload:` is consumed by the matcher (not passed
// to the underlying matches_shape pass) and matches against the
// decoded inner content.
//
// Why a fixed name rather than a directive-object: the OutboxEvent
// shape is the single use case today (cross-site federation tests).
// A more general "decode this field" matcher would add YAML grammar
// surface area for one feature. We keep it tight; if other base64+
// JSON shapes arrive, a sibling key (e.g. `payload_b64_json:` with a
// `from:` path) is the natural extension.
const outboxPayloadKey = "outbox_payload"

// MatchShape returns a Gomega matcher that succeeds iff at least one
// Event in the polled slice has a Payload satisfying `expected` under
// the existing internal/matchers `matches_shape` semantic (subset deep
// match with type-normalising compare).
//
// reg is the matcher registry; nil falls back to matchers.NewRegistry()
// so callers can use MatchShape inline without threading a registry.
// In production wiring, runner.go passes its already-built registry so
// MatchShape inherits any custom matchers registered there.
//
// Used as:
//
//	g.Eventually(poller.PollFn(tp), timeout, polling).
//	    Should(MatchShape(expected, reg))      // wait for arrival
//	g.Consistently(poller.PollFn(tp), timeout, polling).
//	    ShouldNot(MatchShape(expected, reg))   // assert never
func MatchShape(expected map[string]any, reg *matchers.Registry) types.GomegaMatcher {
	if reg == nil {
		reg = matchers.NewRegistry()
	}
	return &shapeMatcher{expected: expected, reg: reg}
}

// shapeMatcher is the GomegaMatcher implementation. The "best
// mismatch reason" captured during Match drives FailureMessage so
// the operator sees the closest-event diff (mirrors the triage value
// of the v2 MissingPositives output).
type shapeMatcher struct {
	expected map[string]any
	reg      *matchers.Registry

	// State captured by the most recent Match call. Gomega calls
	// FailureMessage / NegatedFailureMessage with the same `actual`
	// it just passed to Match, so this is safe even though it makes
	// the matcher non-reentrant across concurrent Match calls (Gomega
	// matchers are documented as single-goroutine).
	lastEventCount int
	bestReason     string // closest mismatch reason; empty if 0 events
	bestIdx        int    // index of the closest-mismatch event; -1 if none
	matchedIdx     int    // index of the event that matched (for negated msg)
}

func (m *shapeMatcher) Match(actual any) (bool, error) {
	events, ok := actual.([]readers.Event)
	if !ok {
		return false, fmt.Errorf("MatchShape: expected []readers.Event, got %T", actual)
	}
	m.lastEventCount = len(events)
	m.bestReason = ""
	m.bestIdx = -1
	m.matchedIdx = -1

	shape, err := m.reg.Get("matches_shape")
	if err != nil {
		// Programmer error: the registry was constructed without the
		// built-in matchers_shape. Surface immediately rather than
		// silently failing every event.
		return false, fmt.Errorf("MatchShape: matches_shape not registered: %w", err)
	}

	// Split off the outbox_payload directive (if any) so the
	// underlying matches_shape pass sees only the envelope-level
	// expectations. The directive's expected value is matched
	// separately against the base64-decoded inner payload.
	envelopeExpected, outboxPayloadExpected, hasOutboxPayload, err := splitOutboxPayloadDirective(m.expected)
	if err != nil {
		return false, err
	}

	for i, ev := range events {
		res := shape.Match(ev.Payload, envelopeExpected)
		if !res.Matched {
			if m.bestReason == "" {
				m.bestReason = res.Reason
				m.bestIdx = i
			}
			continue
		}
		if hasOutboxPayload {
			matched, reason := matchOutboxPayload(ev.Payload, outboxPayloadExpected, shape)
			if !matched {
				if m.bestReason == "" {
					m.bestReason = reason
					m.bestIdx = i
				}
				continue
			}
		}
		m.matchedIdx = i
		return true, nil
	}
	return false, nil
}

// splitOutboxPayloadDirective separates the envelope match shape
// from the outbox_payload directive value. Returns the envelope
// shape (a copy without the directive key), the directive's
// expected map, whether the directive was present, and any
// validation error (the directive's value must be a map).
func splitOutboxPayloadDirective(expected map[string]any) (envelope map[string]any, directive map[string]any, present bool, err error) {
	raw, has := expected[outboxPayloadKey]
	if !has {
		return expected, nil, false, nil
	}
	dir, ok := raw.(map[string]any)
	if !ok {
		return nil, nil, false, fmt.Errorf("MatchShape: %q expected value must be a map (subset shape against the decoded payload), got %T", outboxPayloadKey, raw)
	}
	envelope = make(map[string]any, len(expected)-1)
	for k, v := range expected {
		if k == outboxPayloadKey {
			continue
		}
		envelope[k] = v
	}
	return envelope, dir, true, nil
}

// matchOutboxPayload decodes the event's body_json.payload field
// (base64 → JSON) and runs the supplied matches_shape against the
// inner content. Returns matched + a precise reason on miss so the
// best-mismatch tracking in shapeMatcher can surface it through
// FailureMessage.
//
// The four failure modes are flagged distinctly because each one
// implies a different fix:
//
//  1. body_json missing: the event's payload shape is wrong for an
//     OutboxEvent — likely a poller-shape regression.
//  2. body_json.payload missing/wrong type: the inner envelope is
//     missing — likely a producer skipped its encode step.
//  3. base64 decode failed: the payload bytes aren't standard b64 —
//     likely a producer wire-format regression.
//  4. JSON parse of decoded bytes failed: the inner format isn't
//     JSON — likely the schema changed shape.
func matchOutboxPayload(eventPayload any, expected map[string]any, shape matchers.Matcher) (bool, string) {
	payloadMap, ok := eventPayload.(map[string]any)
	if !ok {
		return false, fmt.Sprintf("%s: event payload is not a map, got %T", outboxPayloadKey, eventPayload)
	}
	bodyJSON, ok := payloadMap["body_json"].(map[string]any)
	if !ok {
		return false, fmt.Sprintf("%s: body_json is missing or not a map (decoded payload requires the body to parse as JSON first)", outboxPayloadKey)
	}
	b64, ok := bodyJSON["payload"].(string)
	if !ok {
		return false, fmt.Sprintf("%s: body_json.payload is missing or not a string", outboxPayloadKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return false, fmt.Sprintf("%s: base64 decode of body_json.payload failed: %v", outboxPayloadKey, err)
	}
	var inner map[string]any
	if err := json.Unmarshal(decoded, &inner); err != nil {
		return false, fmt.Sprintf("%s: JSON parse of decoded payload failed: %v (decoded length=%d)", outboxPayloadKey, err, len(decoded))
	}
	res := shape.Match(inner, expected)
	if !res.Matched {
		return false, fmt.Sprintf("%s: decoded subset mismatch: %s", outboxPayloadKey, res.Reason)
	}
	return true, ""
}

func (m *shapeMatcher) FailureMessage(actual any) string {
	// Three labeled sections — tool framing, system's raw reply,
	// tool's mismatch reason — kept distinct so the operator can
	// read the system's response verbatim without it being woven
	// into the tool's prose.
	var b strings.Builder
	b.WriteString("MatchShape failed.\n")
	if raw, err := json.Marshal(m.expected); err == nil {
		fmt.Fprintf(&b, "  expected:          %s\n", raw)
	} else {
		fmt.Fprintf(&b, "  expected:          %#v\n", m.expected)
	}
	if events, ok := actual.([]readers.Event); ok && m.bestIdx >= 0 && m.bestIdx < len(events) {
		ev := events[m.bestIdx]
		if raw, err := json.Marshal(ev.Payload); err == nil {
			fmt.Fprintf(&b, "  reply from system: %s\n", raw)
		} else {
			fmt.Fprintf(&b, "  reply from system: %#v\n", ev.Payload)
		}
	} else {
		fmt.Fprintf(&b, "  reply from system: (no events polled)\n")
	}
	if m.bestReason != "" {
		fmt.Fprintf(&b, "  mismatch reason:   %s\n", m.bestReason)
	}
	fmt.Fprintf(&b, "  events polled:     %d", m.lastEventCount)
	return b.String()
}

func (m *shapeMatcher) NegatedFailureMessage(actual any) string {
	events, _ := actual.([]readers.Event)
	if m.matchedIdx >= 0 && m.matchedIdx < len(events) {
		return fmt.Sprintf(
			"MatchShape: expected no event to match shape %v, but event #%d (location=%q) did with payload %v",
			m.expected, m.matchedIdx, events[m.matchedIdx].Location, events[m.matchedIdx].Payload,
		)
	}
	return fmt.Sprintf("MatchShape: expected no event to match shape %v, but one did", m.expected)
}
