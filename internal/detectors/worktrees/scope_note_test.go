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

// assertOutsideFinding checks that fs holds exactly one informational finding
// for path: no action, low confidence, the outside_scope evidence and the hint.
func assertOutsideFinding(t *testing.T, fs []findings.Finding, path string) {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("got %d findings, want the one informational finding: %+v", len(fs), fs)
	}
	f := fs[0]
	if f.SuggestedAction.Type != findings.ActionNone || f.Actionable() || f.SuggestedAction.Command != "" {
		t.Errorf("finding suggests an action: %+v", f.SuggestedAction)
	}
	if f.Confidence != findings.ConfidenceLow || f.Path != path {
		t.Errorf("confidence %q path %q, want low and %q", f.Confidence, f.Path, path)
	}
	if len(f.Evidence) != 1 || f.Evidence[0].Code != "outside_scope" {
		t.Fatalf("evidence = %+v, want one outside_scope entry", f.Evidence)
	}
	for _, want := range []string{path, scope.OutsideWorktreeHint} {
		if !strings.Contains(f.Evidence[0].Message, want) {
			t.Errorf("evidence lacks %q: %s", want, f.Evidence[0].Message)
		}
	}
}

// TestOutOfScopeWorktreeIsReportedAsFinding pins the visibility rule: a linked
// worktree outside the guard is never examined or offered for removal, but it
// is reported as an informational finding (visible without --verbose) that
// names the way forward instead of leaving the scan looking empty.
func TestOutOfScopeWorktreeIsReportedAsFinding(t *testing.T) {
	repo := testutil.NewRepo(t)
	outside := repo.AddWorktree("outside", "feat-outside")
	// The guard only allows the repository, like default single-repo mode;
	// the linked worktree lives in another temp directory.
	h := newHarness(t, repo)

	fs, err := h.runErr(repoTarget(repo.Dir))
	if err != nil {
		t.Fatalf("outside worktree is not an error: %v", err)
	}
	assertOutsideFinding(t, fs, gitx.NormalizePath(outside))
}

// TestMergedOutOfScopeWorktreeIsNeverSuggested makes sure the informational
// finding replaces classification: even a worktree whose branch is merged
// gets no removal action while it is outside the guard.
func TestMergedOutOfScopeWorktreeIsNeverSuggested(t *testing.T) {
	repo := testutil.NewRepo(t)
	outside := repo.AddWorktree("outside", "feat-merged")
	h := newHarness(t, repo)

	fs, err := h.runErr(repoTarget(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Actionable() {
			t.Errorf("out-of-scope worktree %s suggested: %+v", outside, f.SuggestedAction)
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
// gets an informational finding, while a missing sibling is still reported as prunable.
func TestSiblingBelowMainIsNotedFromLinkedWorktree(t *testing.T) {
	h, own, sibling := metaHarness(t)

	fs, err := h.runErr(repoTarget(own))
	if len(fs) == 0 || fs[0].Kind != findings.KindWorktreeMissing {
		t.Errorf("want the missing sibling first, got %+v", fs)
	}
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(fs) != 2 {
		t.Fatalf("want the missing sibling and the note, got %+v", fs)
	}
	assertOutsideFinding(t, fs[1:], gitx.NormalizePath(sibling))
}
