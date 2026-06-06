package runtime

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// Context carries everything substitution can resolve against. Built
// incrementally by the runner: Site + Placeholders + Services before
// fire, Input populated after fire.
type Context struct {
	Site         string
	Placeholders map[string]map[string]any
	Services     map[string]Credential // service-level credentials keyed by service name (e.g. "backend")
	Input        InputSnapshot
}

// Credential is the local mirror of verbs.Credential used for service-
// level NATS credentials wired into the substitution Context. Kept as
// a separate type so the runtime package doesn't grow a dependency
// loop on internal/verbs's types beyond what dispatcher.go already has.
type Credential struct {
	Account   string
	JWT       string
	NkeySeed  string
	CredsFile string
}

// InputSnapshot captures the post-substitution values that actually
// got fired, so reads can assert against them via ${input.…}.
type InputSnapshot struct {
	Subject   string
	Payload   map[string]any
	RequestID string
}

var tokenRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// Substitute recursively walks value, resolving any ${…} tokens
// against ctx. Preserves native types — int stays int, bool stays
// bool, etc. A whole-string single token returns the resolved value
// with its original type; mixed string+token returns an interpolated
// string. Unknown paths return a hard error naming the token and the
// available alternatives.
//
// `$auto` is recognised as a sentinel that resolves to a unique
// `it-<runID>-…` string (preserves existing dispatcher behavior).
func Substitute(value any, ctx Context) (any, error) {
	switch v := value.(type) {
	case string:
		return substituteString(v, ctx)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, vv := range v {
			sv, err := Substitute(vv, ctx)
			if err != nil {
				return nil, err
			}
			out[k] = sv
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, vv := range v {
			sv, err := Substitute(vv, ctx)
			if err != nil {
				return nil, err
			}
			out[i] = sv
		}
		return out, nil
	default:
		return value, nil
	}
}

func substituteString(s string, ctx Context) (any, error) {
	if s == "$auto" {
		return autoValue(), nil
	}
	matches := tokenRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	// Whole-string single token → return native type.
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(s) {
		token := s[matches[0][2]:matches[0][3]]
		return resolveToken(token, ctx)
	}
	// Mixed → interpolate as string.
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		token := s[m[2]:m[3]]
		val, err := resolveToken(token, ctx)
		if err != nil {
			return nil, err
		}
		fmt.Fprintf(&b, "%v", val)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

func resolveToken(token string, ctx Context) (any, error) {
	parts := strings.Split(token, ".")
	switch parts[0] {
	case "site":
		if len(parts) > 1 {
			return nil, fmt.Errorf("unknown path %q: 'site' has no subfields", token)
		}
		return ctx.Site, nil
	case "now":
		if len(parts) > 1 {
			return nil, fmt.Errorf("unknown path %q: 'now' has no subfields", token)
		}
		// Current wall-clock time in unix milliseconds. For scenarios
		// that publish synthetic events (e.g. pure jetstream_publish
		// to a canonical subject) and need the event's timestamp to
		// fall within the scenario's [T_open, T_close] window — DB
		// readers' default filter is `createdAt >= start`, so events
		// with a stale literal timestamp are silently filtered out.
		return time.Now().UTC().UnixMilli(), nil
	case "input":
		inputMap := map[string]any{
			"subject":   ctx.Input.Subject,
			"payload":   any(ctx.Input.Payload),
			"requestId": ctx.Input.RequestID,
		}
		v, ok := walkPath(inputMap, parts[1:])
		if !ok {
			return nil, fmt.Errorf("unknown path %q: 'input' has no value at .%s (available: subject, payload.<key>, requestId)",
				token, strings.Join(parts[1:], "."))
		}
		return v, nil
	default:
		ph, ok := ctx.Placeholders[parts[0]]
		if !ok {
			return nil, fmt.Errorf("unknown path %q: no placeholder %q resolved (resolved placeholders: %v)",
				token, parts[0], placeholderNames(ctx.Placeholders))
		}
		if len(parts) < 2 {
			return nil, fmt.Errorf("unknown path %q: placeholder %q needs a subfield (available: %v)",
				token, parts[0], mapKeys(ph))
		}
		v, ok := walkPath(ph, parts[1:])
		if !ok {
			return nil, fmt.Errorf("unknown path %q: placeholder %q has no field %q (available: %v)",
				token, parts[0], strings.Join(parts[1:], "."), mapKeys(ph))
		}
		return v, nil
	}
}

func walkPath(m map[string]any, path []string) (any, bool) {
	if len(path) == 0 {
		return m, true
	}
	var cur any = m
	for _, p := range path {
		m2, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m2[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

func mapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func placeholderNames(m map[string]map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// autoCounter is incremented per $auto resolution within a process.
// Combined with currentRunID() (random per process) it gives a unique
// suffix without depending on wall-clock granularity. The previous
// implementation used time.Now().String()[:8] which collapsed to the
// calendar-month prefix (e.g. "2026-05-") for every call in the same
// month — same suffix → same room name → second create rejected as
// duplicate. Fixed 2026-05-24 after the first end-to-end run surfaced
// it.
var autoCounter atomic.Uint64

// autoValue returns the runtime-generated unique value for `$auto`.
// Format: `it-<runID>-room-auto-<counter>`. Deterministic and
// collision-free within one process run.
func autoValue() string {
	n := autoCounter.Add(1)
	return fmt.Sprintf("it-%s-room-auto-%d", currentRunID(), n)
}
