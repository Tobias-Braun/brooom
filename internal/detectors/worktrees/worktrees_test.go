package worktrees_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/detectors/worktrees"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// now is the fixed scan time: far enough after testutil.BaseTime that commits
// made at BaseTime are stale, while files with real (later) mtimes are "in
// the future" and therefore age 0.
var now = testutil.BaseTime.AddDate(0, 0, 100)

// harness bundles an env, the repository and the guard directories.
type harness struct {
	t    *testing.T
	repo *testutil.Repo
	env  *detect.Env
}

// newHarness creates a repository with a guard allowing the repository and
// the extra directories.
func newHarness(t *testing.T, repo *testutil.Repo, guarded ...string) *harness {
	t.Helper()
	// Never touch the real home: the config layer and git may consult it.
	home := testutil.ResolvedTempDir(t)
	t.Setenv("BROOOM_HOME", home)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	g, err := scope.NewGuard(append([]string{repo.Dir}, guarded...)...)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	// Real mtimes of freshly built fixtures lie after the fixed scan time and
	// would count as recent; recency has its own tests (active_test.go).
	cfg.Thresholds.RecentDays = 0
	env := &detect.Env{Config: cfg, Git: runner, Repos: gitx.NewCache(runner), Guard: g, Now: now}
	return &harness{t: t, repo: repo, env: env}
}

// wtHarness is newHarness with the directory holding wts allowed.
func wtHarness(t *testing.T, repo *testutil.Repo, wts ...string) *harness {
	t.Helper()
	var dirs []string
	for _, w := range wts {
		dirs = append(dirs, filepath.Dir(w))
	}
	return newHarness(t, repo, dirs...)
}

func repoTarget(dir string) scope.Target {
	return scope.Target{Kind: scope.TargetRepo, Path: dir, Scope: findings.Scope{Type: findings.ScopeRepo, Path: dir}}
}

func (h *harness) run(target scope.Target) []findings.Finding {
	h.t.Helper()
	var out []findings.Finding
	err := worktrees.New().Detect(context.Background(), h.env, target, func(f findings.Finding) { out = append(out, f) })
	if err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *harness) detect() []findings.Finding { return h.run(repoTarget(h.repo.Dir)) }

// one returns the only finding or fails.
func one(t *testing.T, fs []findings.Finding) findings.Finding {
	t.Helper()
	if len(fs) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(fs), fs)
	}
	return fs[0]
}

func codes(f findings.Finding) []string {
	var out []string
	for _, e := range f.Evidence {
		out = append(out, e.Code)
	}
	return out
}

func evidence(t *testing.T, f findings.Finding, code string) findings.Evidence {
	t.Helper()
	for _, e := range f.Evidence {
		if e.Code == code {
			return e
		}
	}
	t.Fatalf("evidence %q missing, have %v", code, codes(f))
	return findings.Evidence{}
}

