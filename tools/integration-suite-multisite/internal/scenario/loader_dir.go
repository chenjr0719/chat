package scenario

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// LoadAllInDir walks root recursively for *.yaml / *.yml files and
// calls LoadFile on each. Returns the count of successful parses and
// a slice of errors (one per failing file). Non-existent root returns
// (0, nil) — a missing drafts/ directory is not a validator failure.
//
// Per-file parse errors only — does NOT run cross-scenario checks
// like CrossScenarioCheck (which need the parsed scenarios together).
// Callers wanting cross-scenario validation should use
// LoadAllParsedInDir instead.
func LoadAllInDir(root string) (int, []error) {
	scs, errs := LoadAllParsedInDir(root)
	return len(scs), errs
}

// LoadAllParsedInDir is the variant of LoadAllInDir that returns the
// parsed scenarios. Same walk semantics; callers needing
// CrossScenarioCheck use this so they get the *Scenario values they
// need to pass to the check.
func LoadAllParsedInDir(root string) ([]*Scenario, []error) {
	var scenarios []*Scenario
	var errs []error
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Treat missing root as a no-op; surface other walk errors.
			if path == root {
				return filepath.SkipDir
			}
			errs = append(errs, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if ext != ".yaml" && ext != ".yml" {
			return nil
		}
		loaded, err := LoadFile(path)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		s, ok := loaded.(*Scenario)
		if !ok {
			// LoadFile only returns *Scenario today; defensive
			// against future Any variants.
			return nil
		}
		scenarios = append(scenarios, s)
		return nil
	})
	if walkErr != nil && len(errs) == 0 {
		errs = append(errs, walkErr)
	}
	return scenarios, errs
}
