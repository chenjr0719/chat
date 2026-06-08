package infra

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveRepoRoot returns Config.RepoRoot if set, else walks up from
// the current working directory until it finds a go.mod whose first
// `module` line is `module github.com/hmchangw/chat`. Returns an
// error if no such ancestor exists.
func resolveRepoRoot(cfg *Config) (string, error) {
	if cfg.RepoRoot != "" {
		return cfg.RepoRoot, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	dir := wd
	for {
		if isChatModuleRoot(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("infra: walked to filesystem root without finding repo go.mod")
		}
		dir = parent
	}
}

func isChatModuleRoot(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return line == "module github.com/hmchangw/chat"
		}
	}
	return false
}
