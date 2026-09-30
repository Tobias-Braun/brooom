package worktrees

import (
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/walk"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// TestMtimeSourceMeta: the finding says where LastModified came from, so the
// remove action can skip the drift check for a commit-time baseline.
func TestMtimeSourceMeta(t *testing.T) {
	tests := []struct {
		name  string
		entry *entry
		want  string
	}{
		{"walked mtime", &entry{sized: true, sum: walk.DirSummary{NewestModTime: time.Unix(1000, 0)}}, "walk"},
		{"sized but zero mtime", &entry{sized: true}, "commit"},
		{"not sized", &entry{}, "commit"},
	}
	s := &scan{}
	for _, tt := range tests {
		if got := s.meta(tt.entry)["mtime_source"]; got != tt.want {
			t.Errorf("%s: mtime_source = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// TestRemoveWorktreeHasNoBareGitCommand: `git worktree remove` on an existing
// directory permanently deletes ignored files such as .env, while the real
// action trashes the directory first. The finding therefore carries no such
// command and points at `brooom worktrees --apply` in its reason.
func TestRemoveWorktreeHasNoBareGitCommand(t *testing.T) {
	s := &scan{}
	for _, p := range []string{"/w/plain", "/w/my tree", "/w/a;rm -rf ~", "/w/it's$HOME"} {
		e := &entry{path: p, wt: gitx.Worktree{Path: p}}
		if got := s.command(e, findings.ActionRemoveWorktree); got != "" {
			t.Errorf("remove command for %q = %q, want empty", p, got)
		}
	}
	got := s.reason(findings.ActionRemoveWorktree, "merged into main")
	if !strings.Contains(got, "brooom worktrees --apply") || !strings.HasPrefix(got, "merged into main") {
		t.Errorf("remove reason lacks the pointer: %q", got)
	}
	if got := s.reason(findings.ActionPruneWorktrees, "gone"); got != "gone" {
		t.Errorf("prune reason changed: %q", got)
	}
}

// TestCommandQuotesPaths: a worktree path with spaces or shell
// metacharacters must stay one word in the suggested prune command.
func TestCommandQuotesPaths(t *testing.T) {
	// The command quotes in the host shell's dialect (POSIX on unix,
	// cmd.exe/PowerShell on Windows), so the expected word comes from the
	// same host-aware helper; the dialects themselves are pinned by the
	// findings package tests.
	s := &scan{}
	for _, p := range []string{"/w/plain", "/w/my tree", "/w/a;rm -rf ~", "/w/it's$HOME"} {
		e := &entry{path: p, wt: gitx.Worktree{Path: p}}
		want := "git worktree remove --force -- " + findings.Quote(p)
		if got := s.command(e, findings.ActionPruneWorktrees); got != want {
			t.Errorf("prune command = %q, want %q", got, want)
		}
	}
	e := &entry{path: "/w/my tree", wt: gitx.Worktree{Path: "/w/my tree"}}
	if got := s.command(e, findings.ActionPruneWorktrees); strings.HasSuffix(got, "-- /w/my tree") {
		t.Errorf("path with a space left unquoted: %q", got)
	}
}