// gitIn runs git inside a linked worktree with the test identity.
func gitIn(t *testing.T, repo *testutil.Repo, dir string, args ...string) {
	t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = repo.Env(testutil.BaseTime)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// commitIn adds a file and commits it in the worktree at BaseTime.
func commitIn(t *testing.T, repo *testutil.Repo, wt, file string) {
	t.Helper()
	testutil.WriteFile(t, wt, file, "content of "+file+"\n")
	gitIn(t, repo, wt, "add", "-A")
	gitIn(t, repo, wt, "commit", "-q", "-m", "work on "+file)
}

// ageTree sets the mtime of every entry below dir to when.
func ageTree(t *testing.T, dir string, when time.Time) {
	t.Helper()
	err := filepath.WalkDir(dir, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegistered(t *testing.T) {
	d, ok := detect.Get("worktrees")
	if !ok {
		t.Fatal("worktrees detector is not registered")
	}
	if d.Category() != detect.CategoryGit || d.Description() == "" {
		t.Errorf("category %q, description %q", d.Category(), d.Description())
	}
}

func TestMergedBranchWorktree(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("merged", "feat-merged")
	// Files created now are "in the future" of the fixed scan time and would
	// carry recently_modified; this test is about the untouched shape.
	ageTree(t, wt, testutil.BaseTime)
	h := wtHarness(t, repo, wt)

	f := one(t, h.detect())
	if f.Detector != "worktrees" || f.Kind != findings.KindWorktree || f.Ref != "feat-merged" || f.Path != wt {
		t.Errorf("shape: %+v", f)
	}
	if f.ID != findings.NewID("worktrees", findings.KindWorktree, wt, "feat-merged") {
		t.Errorf("id %q", f.ID)
	}
	if f.Scope.Path != repo.Dir || f.Scope.Type != findings.ScopeRepo {
		t.Errorf("scope %+v", f.Scope)
	}
	if f.Confidence != findings.ConfidenceHigh || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
		t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
	}
	if ev := evidence(t, f, "merged_into"); ev.Value != "main" {
		t.Errorf("merged_into value %v", ev.Value)
	}
	wantMeta := map[string]string{"repo": repo.Dir, "head": repo.Head(), "branch": "feat-merged"}
	for k, v := range wantMeta {
		if f.Meta[k] != v {
			t.Errorf("meta[%s] = %q, want %q", k, f.Meta[k], v)
		}
	}
	if len(f.RiskFlags) != 0 {
		t.Errorf("flags %v", f.RiskFlags)
	}
}

func TestSizeAndTimes(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("sized", "feat-sized")
	h := wtHarness(t, repo, wt)
	f := one(t, h.detect())

	sum, err := walk.DirSize(context.Background(), wt, walk.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if f.SizeBytes <= 0 || f.SizeBytes != sum.SizeBytes {
		t.Errorf("size %d, walk says %d", f.SizeBytes, sum.SizeBytes)
	}
	if f.LastModified == nil || !f.LastModified.Equal(sum.NewestModTime) {
		t.Errorf("last modified %v, want %v", f.LastModified, sum.NewestModTime)
	}
}

func TestSquashMergedWorktree(t *testing.T) {
	repo := testutil.NewRepoWithRemote(t)
	wt := repo.AddWorktree("squash", "feat-squash")
	commitIn(t, repo, wt, "squash.txt")
	repo.SquashMerge("feat-squash", "squash feat", testutil.BaseTime.Add(time.Hour))
	repo.Push("main")
	h := wtHarness(t, repo, wt)

	f := one(t, h.detect())
	if f.Confidence != findings.ConfidenceHigh || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
		t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
	}
	if ev := evidence(t, f, "squash_merged_into"); ev.Value != "origin/main" {
		t.Errorf("squash_merged_into value %v", ev.Value)
	}

	// With ancestor-only detection the squash merge is invisible.
	h.env.Config.Detectors.MergedBranch.Mode = config.MergeAncestor
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("ancestor mode reported %+v", fs)
	}
}

func TestBaseBranchWorktreeIsNotMerged(t *testing.T) {
	// A linked worktree on the base branch is "an ancestor of the base" by
	// definition; it must not be reported as a merged feature branch.
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("switch", "-q", "-c", "elsewhere")
	wt := repo.AddWorktree("on-main", "main")
	h := wtHarness(t, repo, wt)
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("reported %+v", fs)
	}
}

func TestMissingDirectory(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("gone", "feat-gone")
	h := wtHarness(t, repo, wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}

	f := one(t, h.detect())
	if f.Kind != findings.KindWorktreeMissing || f.Ref != "feat-gone" || f.Path != wt {
		t.Errorf("shape: %+v", f)
	}
	if f.ID != findings.NewID("worktrees", findings.KindWorktreeMissing, wt, "feat-gone") {
		t.Errorf("id %q", f.ID)
	}
	a := f.SuggestedAction
	if a.Type != findings.ActionPruneWorktrees || a.Command != "git worktree remove --force "+wt || a.Reason == "" {
		t.Errorf("action %+v", a)
	}
	if f.Confidence != findings.ConfidenceHigh || f.SizeBytes != 0 {
		t.Errorf("confidence %q size %d", f.Confidence, f.SizeBytes)
	}
	if ev := evidence(t, f, "worktree_missing"); ev.Value != wt {
		t.Errorf("worktree_missing value %v", ev.Value)
	}
	if f.Meta["repo"] != repo.Dir || f.Meta["head"] != repo.Head() {
		t.Errorf("meta %v", f.Meta)
	}
	// The missing entry has no directory, so the age falls back to the HEAD
	// commit time.
	if f.LastModified == nil || !f.LastModified.Equal(testutil.BaseTime) {
		t.Errorf("last modified %v", f.LastModified)
	}
}

