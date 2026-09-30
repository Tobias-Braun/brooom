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

// TestCommandQuotesPaths: a worktree path with spaces or shell
// metacharacters must stay one word in the suggested command.
func TestCommandQuotesPaths(t *testing.T) {
	// The commands quote in the host shell's dialect (POSIX on unix,
	// cmd.exe/PowerShell on Windows), so the expected word comes from the
	// same host-aware helper; the dialects themselves are pinned by the
	// findings package tests.
	paths := []string{"/w/plain", "/w/my tree", "/w/a;rm -rf ~", "/w/it's$HOME"}
	s := &scan{}
	for _, p := range paths {
		e := &entry{path: p, wt: gitx.Worktree{Path: p}}
		wantRemove := "git worktree remove -- " + findings.Quote(p)
		wantPrune := "git worktree remove --force -- " + findings.Quote(p)
		if got := s.command(e, findings.ActionRemoveWorktree); got != wantRemove {
			t.Errorf("remove command = %q, want %q", got, wantRemove)
		}
		if got := s.command(e, findings.ActionPruneWorktrees); got != wantPrune {
			t.Errorf("prune command = %q, want %q", got, wantPrune)
		}
	}
	// A path with spaces must never appear bare, whatever the dialect.
	e := &entry{path: "/w/my tree", wt: gitx.Worktree{Path: "/w/my tree"}}
	if got := s.command(e, findings.ActionRemoveWorktree); strings.HasSuffix(got, "-- /w/my tree") {
		t.Errorf("path with a space left unquoted: %q", got)
	}
}
