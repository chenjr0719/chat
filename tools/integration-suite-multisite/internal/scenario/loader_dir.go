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
func LoadAllInDir(root string) (int, []error) {
	var loaded int
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
		if _, err := LoadFile(path); err != nil {
			errs = append(errs, err)
			return nil
		}
		loaded++
		return nil
	})
	if walkErr != nil && len(errs) == 0 {
		errs = append(errs, walkErr)
	}
	return loaded, errs
}