// TestMissingDetachedWorktree covers issue #91: a missing directory says
// nothing about the commits of a detached HEAD, which lived in the admin dir
// that a prune deletes, so the prune is only offered when the HEAD is safe.
func TestMissingDetachedWorktree(t *testing.T) {
	t.Run("unique commit is never pruned", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		wt := repo.AddWorktree("det-gone", "")
		commitIn(t, repo, wt, "unique.txt")
		h := wtHarness(t, repo, wt)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		f := one(t, h.detect())
		a := f.SuggestedAction
		if f.Kind != findings.KindWorktreeMissing || a.Type != findings.ActionNone || a.Command != "" {
			t.Fatalf("kind %q action %+v", f.Kind, a)
		}
		if !strings.Contains(a.Reason, "git worktree repair") {
			t.Errorf("reason %q lacks the repair hint", a.Reason)
		}
		if !f.HasRisk(findings.RiskUnpushedCommits) {
			t.Errorf("flags %v", f.RiskFlags)
		}
		if ev := evidence(t, f, "head_not_pushed"); ev.Value != f.Meta["head"] {
			t.Errorf("head_not_pushed value %v", ev.Value)
		}
		evidence(t, f, "worktree_missing")
	})
	t.Run("head contained in the base is pruned", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		wt := repo.AddWorktree("det-gone", "")
		h := wtHarness(t, repo, wt)
		if err := os.RemoveAll(wt); err != nil {
			t.Fatal(err)
		}
		f := one(t, h.detect())
		if f.SuggestedAction.Type != findings.ActionPruneWorktrees || f.HasRisk(findings.RiskUnpushedCommits) {
			t.Errorf("action %q flags %v", f.SuggestedAction.Type, f.RiskFlags)
		}
	})
}

func TestMissingDirectoryOutsideGuard(t *testing.T) {
	tests := []struct {
		name        string
		mainInGuard bool
		want        int
	}{
		{"main inside the guard: emitted, prune only edits metadata", true, 1},
		{"main outside the guard: skipped", false, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := repo.AddWorktree("gone", "feat-gone")
			if err := os.RemoveAll(wt); err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, repo)
			if !tt.mainInGuard {
				g, err := scope.NewGuard(testutil.ResolvedTempDir(t))
				if err != nil {
					t.Fatal(err)
				}
				h.env.Guard = g
			}
			fs := h.detect()
			if len(fs) != tt.want {
				t.Fatalf("got %d findings, want %d: %+v", len(fs), tt.want, fs)
			}
			if tt.want == 1 && fs[0].SuggestedAction.Type != findings.ActionPruneWorktrees {
				t.Errorf("action %q", fs[0].SuggestedAction.Type)
			}
		})
	}
}

func TestMissingAndLocked(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("gone-locked", "feat-gl")
	repo.Git("worktree", "lock", "--reason", "external drive", wt)
	h := wtHarness(t, repo, wt)
	if err := os.RemoveAll(wt); err != nil {
		t.Fatal(err)
	}
	f := one(t, h.detect())
	if f.Kind != findings.KindWorktreeMissing || f.SuggestedAction.Type != findings.ActionNone {
		t.Errorf("kind %q action %q", f.Kind, f.SuggestedAction.Type)
	}
	if !f.HasRisk(findings.RiskWorktreeLocked) {
		t.Errorf("flags %v", f.RiskFlags)
	}
}

func TestLockedWorktrees(t *testing.T) {
	tests := []struct {
		name  string
		force bool
	}{
		{"locked and merged", false},
		{"locked and merged with --force is still never suggested", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := repo.AddWorktree("locked", "feat-locked")
			repo.Git("worktree", "lock", "--reason", "on usb stick", wt)
			h := wtHarness(t, repo, wt)
			h.env.Force = tt.force

			f := one(t, h.detect())
			if f.SuggestedAction.Type != findings.ActionNone || f.SuggestedAction.Reason == "" {
				t.Errorf("action %+v", f.SuggestedAction)
			}
			if !f.HasRisk(findings.RiskWorktreeLocked) || !f.Blocked() {
				t.Errorf("flags %v", f.RiskFlags)
			}
			if findings.Actionable(f.RiskFlags, true) {
				t.Error("a locked worktree must not be actionable even with force")
			}
			if ev := evidence(t, f, "worktree_locked"); ev.Value != "on usb stick" {
				t.Errorf("worktree_locked value %v", ev.Value)
			}
			evidence(t, f, "merged_into")
			if f.Meta["locked_reason"] != "on usb stick" {
				t.Errorf("meta %v", f.Meta)
			}
		})
	}
}

