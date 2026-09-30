package worktrees_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detectors/mergedbranch"
	"github.com/Tobias-Braun/brooom/internal/detectors/worktrees"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// startedBranch creates a branch with one commit and fast-forward merges it
// into main, the shape of an agent branch that did its work and was merged:
// it sits on the base tip and was never pushed, but its reflog shows more than
// its creation, so it is not "unstarted".
func startedBranch(repo *testutil.Repo, name string) {
	repo.Git("checkout", "-q", "-b", name)
	repo.Commit(name+".txt", name, "work on "+name, testutil.BaseTime.Add(time.Hour))
	repo.Checkout("main")
	repo.Git("merge", "-q", "--ff-only", name)
}

// mergedBranchFindings runs the merged-branch detector on the harness repo.
func mergedBranchFindings(t *testing.T, h *harness) []findings.Finding {
	t.Helper()
	var out []findings.Finding
	err := mergedbranch.New().Detect(context.Background(), h.env, repoTarget(h.repo.Dir), func(f findings.Finding) { out = append(out, f) })
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestUnstartedWorktreeIsNotMerged covers issue #206: a worktree pre-created
// for a queued task sits on the base tip with a never-pushed branch, which
// merged-branch already ignores. worktrees must agree instead of planning to
// trash it once the recency window passed.
func TestUnstartedWorktreeIsNotMerged(t *testing.T) {
	tests := []struct {
		name       string
		prepare    func(repo *testutil.Repo) string
		wantMerged bool
	}{
		{"pre-created worktree", func(repo *testutil.Repo) string { return repo.AddWorktree("wt1", "wt1") }, false},
		{"branch with work merged fast-forward", func(repo *testutil.Repo) string {
			startedBranch(repo, "wt1")
			return repo.AddWorktree("wt1", "wt1")
		}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := tt.prepare(repo)
			ageTree(t, wt, testutil.BaseTime)
			h := wtHarness(t, repo, wt)
			// The fixture commits are 100 days old; only the merged rule is
			// under test, not abandoned-checkout reporting.
			h.env.Config.Detectors.Worktrees.IncludeStale = false

			wtFindings := h.detect()
			// The main checkout is on main, which is never reported, so the
			// only possible merged-branch finding is the worktree branch.
			branchFindings := mergedBranchFindings(t, h)
			if got := len(wtFindings) == 1; got != tt.wantMerged {
				t.Errorf("worktrees reported = %v, want %v: %+v", got, tt.wantMerged, wtFindings)
			}
			if got := len(branchFindings) == 1; got != tt.wantMerged {
				t.Errorf("merged-branch reported = %v, want %v: %+v", got, tt.wantMerged, branchFindings)
			}
		})
	}
}

// TestPushedWorktreeBranchStaysMerged pins the boundary of the rule: a branch
// that exists on a remote is not "unstarted", whatever its reflog says.
func TestPushedWorktreeBranchStaysMerged(t *testing.T) {
	repo := testutil.NewRepoWithRemote(t)
	wt := repo.AddWorktree("wt1", "wt1")
	repo.Push("wt1")
	ageTree(t, wt, testutil.BaseTime)
	h := wtHarness(t, repo, wt)
	// Pushed branches are not "unstarted": the merged report stays.
	f := one(t, h.detect())
	if !slices.Contains(codes(f), "merged_into") {
		t.Errorf("evidence %v", codes(f))
	}
}

// TestRecentWorktreeKeepsAction covers issue #255: a merged worktree touched
// seconds ago keeps confidence high and its removal action, with and without
// --force; recently_modified is informational only.
func TestRecentWorktreeKeepsAction(t *testing.T) {
	for _, force := range []bool{false, true} {
		repo := testutil.NewRepo(t)
		wt := repo.AddStartedWorktree("wt", "feat")
		h := activeHarness(t, repo, wt)
		h.env.Force = force
		f := one(t, h.detect())
		if !f.HasRisk(findings.RiskRecentlyModified) {
			t.Fatalf("flags %v", f.RiskFlags)
		}
		if f.SuggestedAction.Type != findings.ActionRemoveWorktree || f.Confidence != findings.ConfidenceHigh {
			t.Errorf("force=%v: action %q confidence %q (reason %q)", force, f.SuggestedAction.Type, f.Confidence, f.SuggestedAction.Reason)
		}
	}
}

// deleteBranchRef removes the ref of a checked-out branch behind git's back,
// which `git branch -D` refuses to do.
func deleteBranchRef(repo *testutil.Repo, branch string) {
	repo.Git("update-ref", "-d", "refs/heads/"+branch)
}

// TestMissingBranchRef covers issue #205: a worktree whose branch ref is gone
// (porcelain reports an all-zero HEAD) used to vanish from the report.
func TestMissingBranchRef(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	deleteBranchRef(repo, "feat")
	h := wtHarness(t, repo, wt)

	f := one(t, h.detect())
	if f.Confidence != findings.ConfidenceLow || f.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
	}
	if !slices.Contains(codes(f), "branch_ref_missing") {
		t.Errorf("evidence %v", codes(f))
	}
	for _, hint := range []string{"git branch feat", "git worktree repair"} {
		if !strings.Contains(f.SuggestedAction.Reason, hint) {
			t.Errorf("reason lacks %q: %q", hint, f.SuggestedAction.Reason)
		}
	}
}

// TestUnbornOrphanWorktreeIsQuiet: a fresh `git worktree add --orphan` has no
// commit yet by design and is not clutter.
func TestUnbornOrphanWorktreeIsQuiet(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := filepath.Join(testutil.ResolvedTempDir(t), "orphan")
	cmd := exec.Command("git", "worktree", "add", "-q", "--orphan", "-b", "orph", wt)
	cmd.Dir, cmd.Env = repo.Dir, repo.Env(testutil.BaseTime)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("git worktree add --orphan unsupported: %v: %s", err, out)
	}
	h := wtHarness(t, repo, wt)
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("unborn orphan worktree reported: %+v", fs)
	}
}

// TestMissingBranchRefDoesNotFailScan makes sure the new rule never turns the
// oddity into a scan error.
func TestMissingBranchRefDoesNotFailScan(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("wt", "feat")
	deleteBranchRef(repo, "feat")
	h := wtHarness(t, repo, wt)
	worktrees.SetOpenFiles(t, func(context.Context, []string) (map[string]bool, error) { return nil, nil })
	if err := worktrees.New().Detect(context.Background(), h.env, repoTarget(repo.Dir), func(findings.Finding) {}); err != nil {
		t.Fatal(err)
	}
}
