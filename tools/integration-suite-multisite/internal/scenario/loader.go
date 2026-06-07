package scenario

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Any is the marker interface implemented by *Scenario. Phase 3 burnt
// the v2 path, so today there is only one shape — but the interface is
// preserved so callers (and a future v4) don't have to re-thread the
// type-switch.
type Any interface {
	isScenario()
}

func (*Scenario) isScenario() {}

// LoadFile reads a scenario YAML at the given path and returns
// *Scenario (wrapped in Any so the runner.go signature stays
// stable). Rejects v2 `sequence:` shape with a clear migration error.
func LoadFile(path string) (Any, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("scenario: read %s: %w", path, err)
	}

	// Peek shape to give v2-leftover scenarios a clear migration error
	// instead of a vague unmarshal failure.
	var shape map[string]any
	if err := yaml.Unmarshal(body, &shape); err != nil {
		return nil, fmt.Errorf("scenario: parse %s: %w", path, err)
	}
	if _, hasSequence := shape["sequence"]; hasSequence {
		return nil, fmt.Errorf("scenario %s: legacy v2 `sequence:` shape no longer supported — migrate to v3 shape (see SCENARIO-REFERENCE.md)", path)
	}

	return loadScenarioBody(path, body)
}

// loadScenarioBody parses a scenario and runs light validation: required
// fields present. Additional validation happens at the runtime boundary
// (Sandbox.Setup) where more context is available.
func loadScenarioBody(path string, data []byte) (*Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("scenario: parse %s: %w", path, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("scenario %s: missing `scenario:` name", path)
	}
	if s.Source == "" {
		return nil, fmt.Errorf("scenario %s: missing `source:` citation", path)
	}

	if err := rejectDeprecatedTokens(&s); err != nil {
		return nil, err
	}
	if err := ValidateSiteFields(&s); err != nil {
		return nil, err
	}

	s.SourcePath = path
	return &s, nil
}

// rejectDeprecatedTokens scans the scenario for tokens that were
// removed in the multi-site fork. Run after YAML parse, before any
// validation that uses the parsed values.
func rejectDeprecatedTokens(s *Scenario) error {
	deprecated := []string{
		"${site}",
		"${siteA}",
		"${siteB}",
	}
	check := func(field, value string) error {
		for _, tok := range deprecated {
			if strings.Contains(value, tok) {
				return fmt.Errorf("scenario %q: %s contains deprecated token %q — write the site name literally (e.g. \"site-a\")", s.Name, field, tok)
			}
		}
		if strings.Contains(value, "${service.") {
			return fmt.Errorf("scenario %q: %s contains ${service.*.credential} which is removed in multi-site; use user creds only", s.Name, field)
		}
		// Reject ${<alias>.site} as a substitution token. Match
		// pattern ${<word>.site} where <word> is the alias.
		// Quick rejection: any occurrence of ".site}" hints at it.
		if strings.Contains(value, ".site}") {
			return fmt.Errorf("scenario %q: %s contains ${<alias>.site} which is removed in multi-site; write the site name literally", s.Name, field)
		}
		return nil
	}

	if err := check("input.subject", s.Input.Subject); err != nil {
		return err
	}
	if err := check("input.credential", s.Input.Credential); err != nil {
		return err
	}
	for k, v := range s.Input.Payload {
		if vs, ok := v.(string); ok {
			if err := check(fmt.Sprintf("input.payload.%s", k), vs); err != nil {
				return err
			}
		}
	}
	for i, e := range s.Expected {
		for k, v := range e.Match {
			if vs, ok := v.(string); ok {
				if err := check(fmt.Sprintf("expected[%d].match.%s", i, k), vs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
