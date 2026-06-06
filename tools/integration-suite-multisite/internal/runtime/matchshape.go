package runtime

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/onsi/gomega/types"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/matchers"
	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/readers"
)

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

	for i, ev := range events {
		res := shape.Match(ev.Payload, m.expected)
		if res.Matched {
			m.matchedIdx = i
			return true, nil
		}
		// Track the first mismatch reason — that's the closest signal
		// the operator gets without a full diff engine. Future work
		// could rank by "fields-correct" count.
		if m.bestReason == "" {
			m.bestReason = res.Reason
			m.bestIdx = i
		}
	}
	return false, nil
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
