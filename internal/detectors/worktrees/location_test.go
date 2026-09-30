package worktrees

import (
	"path/filepath"
	"testing"
)

func TestAgentLocation(t *testing.T) {
	tests := []struct {
		name string
		goos string
		repo string
		wt   string
		want string
	}{
		{"claude worktrees", "linux", "/src/repo", "/src/repo/.claude/worktrees/a", conventionClaude},
		{"nested below claude worktrees", "linux", "/src/repo", "/src/repo/.claude/worktrees/a/b", conventionClaude},
		{"dot worktrees", "linux", "/src/repo", "/src/repo/.worktrees/a", conventionDotDir},
		{"the convention directory itself is not a worktree", "linux", "/src/repo", "/src/repo/.claude/worktrees", ""},
		{"sibling wt", "linux", "/src/repo", "/src/repo-wt1", conventionSibling},
		{"sibling wt with suffix", "linux", "/src/repo", "/src/repo-wt-feature", conventionSibling},
		{"other sibling", "linux", "/src/repo", "/src/repo-other", ""},
		{"wt directory in another parent", "linux", "/src/repo", "/elsewhere/repo-wt1", ""},
		{"nested below a sibling is not a sibling", "linux", "/src/repo", "/src/repo-wt1/inner", ""},
		{"string prefix is not containment", "linux", "/src/repo", "/src/repo.claude/worktrees/a", ""},
		{"case matters on linux", "linux", "/src/repo", "/src/repo/.Claude/Worktrees/a", ""},
		{"case is ignored on windows", "windows", "/src/Repo", "/src/repo/.CLAUDE/Worktrees/a", conventionClaude},
		{"case is ignored on darwin for siblings", "darwin", "/src/Repo", "/src/repo-WT2", conventionSibling},
		{"trailing separators and dots are cleaned", "linux", "/src/repo/", "/src/repo/./.worktrees/../.worktrees/a/", conventionDotDir},
		{"plain worktree elsewhere", "linux", "/src/repo", "/tmp/feature", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentLocation(tt.goos, filepath.FromSlash(tt.repo), filepath.FromSlash(tt.wt))
			if got != tt.want {
				t.Errorf("agentLocation(%q, %q, %q) = %q, want %q", tt.goos, tt.repo, tt.wt, got, tt.want)
			}
		})
	}
}
