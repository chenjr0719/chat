package readers

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// ContainerLogEventRule is one compiled (pattern, type) pair the
// reader consults at emit time to classify a log line. The slice
// order is meaningful: classify() returns the first matching rule's
// Type (§4.6.0 first-match-wins).
type ContainerLogEventRule struct {
	Pattern string    // regex source; preserved for diagnostics
	Type    EventType // the EventType assigned when Pattern matches a line
}

// compiledRule is the runtime form of a ContainerLogEventRule with
// its regexp precompiled once at reader construction.
type compiledRule struct {
	re  *regexp.Regexp
	typ EventType
}

// ContainerLogsReader tails `docker logs -f <container>` from the
// start of the scenario and emits one Event per JSON-parseable line.
// Event.Traceparent is populated from the line's `traceparent` field
// when present, empty otherwise. The verdict classifier handles
// trace presence/absence at verdict time — this reader does NOT
// filter by trace.
//
// Event classification (§4.6.0): ignoreREs are evaluated first
// (matching line is DROPPED, no event emitted), then eventREs are
// evaluated in declaration order (first match wins, sets Event.Type).
// If no rule matches, Event.Type is EventUnmatched. With both lists
// empty (the default/baseline) every emitted event carries
// EventUnmatched — preserving the pre-wiring behavior.
type ContainerLogsReader struct {
	Container string // docker container name
	Service   string // service name (for OwnerSvc)
	Location  string // catalog location (e.g., "logs.room-service")

	eventREs  []compiledRule   // first-match-wins, in declaration order
	ignoreREs []*regexp.Regexp // any match drops the line before classification
}

// NewContainerLogsReader returns a ContainerLogsReader for the named
// container with no classification rules. Every emitted event carries
// Type=EventUnmatched. Convenience constructor for callers that have
// no catalog rules to wire (tests, legacy registration sites).
func NewContainerLogsReader(container, service, location string) *ContainerLogsReader {
	// nil/nil cannot fail to compile; err is unreachable.
	r, _ := NewContainerLogsReaderWithRules(container, service, location, nil, nil)
	return r
}

// NewContainerLogsReaderWithRules builds a ContainerLogsReader and
// pre-compiles its event/ignore regex rules per §4.6.0. Compilation
// happens ONCE at construction so per-line cost is just the matcher
// loop. An invalid regex in either list is a hard construction error:
// the returned error contains the offending pattern and the verbatim
// regexp.Compile error verbatim so the engineer can fix the YAML.
func NewContainerLogsReaderWithRules(container, service, location string, events []ContainerLogEventRule, ignore []string) (*ContainerLogsReader, error) {
	r := &ContainerLogsReader{Container: container, Service: service, Location: location}
	for _, e := range events {
		re, err := regexp.Compile(e.Pattern)
		if err != nil {
			return nil, fmt.Errorf("compile event pattern %q: %w", e.Pattern, err)
		}
		r.eventREs = append(r.eventREs, compiledRule{re: re, typ: e.Type})
	}
	for _, p := range ignore {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("compile ignore pattern %q: %w", p, err)
		}
		r.ignoreREs = append(r.ignoreREs, re)
	}
	return r, nil
}

// classify returns (Type, emit) for one raw log line per §4.6.0:
//   - if any ignore regex matches → (_, false): caller drops the line.
//   - else if any event regex matches → (rule.Type, true), first-match-wins.
//   - else → (EventUnmatched, true).
func (r *ContainerLogsReader) classify(line string) (EventType, bool) {
	for _, re := range r.ignoreREs {
		if re.MatchString(line) {
			return "", false
		}
	}
	for _, rule := range r.eventREs {
		if rule.re.MatchString(line) {
			return rule.typ, true
		}
	}
	return EventUnmatched, true
}

