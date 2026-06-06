package readers

import (
	"encoding/json"
	"time"

	"github.com/hmchangw/chat/tools/integration-suite-multisite/internal/verbs"
)

// ReplyPayload is the structured Event.Payload emitted by NATSReplyReader.
// The reader captures every observable fact about a request/reply
// outcome and surfaces them verbatim; matchers (and the future reporter)
// do the interpretation. There is deliberately no classification or
// enum mapping — failure-mode strings live in Error verbatim so that
// scenarios can match exactly what the verb / NATS library produced.
type ReplyPayload struct {
	BodyJSON  map[string]any      `json:"body_json,omitempty"` // parsed body when content-type is JSON-decodable
	BodyRaw   string              `json:"body_raw,omitempty"`  // raw string fallback when BodyJSON is nil
	Header    map[string][]string `json:"header,omitempty"`    // every header on the reply
	Error     string              `json:"error,omitempty"`     // verb Outcome.Err.Error() verbatim; empty on success
	LatencyMs int64               `json:"latency_ms"`          // fire → reply (or fire → error)
}

// NewReplyPayload constructs a ReplyPayload from a verb Outcome and a
// measured latency. Body decoding tries JSON first; falls back to a
// raw string on failure. Header is copied so the original nats.Header
// can be reused safely.
func NewReplyPayload(out *verbs.Outcome, latency time.Duration) ReplyPayload {
	p := ReplyPayload{
		LatencyMs: latency.Milliseconds(),
	}
	if out.Err != nil {
		p.Error = out.Err.Error()
	}
	if len(out.Reply) > 0 {
		var m map[string]any
		if err := json.Unmarshal(out.Reply, &m); err == nil {
			p.BodyJSON = m
		} else {
			p.BodyRaw = string(out.Reply)
		}
	}
	if len(out.Header) > 0 {
		h := make(map[string][]string, len(out.Header))
		for k, v := range out.Header {
			cp := make([]string, len(v))
			copy(cp, v)
			h[k] = cp
		}
		p.Header = h
	}
	return p
}
