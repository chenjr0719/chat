package scenario

import (
	"fmt"
	"os"

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
// stable). Files without `cases:` are rejected with a clear
// "expected" error — the v2 `sequence:` shape no longer parses.
func LoadFile(path string) (Any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Peek shape to give v2-leftover scenarios a clear migration error
	// instead of a vague unmarshal failure.
	var shape map[string]any
	if err := yaml.Unmarshal(data, &shape); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	if _, hasSequence := shape["sequence"]; hasSequence {
		return nil, fmt.Errorf("scenario %s: legacy v2 `sequence:` shape no longer supported — migrate to v3 `cases:` (see SCENARIO-REFERENCE.md)", path)
	}
	if _, hasCases := shape["cases"]; !hasCases {
		return nil, fmt.Errorf("scenario %s: missing `cases:` block (v3 required)", path)
	}

	return loadScenarioBody(path, data)
}

// loadScenarioBody parses a scenario and runs light validation: required
// fields present, kind sane. Effect-flag catalog validation happens at
// the runtime boundary (Sandbox.Setup) where the effect registry is
// available.
func loadScenarioBody(path string, data []byte) (*Scenario, error) {
	var s Scenario
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	if s.Name == "" {
		return nil, fmt.Errorf("scenario %s: missing `scenario:` name", path)
	}
	if s.Source == "" {
		return nil, fmt.Errorf("scenario %s: missing `source:` citation", path)
	}
	if s.BaseInput.Verb == "" {
		return nil, fmt.Errorf("scenario %s: missing `base_input.verb`", path)
	}
	if len(s.Cases) == 0 {
		return nil, fmt.Errorf("scenario %s: cases: list must have at least one entry", path)
	}
	seen := map[string]bool{}
	for i, c := range s.Cases {
		if c.Name == "" {
			return nil, fmt.Errorf("scenario %s: cases[%d] missing name", path, i)
		}
		if seen[c.Name] {
			return nil, fmt.Errorf("scenario %s: cases[%d] duplicate name %q", path, i, c.Name)
		}
		seen[c.Name] = true
		if c.Tag != "positive" && c.Tag != "negative" {
			return nil, fmt.Errorf("scenario %s: cases[%d] tag must be 'positive' or 'negative', got %q", path, i, c.Tag)
		}
		if len(c.Expected) == 0 {
			return nil, fmt.Errorf("scenario %s: cases[%d] expected: must have at least one entry", path, i)
		}
	}
	return &s, nil
}