// Watch starts `docker logs -f` on the container and emits one Event per
// structured JSON log line whose timestamp is >= start. `traceparent` is
// accepted to satisfy the Reader interface but not used — slog lines
// carry their own `traceparent` field when production wires OTel into
// the logger, which is the source-of-truth this reader extracts.
//
// Container resolution: the literal `r.Container` name (set at
// registration) is the FALLBACK. The primary path queries
// `docker ps --filter label=com.docker.compose.service=<r.Service>`
// so the reader works regardless of docker-compose's project-name
// prefix (e.g. `chat-local-services-room-worker-1` vs literal
// `room-worker`). §9.8 of the corrections spec.
func (r *ContainerLogsReader) Watch(ctx context.Context, _ string, start time.Time) (<-chan Event, error) {
	containerRef := r.resolveContainerRef(ctx)
	out := make(chan Event, 64)
	cmd := exec.CommandContext(ctx, "docker", "logs", "-f", "--since", start.Format(time.RFC3339), containerRef)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("logs reader: pipe %s: %w", containerRef, err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("logs reader: start %s: %w", containerRef, err)
	}

	go func() {
		defer close(out)
		scanner := bufio.NewScanner(stdout)
		// Allow lines up to 1 MiB
		buf := make([]byte, 0, 64*1024)
		scanner.Buffer(buf, 1024*1024)
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			typ, emit := r.classify(line)
			if !emit {
				continue
			}
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err != nil {
				// Not JSON; skip
				continue
			}
			ts := parseLogTime(entry)
			if ts.Before(start) {
				continue
			}
			tp, _ := entry["traceparent"].(string)
			out <- Event{
				Location:    r.Location,
				Timestamp:   ts,
				Traceparent: tp,
				OwnerSvc:    r.Service,
				Payload:     entry,
				Type:        typ,
			}
		}
		// Reap the `docker logs -f` subprocess and surface a non-zero
		// exit loudly. The previous //nolint:errcheck swallowed bad-
		// container-name failures: `docker logs <missing>` writes to
		// stderr (uncaptured), exits non-zero, and the empty-stdout
		// scanner already returned false above — so the assertion looks
		// indistinguishable from "log line never appeared" (worst case:
		// false-green on `not: true` assertions). The ctx.Err() guard
		// suppresses the warning on normal Sandbox.Teardown, where
		// the harness cancels the context to stop the follow.
		if waitErr := cmd.Wait(); waitErr != nil && ctx.Err() == nil {
			slog.Warn(
				"logs_tail: docker logs exited non-zero — container likely not resolvable by the literal name "+
					"(or the docker-compose service label query returned an unresolvable ref)",
				"container_ref", containerRef,
				"err", waitErr,
			)
		}
	}()
	return out, nil
}

// resolveContainerRef returns a docker-CLI-usable reference (ID or
// name) for the container hosting this reader's service. Strategy:
//  1. Query `docker ps --filter label=com.docker.compose.service=<svc>`
//     for the first match — the compose service label is set by docker
//     compose regardless of the project's name-prefixing scheme.
//  2. Fall back to the literal `r.Container` name if step 1 returns
//     nothing (e.g. running under plain `docker run` without compose).
//
// Errors from docker are also fallback triggers so this never blocks
// the caller — the failure mode is "no events emitted" via the later
// docker logs subprocess, which is the same as the pre-§9.8 behavior.
func (r *ContainerLogsReader) resolveContainerRef(ctx context.Context) string {
	cmd := exec.CommandContext(ctx, "docker", "ps",
		"--filter", fmt.Sprintf("label=com.docker.compose.service=%s", r.Service),
		"--format", "{{.ID}}")
	out, err := cmd.Output()
	if err != nil {
		return r.Container
	}
	first := strings.TrimSpace(string(out))
	if first == "" {
		return r.Container
	}
	// Multiple matches → take the first line.
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = strings.TrimSpace(first[:i])
	}
	if first == "" {
		return r.Container
	}
	return first
}

// parseLogTime extracts the timestamp from a slog JSON log entry.
// The slog JSON handler emits a "time" field in RFC3339Nano.
func parseLogTime(entry map[string]any) time.Time {
	if s, ok := entry["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t
		}
	}
	return time.Now()
}
