package action

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// hookRunner runs before once, right before the first git call for which match
// is true, to simulate another process changing the repository inside the
// window between re-validation and the deleting git call.
type hookRunner struct {
	gitx.Runner
	match  func(args []string) bool
	before func()
	fired  bool
}

func (h *hookRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if !h.fired && h.match(args) {
		h.fired = true
		h.before()
	}
	return h.Runner.Run(ctx, dir, args...)
}

// isDelete matches the git calls that delete a branch ref, whichever way the
// action does it.
func isDelete(args []string) bool {
	return len(args) > 1 && (args[0] == "update-ref" && args[1] == "-d" || args[0] == "branch" && (args[1] == "-D" || args[1] == "-d"))
}

// moveBranch adds a commit to name while leaving the checkout as it was.
func (fx *branchFixture) moveBranch(name, back string) {
	fx.repo.Checkout(name)
	fx.repo.Commit("late.txt", "late", "late work", testutil.BaseTime.Add(9*time.Hour))
	fx.repo.Checkout(back)
}

// TestDeleteBranchMovedDuringApplyIsSkipped covers the window after
// re-validation: a branch that gains a commit must survive, be reported as
// skipped and never be recorded as deleted. Before the fix `git branch -D`
// deleted whatever the ref pointed to by then.
func TestDeleteBranchMovedDuringApplyIsSkipped(t *testing.T) {
	tests := []struct {
		name string
		// escalate makes git refuse -d first, so the -D fallback runs; otherwise
		// -D is chosen directly because the merge is verified against the base.
		escalate bool
	}{
		{"direct -D", false},
		{"escalated -D", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBranchFixture(t)
			fx.featureBranch("feat/x")
			fx.repo.Git("branch", "work")
			fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
			fx.repo.Push("main")
			// HEAD on work: git's own -d check (against HEAD) refuses feat/x,
			// the merge into main is still verified for -D.
			fx.repo.Checkout("work")
			f := fx.finding("feat/x", "merged-branch", "")
			step, err := fx.plan(f)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(step.Command, "-D") {
				t.Fatalf("command = %q, want -D", step.Command)
			}
			fx.env.Git = &hookRunner{Runner: fx.env.Git, match: isDelete, before: func() { fx.moveBranch("feat/x", "work") }}
			if tc.escalate {
				// Start from -d so git refuses it and the -D fallback runs.
				d, err := evaluate(context.Background(), fx.env, f)
				if err != nil {
					t.Fatal(err)
				}
				d.flag = flagSafe
				fx.env.Git.(*hookRunner).match = func(args []string) bool { return len(args) > 1 && args[0] == "update-ref" }
				_, err = d.run(context.Background(), fx.env)
				wantBranchSkip(t, err, "branch moved during apply")
				if !fx.branchExists("feat/x") {
					t.Fatal("the moved branch must survive")
				}
				return
			}

			en, err := fx.act.Apply(context.Background(), fx.env, step)
			if err != nil || en.Status != session.StatusSkipped || !strings.Contains(en.Error, "branch moved during apply") {
				t.Fatalf("entry = %+v, err = %v", en, err)
			}
			if !fx.branchExists("feat/x") {
				t.Fatal("the moved branch must survive")
			}
			if en.Undo != nil || en.Restorable {
				t.Fatalf("a skipped entry must carry no undo data: %+v", en)
			}
		})
	}
}

// TestDeleteBranchDashDRecordsDeletedSha covers the -d path, where git's own
// merge check runs against the current tip: the sha git reports as deleted is
// what undo must restore, not the one seen at re-validation.
func TestDeleteBranchDashDRecordsDeletedSha(t *testing.T) {
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	f := fx.finding("feat/x", "merged-branch", "")
	step, err := fx.plan(f)
	if err != nil {
		t.Fatal(err)
	}
	// The branch moves onto main's tip (still fully merged), so -d succeeds
	// but deletes a different commit than the one validated.
	main := fx.repo.Git("rev-parse", "main")
	fx.env.Git = &hookRunner{Runner: fx.env.Git, match: isDelete, before: func() { fx.repo.Git("branch", "-f", "feat/x", main) }}
	en, err := fx.act.Apply(context.Background(), fx.env, step)
	if err != nil || en.Status != session.StatusApplied {
		t.Fatalf("entry = %+v, err = %v", en, err)
	}
	if en.Undo["sha"] != main {
		t.Fatalf("recorded sha %s, want the deleted %s", en.Undo["sha"], main)
	}
}

