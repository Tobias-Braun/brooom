package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// reviewFixture has an unmerged, never pushed branch feat/unmerged and a
// merged worktree on feat/dirty with an untracked file: the two kinds of work
// sweep refuses and review decides on.
func reviewFixture(t *testing.T) (f *cleanupFixture, dirtyWT string) {
	t.Helper()
	f = newCleanupFixture(t, nil)
	f.feature("feat/unmerged")
	f.feature("feat/dirty")
	dirtyWT = f.worktreeInRepo("dirty", "feat/dirty")
	f.mergeCommit("feat/dirty")
	f.publish()
	testutil.WriteFile(t, dirtyWT, "scratch.txt", "uncommitted\n")
	return f, dirtyWT
}

func review(t *testing.T, stdin string, tty bool, args ...string) (int, string, string) {
	t.Helper()
	return runApp(t, stdin, tty, time.Time{}, append([]string{"review"}, args...)...)
}

// TestReviewDecidesPerItem keeps the branch, deletes the dirty worktree and
// restores it with undo, untracked file included.
func TestReviewDecidesPerItem(t *testing.T) {
	f, dirtyWT := reviewFixture(t)
	code, out, errOut := review(t, "k\nd\n", true)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	for _, want := range []string{
		"[1/2] branch feat/unmerged", `1 commit on no remote: "work on feat/unmerged"`,
		"[2/2] worktree " + dirtyWT + " (feat/dirty)", "1 file untracked: scratch.txt", "flags: worktree_dirty",
		"[d]elete / [k]eep / [q]uit?",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if !f.hasBranch("feat/unmerged") {
		t.Error("the kept branch was deleted")
	}
	if exists(dirtyWT) {
		t.Fatalf("the dirty worktree was not deleted:\n%s", out)
	}
	if len(f.sessions()) != 1 {
		t.Fatalf("want one session, got %d", len(f.sessions()))
	}
	if code, out, errOut := runApp(t, "", false, time.Time{}, "undo", "--yes"); code != ExitOK {
		t.Fatalf("undo: code %d, stderr %q\n%s", code, errOut, out)
	}
	if data, err := os.ReadFile(filepath.Join(dirtyWT, "scratch.txt")); err != nil || string(data) != "uncommitted\n" {
		t.Errorf("undo did not bring the uncommitted file back: %q, %v", data, err)
	}
}

// TestReviewDeletesAnUnmergedBranch: the branch is removed with -D and can be
// recreated by undo.
func TestReviewDeletesAnUnmergedBranch(t *testing.T) {
	f, dirtyWT := reviewFixture(t)
	code, out, errOut := review(t, "d\nk\n", true)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if f.hasBranch("feat/unmerged") || !exists(dirtyWT) {
		t.Fatalf("branches %v, worktree kept %v\n%s", f.branches(), exists(dirtyWT), out)
	}
	if code, _, errOut := runApp(t, "", false, time.Time{}, "undo", "--yes"); code != ExitOK || !f.hasBranch("feat/unmerged") {
		t.Errorf("undo: code %d, stderr %q, branches %v", code, errOut, f.branches())
	}
}

// TestReviewQuitDiscardsEverything: q after a delete decision changes nothing.
func TestReviewQuitDiscardsEverything(t *testing.T) {
	for _, stdin := range []string{"d\nq\n", "d\n"} { // q, and end of input
		f, dirtyWT := reviewFixture(t)
		code, out, _ := review(t, stdin, true)
		if code != ExitOK || !strings.Contains(out, "quit: nothing was changed") {
			t.Fatalf("%q: code %d\n%s", stdin, code, out)
		}
		if !f.hasBranch("feat/unmerged") || !exists(dirtyWT) || len(f.sessions()) != 0 {
			t.Errorf("%q: something changed", stdin)
		}
	}
}

// TestReviewWithoutTerminalOnlyLists: without a terminal (or with --dry-run)
// review prints the items and changes nothing.
func TestReviewWithoutTerminalOnlyLists(t *testing.T) {
	for _, tc := range []struct {
		tty  bool
		args []string
	}{{false, nil}, {true, []string{"--dry-run"}}} {
		f, dirtyWT := reviewFixture(t)
		code, out, _ := review(t, "d\nd\n", tc.tty, tc.args...)
		if code != ExitOK || !strings.Contains(out, "[2/2]") || strings.Contains(out, "[d]elete") || !strings.Contains(out, "nothing was changed") {
			t.Errorf("tty %v %v: code %d\n%s", tc.tty, tc.args, code, out)
		}
		if !f.hasBranch("feat/unmerged") || !exists(dirtyWT) {
			t.Errorf("tty %v %v: something changed", tc.tty, tc.args)
		}
	}
}

// TestReviewShowsButKeepsWorkInUse: the checked-out branch can never be
// deleted, so it is listed with the reason and not asked about.
func TestReviewShowsButKeepsWorkInUse(t *testing.T) {
	f, _ := reviewFixture(t)
	f.repo.Checkout("feat/unmerged")
	code, out, errOut := review(t, "k\n", true)
	if code != ExitOK {
		t.Fatalf("code %d, stderr %q\n%s", code, errOut, out)
	}
	if !strings.Contains(out, "branch feat/unmerged") || !strings.Contains(out, "kept: ") {
		t.Errorf("the branch in use must be listed as kept:\n%s", out)
	}
	if strings.Count(out, "[d]elete") != 1 {
		t.Errorf("only the worktree may be asked about:\n%s", out)
	}
}

func TestReviewNothingToReview(t *testing.T) {
	f := newCleanupFixture(t, nil)
	f.mergedAndSquashed()
	code, out, _ := review(t, "", true)
	if code != ExitOK || !strings.Contains(out, "nothing to review") {
		t.Errorf("code %d\n%s", code, out)
	}
}
