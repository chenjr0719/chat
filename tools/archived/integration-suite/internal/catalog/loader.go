package catalog

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Load reads every YAML file under catalogsRoot and returns a Catalog.
// Missing subdirectories are tolerated and produce empty slices.
func Load(catalogsRoot string) (*Catalog, error) {
	c := &Catalog{}

	if err := loadDir(filepath.Join(catalogsRoot, "verbs"), &c.Verbs); err != nil {
		return nil, fmt.Errorf("load verbs: %w", err)
	}
	if err := loadDir(filepath.Join(catalogsRoot, "readers"), &c.Readers); err != nil {
		return nil, fmt.Errorf("load readers: %w", err)
	}
	if err := loadDir(filepath.Join(catalogsRoot, "services"), &c.Services); err != nil {
		return nil, fmt.Errorf("load services: %w", err)
	}
	if err := loadDir(filepath.Join(catalogsRoot, "mishaps"), &c.Mishaps); err != nil {
		return nil, fmt.Errorf("load mishaps: %w", err)
	}
	if err := loadDir(filepath.Join(catalogsRoot, "seed-effects"), &c.SeedEffects); err != nil {
		return nil, fmt.Errorf("load seed-effects: %w", err)
	}
	if err := loadFile(filepath.Join(catalogsRoot, "matchers.yaml"), &c.Matchers); err != nil {
		return nil, fmt.Errorf("load matchers: %w", err)
	}

	return c, nil
}

// loadDir reads every *.yaml in dir, unmarshals each into one T, appends to out.
// Missing dir is OK.
func loadDir[T any](dir string, out *[]T) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read dir %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".yaml" {
			continue
		}
		var item T
		if err := loadFile(filepath.Join(dir, e.Name()), &item); err != nil {
			return err
		}
		*out = append(*out, item)
	}
	return nil
}

// loadFile reads one YAML file into v. Missing file is OK (leaves v at zero value).
func loadFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := yaml.Unmarshal(data, v); err != nil {
		return fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return nil
}
