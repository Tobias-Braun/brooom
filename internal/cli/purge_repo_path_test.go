package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestPurgeRepoPath covers every branch of the directory choice for git
// maintenance, in particular the linked-worktree fallback that keeps purge
// working from a linked worktree without making the main checkout a general
// location.
func TestPurgeRepoPath(t *testing.T) {
	base := testutil.ResolvedTempDir(t)
	main := filepath.Join(base, "main")
	linked := filepath.Join(base, "linked")
	other := filepath.Join(base, "other")
	for _, d := range []string{main, linked, other} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	allowMain, err := scope.NewGuard(main)
	if err != nil {
		t.Fatal(err)
	}
	fromLinked, err := scope.NewGuard(linked)
	if err != nil {
		t.Fatal(err)
	}
	metaOnly, err := fromLinked.WithRepoMeta(main)
	if err != nil {
		t.Fatal(err)
	}
	fromOther, err := scope.NewGuard(other)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		guard  *scope.Guard
		linked string
		want   string
		ok     bool
	}{
		{"main worktree allowed", allowMain, linked, main, true},
		{"main only as metadata falls back to the linked worktree", metaOnly, linked, linked, true},
		{"main only as metadata and linked outside the guard", metaOnly, other, "", false},
		{"main neither allowed nor metadata", fromOther, linked, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := purgeRepoPath(tc.guard, main, tc.linked)
			if got != tc.want || ok != tc.ok {
				t.Errorf("purgeRepoPath = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}