func TestLockedNonCandidateIsNotReported(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("locked-wip", "feat-wip")
	commitIn(t, repo, wt, "wip.txt")
	repo.Git("worktree", "lock", wt)
	h := wtHarness(t, repo, wt)
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("reported %+v", fs)
	}
}

func TestDirtyWorktrees(t *testing.T) {
	tests := []struct {
		name       string
		force      bool
		lock       bool
		wantAction findings.ActionType
		wantReason string
	}{
		{"dirty and merged is blocked", false, false, findings.ActionNone,
			"uncommitted changes; commit, stash or re-run with --force to trash the directory"},
		{"dirty and merged with --force is suggested for the trash", true, false, findings.ActionRemoveWorktree,
			"forced: uncommitted changes will be moved to the trash, not deleted"},
		{"dirty and locked with --force stays blocked", true, true, findings.ActionNone, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := repo.AddWorktree("dirty", "feat-dirty")
			testutil.WriteFile(t, wt, "scratch.txt", "not committed\n")
			if tt.lock {
				repo.Git("worktree", "lock", wt)
			}
			h := wtHarness(t, repo, wt)
			h.env.Force = tt.force

			f := one(t, h.detect())
			if f.SuggestedAction.Type != tt.wantAction {
				t.Errorf("action %q, want %q", f.SuggestedAction.Type, tt.wantAction)
			}
			if tt.wantReason != "" && f.SuggestedAction.Reason != tt.wantReason {
				t.Errorf("reason %q", f.SuggestedAction.Reason)
			}
			if !f.HasRisk(findings.RiskWorktreeDirty) {
				t.Errorf("flags %v", f.RiskFlags)
			}
			evidence(t, f, "worktree_dirty")
			if got := findings.Actionable(f.RiskFlags, tt.force); got != (tt.wantAction != findings.ActionNone) {
				t.Errorf("Actionable(force=%v) = %v", tt.force, got)
			}
		})
	}
}

func TestUpstreamGone(t *testing.T) {
	tests := []struct {
		name       string
		localExtra bool
		want       bool
	}{
		{"all commits are contained in a remote branch", false, true},
		{"a unique local commit keeps it out", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepoWithRemote(t)
			wt := repo.AddWorktree("gone-up", "feat-up")
			commitIn(t, repo, wt, "up.txt")
			gitIn(t, repo, wt, "push", "-q", "-u", "origin", "feat-up")
			// A second remote branch keeps the commits reachable on the
			// remote after feat-up is deleted there, without merging them.
			gitIn(t, repo, wt, "push", "-q", "origin", "feat-up:other")
			repo.DeleteRemoteBranch("feat-up")
			repo.Fetch()
			if tt.localExtra {
				commitIn(t, repo, wt, "local-only.txt")
			}
			h := wtHarness(t, repo, wt)

			fs := h.detect()
			if !tt.want {
				if len(fs) != 0 {
					t.Fatalf("reported %+v", fs)
				}
				return
			}
			f := one(t, fs)
			if f.Confidence != findings.ConfidenceMedium || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
				t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
			}
			if ev := evidence(t, f, "upstream_gone"); ev.Value != "origin/feat-up" {
				t.Errorf("upstream_gone value %v", ev.Value)
			}
		})
	}
}