func TestDeletedSHA(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Deleted branch feat/x (was abc1234).", "abc1234"},
		{"Deleted branch feat/x (was 0123456789abcdef0123456789abcdef01234567).", "0123456789abcdef0123456789abcdef01234567"},
		{"Deleted branch feat/x.", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := deletedSHA(tc.in); got != tc.want {
			t.Errorf("deletedSHA(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// branchConfig returns feat/x's branch.* configuration; git exits 1 without
// any, which is the empty string here.
func (fx *branchFixture) branchConfig() string {
	out, err := fx.env.Git.Run(context.Background(), fx.repo.Dir, "config", "--local", "--get-regexp", `^branch\.feat/x\.`)
	if err != nil {
		return ""
	}
	return out
}

// trackedFixture pushes feat/x with an upstream and merges it, so `-d` works.
func trackedFixture(t *testing.T) *branchFixture {
	t.Helper()
	fx := newBranchFixture(t)
	fx.featureBranch("feat/x")
	fx.repo.Git("push", "-q", "-u", "origin", "feat/x")
	fx.repo.Git("merge", "-q", "--no-ff", "-m", "merge", "feat/x")
	fx.repo.Push("main")
	return fx
}

// TestDeleteBranchUndoRestoresUpstream pins that a branch which tracked
// origin/feat/x comes back tracking it. Before the fix undo ran a plain
// `git branch name sha` and the tracking configuration was lost.
func TestDeleteBranchUndoRestoresUpstream(t *testing.T) {
	tests := []struct {
		name string
		// remoteGone deletes the remote branch first, so the upstream ref does
		// not exist any more when undo runs.
		remoteGone bool
	}{
		{"remote ref exists", false},
		{"remote ref gone, config written directly", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := trackedFixture(t)
			if tc.remoteGone {
				fx.repo.Git("push", "-q", "origin", "--delete", "feat/x")
				fx.repo.Git("fetch", "-q", "--prune", "origin")
			}
			_, en := fx.mustApply(fx.finding("feat/x", "merged-branch", ""))
			if fx.branchConfig() != "" {
				t.Fatal("the deletion is expected to drop the branch configuration")
			}
			if err := fx.act.Undo(context.Background(), fx.env, en); err != nil {
				t.Fatalf("Undo: %v", err)
			}
			if got := fx.repo.Git("config", "branch.feat/x.remote"); got != "origin" {
				t.Errorf("remote = %q, want origin", got)
			}
			if got := fx.repo.Git("config", "branch.feat/x.merge"); got != "refs/heads/feat/x" {
				t.Errorf("merge = %q, want refs/heads/feat/x", got)
			}
			if !tc.remoteGone {
				if got := fx.repo.Git("rev-parse", "--abbrev-ref", "feat/x@{upstream}"); got != "origin/feat/x" {
					t.Errorf("upstream = %q", got)
				}
			}
		})
	}
}

// TestDeleteBranchUndoKeepsExistingBranchConfig pins "only when undo created
// the branch": an already existing branch at the same commit is left alone.
func TestDeleteBranchUndoKeepsExistingBranchConfig(t *testing.T) {
	fx := trackedFixture(t)
	_, en := fx.mustApply(fx.finding("feat/x", "merged-branch", ""))
	fx.repo.Git("branch", "feat/x", en.Undo["sha"])
	if err := fx.act.Undo(context.Background(), fx.env, en); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if got := fx.branchConfig(); got != "" {
		t.Fatalf("an existing branch must not be reconfigured, got %q", got)
	}
}

// TestDeleteBranchUndoRejectsForgedUpstream pins the manifest hardening: the
// recorded upstream values are validated before anything is created.
func TestDeleteBranchUndoRejectsForgedUpstream(t *testing.T) {
	tests := []struct{ name, remote, merge string }{
		{"option remote", "--evil", "refs/heads/x"},
		{"remote with control character", "or\x01igin", "refs/heads/x"},
		{"remote with traversal", "..", "refs/heads/x"},
		{"remote with inner traversal", "a/../b", "refs/heads/x"},
		{"remote ending in lock", "origin.lock", "refs/heads/x"},
		{"merge outside heads", "origin", "HEAD"},
		{"merge with traversal", "origin", "refs/heads/../x"},
		{"merge with option", "origin", "-x"},
		{"only remote", "origin", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fx := newBranchFixture(t)
			en := session.Entry{Path: fx.repo.Dir, Undo: map[string]string{
				"branch": "forged", "sha": fx.repo.Head(),
				"upstream_remote": tc.remote, "upstream_merge": tc.merge,
			}}
			if err := fx.act.Undo(context.Background(), fx.env, en); err == nil {
				t.Fatal("Undo succeeded, want an error")
			}
			if fx.branchExists("forged") {
				t.Fatal("no branch may be created from a forged manifest")
			}
		})
	}
}
