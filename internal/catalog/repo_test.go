package catalog

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEncodeClaudeProject(t *testing.T) {
	for in, want := range map[string]string{
		"/Volumes/Developer/Developer/private/brooom":        "-Volumes-Developer-Developer-private-brooom",
		"/Volumes/Developer/brooom/.claude/worktrees/cli-ux": "-Volumes-Developer-brooom--claude-worktrees-cli-ux",
		`C:\Users\me\app`:    "C--Users-me-app",
		"/home/me/my app.v2": "-home-me-my-app-v2",
	} {
		if got := encodeClaudeProject(in); got != want {
			t.Errorf("encode(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBelongsOnlyToTheRepository pins the rule that keeps the lossy encoding
// safe: the exact name or a worktree folder of the repository, never a
// sibling that merely shares the prefix.
func TestBelongsOnlyToTheRepository(t *testing.T) {
	exact, prefixes := repoKeys(encodeClaudeProject, []string{"/w/app"})
	for name, want := range map[string]bool{
		"-w-app":                      true,
		"-w-app--claude-worktrees-a1": true,
		"-w-app--worktrees-feat":      true,
		"-w-app-site":                 false,
		"-w-application":              false,
		"-w-app--claude-worktrees-":   false,
		"-w-other":                    false,
		"-W-APP":                      false,
	} {
		if got := belongs(name, exact, prefixes, false); got != want {
			t.Errorf("belongs(%q) = %v, want %v", name, got, want)
		}
	}
	if !belongs("-W-APP", exact, prefixes, true) {
		t.Error("a case-folding file system must match regardless of case")
	}
}

// TestRepoLocationsListOnlyExistingDirectories: each directory of the
// repository is its own base, missing ones and plain files yield nothing.
func TestRepoLocationsListOnlyExistingDirectories(t *testing.T) {
	m := newAIMachine(t)
	projects := m.userPath(".claude/projects")
	for _, d := range []string{"-w-app", "-w-app--claude-worktrees-x", "-w-app-site"} {
		if err := os.MkdirAll(filepath.Join(projects, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(projects, "-w-app--worktrees-file"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	c := embeddedCatalog(t, "linux")
	var bases []string
	for _, l := range c.RepoLocations(m.env("linux"), []string{"/w/app"}, CategoryAI) {
		bases = append(bases, filepath.Base(l.Base))
	}
	slices.Sort(bases)
	if want := []string{"-w-app", "-w-app--claude-worktrees-x"}; !slices.Equal(bases, want) {
		t.Errorf("bases = %v, want %v", bases, want)
	}
	if got := c.RepoLocations(m.env("linux"), nil, CategoryAI); len(got) != 0 {
		t.Errorf("without repositories: %v", got)
	}
	// RepoLocationsAt finds the same location from its base alone.
	base := filepath.Join(projects, "-w-app")
	at := c.RepoLocationsAt(m.env("linux"), base, CategoryAI)
	if len(at) != 1 || at[0].Base != base || at[0].ToolID != "claude-code" {
		t.Errorf("RepoLocationsAt = %+v", at)
	}
	if got := c.RepoLocationsAt(m.env("linux"), projects, CategoryAI); len(got) != 0 {
		t.Errorf("the projects directory itself is no location: %+v", got)
	}
}

func TestValidateRepoPattern(t *testing.T) {
	for _, tc := range []struct {
		pat, key, want string
	}{
		{"~/.claude/projects/{repo}/*.jsonl", "claude-code", ""},
		{"~/.claude/projects/*.jsonl", "", ""},
		{"~/.claude/projects/{repo}/*.jsonl", "", "needs repo_key"},
		{"~/.claude/projects/{repo}/*.jsonl", "nope", "needs repo_key"},
		{"~/.claude/p-{repo}/*.jsonl", "claude-code", "whole path segment"},
		{"~/.claude/{repo}/{repo}/x", "claude-code", "used once"},
		{"~/.claude/*/{repo}/x", "claude-code", "no wildcard"},
	} {
		err := validateRepoPattern(tc.pat, tc.key)
		if (tc.want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%s (%q): err %v, want %q", tc.pat, tc.key, err, tc.want)
		}
	}
}