func TestStale(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, wt string, h *harness)
		want  bool
	}{
		{"old commit and old files", func(t *testing.T, wt string, h *harness) {}, true},
		{"fresh file keeps it alive", func(t *testing.T, wt string, h *harness) {
			p := filepath.Join(wt, "recent.txt")
			testutil.WriteFile(t, wt, "recent.txt", "x")
			testutil.SetMTime(t, p, now.AddDate(0, 0, -1))
		}, false},
		{"include_stale=false disables the rule", func(t *testing.T, wt string, h *harness) {
			h.env.Config.Detectors.Worktrees.IncludeStale = false
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			wt := repo.AddWorktree("stale", "feat-stale")
			commitIn(t, repo, wt, "old.txt")
			ageTree(t, wt, testutil.BaseTime)
			h := wtHarness(t, repo, wt)
			tt.setup(t, wt, h)

			fs := h.detect()
			if !tt.want {
				if len(fs) != 0 {
					t.Fatalf("reported %+v", fs)
				}
				return
			}
			// The branch has an unpushed commit; removing a worktree never
			// deletes the branch, so it is still removable.
			f := one(t, fs)
			if f.Confidence != findings.ConfidenceMedium || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
				t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
			}
			ev := evidence(t, f, "worktree_stale")
			if !strings.Contains(ev.Message, "100 days") {
				t.Errorf("message %q", ev.Message)
			}
			if f.AgeDays != 100 {
				t.Errorf("age %d", f.AgeDays)
			}
		})
	}
}

func TestDetachedHead(t *testing.T) {
	t.Run("contained in the base is medium", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		wt := repo.AddWorktree("det", "")
		h := wtHarness(t, repo, wt)
		f := one(t, h.detect())
		if f.Ref != "" || f.Meta["branch"] != "" {
			t.Errorf("ref %q meta %v", f.Ref, f.Meta)
		}
		if f.Confidence != findings.ConfidenceMedium || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
			t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
		}
		if ev := evidence(t, f, "head_contained_in"); ev.Value != "main" {
			t.Errorf("value %v", ev.Value)
		}
	})
	t.Run("contained in a remote branch is medium", func(t *testing.T) {
		repo := testutil.NewRepoWithRemote(t)
		repo.Git("switch", "-q", "-c", "feat-remote")
		repo.Commit("r.txt", "r", "remote work", testutil.BaseTime.Add(time.Hour))
		repo.Push("feat-remote")
		repo.Git("switch", "-q", "main")
		wt := repo.AddWorktree("det-remote", "")
		gitIn(t, repo, wt, "checkout", "-q", "--detach", "feat-remote")
		h := wtHarness(t, repo, wt)
		f := one(t, h.detect())
		if ev := evidence(t, f, "head_contained_in"); ev.Value != "origin/feat-remote" {
			t.Errorf("value %v", ev.Value)
		}
	})
	t.Run("unique commit is not reported while fresh", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		wt := repo.AddWorktree("det-unique", "")
		commitIn(t, repo, wt, "unique.txt")
		h := wtHarness(t, repo, wt)
		if fs := h.detect(); len(fs) != 0 {
			t.Errorf("reported %+v", fs)
		}
	})
	t.Run("unique commit that is stale is reported but never suggested", func(t *testing.T) {
		repo := testutil.NewRepo(t)
		wt := repo.AddWorktree("det-unique-stale", "")
		commitIn(t, repo, wt, "unique.txt")
		ageTree(t, wt, testutil.BaseTime)
		h := wtHarness(t, repo, wt)
		h.env.Force = true
		f := one(t, h.detect())
		if f.SuggestedAction.Type != findings.ActionNone || !f.HasRisk(findings.RiskUnpushedCommits) {
			t.Errorf("action %q flags %v", f.SuggestedAction.Type, f.RiskFlags)
		}
		if findings.Actionable(f.RiskFlags, false) {
			t.Error("unpushed_commits must block without --force")
		}
		evidence(t, f, "worktree_stale")
	})
}

func TestMainWorktreeIsNeverReported(t *testing.T) {
	repo := testutil.NewRepo(t)
	// No linked worktrees at all: the main worktree alone yields nothing,
	// even though its branch is trivially "merged" and it is old.
	h := newHarness(t, repo)
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("reported %+v", fs)
	}
}

