package worktrees

import (
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

// TestCommandQuotesPaths: a worktree path with spaces or shell
// metacharacters must stay one word in the suggested command.
func TestCommandQuotesPaths(t *testing.T) {
	tests := []struct {
		path       string
		wantRemove string
		wantPrune  string
	}{
		{"/w/plain", "git worktree remove -- /w/plain", "git worktree remove --force -- /w/plain"},
		{"/w/my tree", "git worktree remove -- '/w/my tree'", "git worktree remove --force -- '/w/my tree'"},
		{"/w/a;rm -rf ~", "git worktree remove -- '/w/a;rm -rf ~'", "git worktree remove --force -- '/w/a;rm -rf ~'"},
		{"/w/it's$HOME", `git worktree remove -- '/w/it'\''s$HOME'`, `git worktree remove --force -- '/w/it'\''s$HOME'`},
	}
	s := &scan{}
	for _, tt := range tests {
		e := &entry{path: tt.path, wt: gitx.Worktree{Path: tt.path}}
		if got := s.command(e, findings.ActionRemoveWorktree); got != tt.wantRemove {
			t.Errorf("remove command = %q, want %q", got, tt.wantRemove)
		}
		if got := s.command(e, findings.ActionPruneWorktrees); got != tt.wantPrune {
			t.Errorf("prune command = %q, want %q", got, tt.wantPrune)
		}
	}
}
