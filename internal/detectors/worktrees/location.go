package worktrees

import (
	"path/filepath"
	"strings"
)

// Directory conventions agent tools use for the linked worktrees they create.
// They are context only: no convention makes a worktree removable by itself,
// a human may just as well have created a directory there.
const (
	conventionClaude  = ".claude/worktrees"
	conventionDotDir  = ".worktrees"
	conventionSibling = "<repo>-wt*"
)

// agentLocation returns the agent directory convention the worktree at wtPath
// follows relative to the main worktree at repoRoot, or "" when it follows
// none. Paths are compared component-wise, never as string prefixes, so
// "/src/repo-other" is not below "/src/repo" and "/src/repo-wt1" is only a
// sibling because its parent directory is the parent of the repository.
// goos selects case-insensitive comparison for Windows and macOS, whose
// default filesystems ignore case; it is a parameter so the Windows rules can
// be tested on any OS.
func agentLocation(goos, repoRoot, wtPath string) string {
	repo, wt := splitPath(repoRoot), splitPath(wtPath)
	for _, c := range []string{conventionClaude, conventionDotDir} {
		base := append(append([]string{}, repo...), strings.Split(c, "/")...)
		if rest, ok := trimPrefix(goos, base, wt); ok && len(rest) > 0 {
			return c
		}
	}
	if len(repo) < 2 || len(wt) != len(repo) {
		return ""
	}
	last := len(repo) - 1
	if !sameComponents(goos, repo[:last], wt[:last]) {
		return ""
	}
	if hasFoldPrefix(goos, wt[last], repo[last]+"-wt") {
		return conventionSibling
	}
	return ""
}

// splitPath cleans p and splits it into its volume (empty on Unix) and its
// separator-delimited components.
func splitPath(p string) []string {
	p = filepath.Clean(p)
	vol := filepath.VolumeName(p)
	rest := strings.Trim(p[len(vol):], string(filepath.Separator))
	parts := []string{vol}
	if rest != "" {
		parts = append(parts, strings.Split(rest, string(filepath.Separator))...)
	}
	return parts
}

// foldsCase reports whether the OS default filesystem ignores case.
func foldsCase(goos string) bool {
	return goos == "windows" || goos == "darwin"
}

func sameComponent(goos, a, b string) bool {
	if foldsCase(goos) {
		return strings.EqualFold(a, b)
	}
	return a == b
}

func sameComponents(goos string, a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !sameComponent(goos, a[i], b[i]) {
			return false
		}
	}
	return true
}

// trimPrefix returns the components of p below base and whether base is a
// component-wise prefix of p.
func trimPrefix(goos string, base, p []string) ([]string, bool) {
	if len(p) < len(base) || !sameComponents(goos, base, p[:len(base)]) {
		return nil, false
	}
	return p[len(base):], true
}

func hasFoldPrefix(goos, s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	return sameComponent(goos, s[:len(prefix)], prefix)
}
