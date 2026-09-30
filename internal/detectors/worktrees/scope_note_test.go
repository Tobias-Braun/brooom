package worktrees_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detectors/worktrees"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// runErr is run that returns the detector error instead of failing, because
// a non-fatal note comes back as an error next to the findings.
func (h *harness) runErr(target scope.Target) ([]findings.Finding, error) {
	h.t.Helper()
	var out []findings.Finding
	err := worktrees.New().Detect(context.Background(), h.env, target, func(f findings.Finding) { out = append(out, f) })
	return out, err
}

// TestOutOfScopeWorktreeIsReportedAsNote pins the visibility rule: a linked
// worktree outside the guard is still never listed, but the scan says why and
// names the way forward instead of printing "nothing to clean".
func TestOutOfScopeWorktreeIsReportedAsNote(t *testing.T) {
	repo := testutil.NewRepo(t)
	outside := repo.AddWorktree("outside", "feat-outside")
	// The guard only allows the repository, like default single-repo mode;
	// the linked worktree lives in another temp directory.
	h := newHarness(t, repo)

	fs, err := h.runErr(repoTarget(repo.Dir))
	if len(fs) != 0 {
		t.Errorf("reported %+v", fs)
	}
	if err == nil {
		t.Fatal("no note for the worktree outside the scope")
	}
	for _, want := range []string{gitx.NormalizePath(outside), scope.OutsideWorktreeHint} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("note lacks %q: %v", want, err)
		}
	}
}

// TestInScopeAndMissingWorktreesGetNoNote makes sure the note is only for
// existing worktrees the guard refuses: allowed ones are reported normally and
// a missing one is prunable metadata, reported without a note.
func TestInScopeAndMissingWorktreesGetNoNote(t *testing.T) {
	repo := testutil.NewRepo(t)
	inside := filepath.Join(repo.Dir, ".claude", "worktrees", "in")
	repo.Git("worktree", "add", "-q", "-b", "in", inside)
	gone := repo.AddWorktree("gone", "feat-gone")
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, repo)

	fs, err := h.runErr(repoTarget(repo.Dir))
	if err != nil {
		t.Errorf("unexpected note: %v", err)
	}
	if len(fs) != 2 {
		t.Errorf("got %d findings, want 2: %+v", len(fs), fs)
	}
}

// metaHarness runs from the linked worktree own, whose main checkout is only
// repository metadata for the guard. It returns the harness plus a live
// sibling and a missing sibling below the main checkout.
func metaHarness(t *testing.T) (h *harness, own, sibling string) {
	t.Helper()
	repo := testutil.NewRepo(t)
	sibling = filepath.Join(repo.Dir, ".claude", "worktrees", "sibling")
	repo.Git("worktree", "add", "-q", "-b", "sibling", sibling)
	gone := filepath.Join(repo.Dir, ".claude", "worktrees", "gone")
	repo.Git("worktree", "add", "-q", "-b", "gone", gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	own = repo.AddWorktree("own", "own")
	h = newHarness(t, repo)
	g, err := scope.NewGuard(own)
	if err != nil {
		t.Fatal(err)
	}
	if h.env.Guard, err = g.WithRepoMeta(repo.Dir); err != nil {
		t.Fatal(err)
	}
	return h, own, sibling
}

// TestSiblingBelowMainIsNotedFromLinkedWorktree is the detector side of the
// least-privilege rule: a sibling below the main checkout is not offered and
// gets a note, while a missing sibling is still reported as prunable.
func TestSiblingBelowMainIsNotedFromLinkedWorktree(t *testing.T) {
	h, own, sibling := metaHarness(t)

	fs, err := h.runErr(repoTarget(own))
	if len(fs) != 1 || fs[0].Kind != findings.KindWorktreeMissing {
		t.Errorf("want only the missing sibling, got %+v", fs)
	}
	if err == nil || !strings.Contains(err.Error(), gitx.NormalizePath(sibling)) {
		t.Errorf("note = %v, want one naming the sibling", err)
	}
}
