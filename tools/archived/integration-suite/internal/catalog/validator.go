package catalog

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/readers"
)

// Validate checks that every executor name referenced from the catalog
// YAML resolves to a Go symbol (knownExecutors), and every service owner
// listed on a reader is a real service (knownServices).
//
// Returns a list of error strings; empty list = catalog is valid.
func Validate(c *Catalog, knownExecutors, knownServices map[string]bool) []string {
	var errs []string
	for _, v := range c.Verbs {
		if !knownExecutors[v.Executor] {
			errs = append(errs, fmt.Sprintf("verb %q: executor %q not registered", v.Name, v.Executor))
		}
	}
	// index-based: Reader struct exceeds gocritic's 128-byte rangeValCopy threshold
	for i := range c.Readers {
		r := &c.Readers[i]
		if !knownExecutors[r.Executor] {
			errs = append(errs, fmt.Sprintf("reader %q: executor %q not registered", r.Location, r.Executor))
		}
		for _, o := range r.Owners {
			if !knownServices[o] {
				errs = append(errs, fmt.Sprintf("reader %q: owner %q is not a registered service", r.Location, o))
			}
		}
	}
	return errs
}

// ValidateForMishap enforces Part-2 schema requirements: every
// Service in the catalog must declare `container:` (so mishap.DockerCLI
// knows what to target). mishap_eligible defaults to false at the YAML
// layer; scenarios opt services in explicitly. This validator only
// gates the container field. Accumulates all errors so a single run
// reports every offender.
func ValidateForMishap(cat *Catalog) error {
	var errs []error
	for _, svc := range cat.Services {
		if strings.TrimSpace(svc.Container) == "" {
			errs = append(errs, fmt.Errorf("service %q: missing required field 'container:' (Part-2 §4.2)", svc.Name))
		}
	}
	return errors.Join(errs...)
}

var validReaderEventTypes = map[readers.EventType]bool{
	readers.EventCascade:         true,
	readers.EventFailure:         true,
	readers.EventRestartNoise:    true,
	readers.EventDisconnectNoise: true,
	readers.EventBackground:      true,
}

func allowedReaderEventTypes() string {
	keys := make([]string, 0, len(validReaderEventTypes))
	for k := range validReaderEventTypes {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

// ValidateReaderEvents enforces that every reader's events[].type is
// set to one of the documented values (§4.6.0). The 'unmatched' type
// is auto-assigned by the reader; not declarable in YAML. Accumulates
// all errors so a single run reports every offender.
func ValidateReaderEvents(cat *Catalog) error {
	var errs []error
	// index-based: Reader struct exceeds gocritic's 128-byte rangeValCopy threshold
	for i := range cat.Readers {
		r := &cat.Readers[i]
		for j, ev := range r.Events {
			if strings.TrimSpace(string(ev.Type)) == "" {
				errs = append(errs, fmt.Errorf("reader %q event[%d]: missing 'type:' (§4.6.0)", r.Location, j))
				continue
			}
			if !validReaderEventTypes[ev.Type] {
				errs = append(errs, fmt.Errorf("reader %q event[%d]: unknown type %q (allowed: %s)", r.Location, j, ev.Type, allowedReaderEventTypes()))
			}
		}
	}
	return errors.Join(errs...)
}
