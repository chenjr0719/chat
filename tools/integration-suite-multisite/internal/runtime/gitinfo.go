package runtime

import (
	"os/exec"
	"strings"
)

// GitInfo carries commit identifiers surfaced at the top of the
// run report.
type GitInfo struct {
	HEAD        string // short hash of HEAD at run time
	LatestMerge string // short hash of the most recent merge commit reachable from HEAD; "" if none
}

// CollectGitInfo runs git from `repoRoot` and returns the current HEAD
// plus the latest merge commit reachable from HEAD. Any command that
// fails leaves its field empty — the reporter handles missing values
// gracefully so a non-git environment doesn't break the run.
func CollectGitInfo(repoRoot string) GitInfo {
	out := GitInfo{}
	if h := runGit(repoRoot, "rev-parse", "--short", "HEAD"); h != "" {
		out.HEAD = h
	}
	if m := runGit(repoRoot, "log", "-1", "--merges", "--format=%h"); m != "" {
		out.LatestMerge = m
	}
	return out
}

func runGit(dir string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