func TestScopeWorktreeIsSkipped(t *testing.T) {
	repo := testutil.NewRepo(t)
	a := repo.AddWorktree("a", "feat-a")
	b := repo.AddWorktree("b", "feat-b")
	h := wtHarness(t, repo, a, b)

	// Default mode: the user stands in a, so a is the scope and never listed.
	fs := h.run(scope.Target{Kind: scope.TargetRepo, Path: a, Scope: findings.Scope{Type: findings.ScopeRepo, Path: a}})
	if got := one(t, fs); got.Path != b {
		t.Errorf("reported %s, want %s", got.Path, b)
	}

	// Workspaces mode: the scope is a root, so a discovered linked worktree
	// is reported normally.
	root := filepath.Dir(a)
	fs = h.run(scope.Target{Kind: scope.TargetRepo, Path: a, Scope: findings.Scope{Type: findings.ScopeRoot, Path: root}})
	paths := []string{}
	for _, f := range fs {
		paths = append(paths, f.Path)
	}
	slices.Sort(paths)
	if want := []string{a, b}; !slices.Equal(paths, want) {
		t.Errorf("paths %v, want %v", paths, want)
	}
}

func TestIdenticalIDsFromMainAndLinkedTargets(t *testing.T) {
	repo := testutil.NewRepo(t)
	a := repo.AddWorktree("a", "feat-a")
	b := repo.AddWorktree("b", "feat-b")
	h := wtHarness(t, repo, a, b)
	root := filepath.Dir(a)

	ids := func(fs []findings.Finding) []string {
		var out []string
		for _, f := range fs {
			out = append(out, f.ID)
		}
		slices.Sort(out)
		return out
	}
	fromMain := ids(h.run(scope.Target{Kind: scope.TargetRepo, Path: repo.Dir, Scope: findings.Scope{Type: findings.ScopeRoot, Path: root}}))
	fromLinked := ids(h.run(scope.Target{Kind: scope.TargetRepo, Path: a, Scope: findings.Scope{Type: findings.ScopeRoot, Path: root}}))
	if len(fromMain) != 2 || !slices.Equal(fromMain, fromLinked) {
		t.Errorf("main %v, linked %v", fromMain, fromLinked)
	}
}

func TestOutsideGuardIsSkipped(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.AddWorktree("outside", "feat-outside")
	// The guard only allows the repository, like default single-repo mode;
	// the sibling worktree lives in another temp directory.
	h := newHarness(t, repo)
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("reported %+v", fs)
	}
}

func TestAgentLocationEvidence(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := filepath.Join(repo.Dir, ".claude", "worktrees", "agent-1")
	repo.Git("worktree", "add", "-q", "-b", "agent-1", wt)
	h := newHarness(t, repo)
	wt, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}

	f := one(t, h.detect())
	if f.Path != wt {
		t.Errorf("path %s, want %s", f.Path, wt)
	}
	if ev := evidence(t, f, "agent_worktree_location"); ev.Value != ".claude/worktrees" {
		t.Errorf("value %v", ev.Value)
	}
	// Context only: confidence and action are the ones of the merge rule.
	if f.Confidence != findings.ConfidenceHigh || f.SuggestedAction.Type != findings.ActionRemoveWorktree {
		t.Errorf("confidence %q action %q", f.Confidence, f.SuggestedAction.Type)
	}
}

func TestNoLocationEvidenceForPlainWorktree(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("plain", "feat-plain")
	h := wtHarness(t, repo, wt)
	f := one(t, h.detect())
	if slices.Contains(codes(f), "agent_worktree_location") {
		t.Errorf("unexpected location evidence: %v", codes(f))
	}
}

func TestSkippedTargetsAndSettings(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("x", "feat-x")
	h := wtHarness(t, repo, wt)

	project := repoTarget(repo.Dir)
	project.Kind = scope.TargetProject
	if fs := h.run(project); len(fs) != 0 {
		t.Errorf("project target reported %+v", fs)
	}

	h.env.Config.Detectors.Worktrees.Enabled = false
	if fs := h.detect(); len(fs) != 0 {
		t.Errorf("disabled detector reported %+v", fs)
	}
}

func TestCancelledContext(t *testing.T) {
	repo := testutil.NewRepo(t)
	wt := repo.AddWorktree("c", "feat-c")
	h := wtHarness(t, repo, wt)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := worktrees.New().Detect(ctx, h.env, repoTarget(repo.Dir), func(findings.Finding) {})
	if err == nil {
		t.Error("cancelled context must abort the scan")
	}
}
