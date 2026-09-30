package action

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestRemoveWorktreeCommitTimeBaseline: the detector falls back to the HEAD
// commit time when it has no file mtime, so the fresh newest mtime at apply
// time is always later. Such a baseline must not trigger the drift check,
// while a baseline from a real walk still does.
func TestRemoveWorktreeCommitTimeBaseline(t *testing.T) {
	tests := []struct {
		name    string
		source  string
		wantErr bool
	}{
		{"commit baseline is not compared", "commit", false},
		{"walk baseline is compared", "walk", true},
		{"legacy finding without a source is compared", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			fx.env.Force = true // the edited file is untracked, which makes the worktree dirty
			p := fx.add("wt", "feat")
			testutil.SetMTime(t, testutil.WriteFile(t, p, "notes.txt", "x"), testutil.BaseTime.AddDate(0, 0, 50))
			f := fx.removeFinding(p)
			base := testutil.BaseTime
			f.LastModified = &base
			if tt.source != "" {
				f.Meta["mtime_source"] = tt.source
			}
			_, err := fx.plan(removeWorktree{}, f)
			if tt.wantErr {
				wantSkip(t, err, "modified since the scan")
			} else if err != nil {
				t.Fatalf("Plan: %v", err)
			}
		})
	}
}

// lockingRunner fails `worktree remove` and locks the registration at the
// same moment, as if another process had locked it while the directory was
// being trashed.
type lockingRunner struct {
	gitx.Runner
	admin string
}

func (r lockingRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	for _, a := range args {
		if a == "remove" {
			if err := os.WriteFile(filepath.Join(r.admin, "locked"), []byte("taken meanwhile"), 0o600); err != nil {
				return "", err
			}
			return "", &gitx.Error{Args: args, Dir: dir, ExitCode: 128, Stderr: "fatal: simulated remove failure"}
		}
	}
	return r.Runner.Run(ctx, dir, args...)
}

// TestRemoveWorktreeRegistrationFallback: when git refuses to deregister a
// worktree whose directory is gone (old git), the targeted fallback drops
// only that administrative directory, and refuses when it is locked.
func TestRemoveWorktreeRegistrationFallback(t *testing.T) {
	setup := func(t *testing.T) (*wtFixture, string, string, Step) {
		fx := newWTFixture(t)
		a := fx.add("a", "feat-a")
		b := fx.add("b", "feat-b")
		step, err := fx.plan(removeWorktree{}, fx.removeFinding(a))
		if err != nil {
			t.Fatal(err)
		}
		fx.env.Git = failingRunner{Runner: fx.git, fail: "remove"}
		return fx, a, b, step
	}
	t.Run("targeted deregistration", func(t *testing.T) {
		fx, a, b, step := setup(t)
		en, err := removeWorktree{}.Apply(context.Background(), fx.env, step)
		if err != nil || en.Status != session.StatusApplied || en.Trash == nil {
			t.Fatalf("entry = %+v, err = %v", en, err)
		}
		fx.env.Git = fx.git
		if fx.registered(a) || !fx.registered(b) {
			t.Errorf("registered a=%v b=%v, want only b", fx.registered(a), fx.registered(b))
		}
	})
	t.Run("locked registration is kept", func(t *testing.T) {
		fx, a, _, step := setup(t)
		admin, ok := gitx.WorktreeAdminDir(filepath.Join(fx.repo.Dir, ".git"), a)
		if !ok {
			t.Fatal("no admin dir")
		}
		fx.env.Git = lockingRunner{Runner: fx.git, admin: admin}
		en, err := removeWorktree{}.Apply(context.Background(), fx.env, step)
		if err == nil || en.Status != session.StatusFailed || en.Trash == nil || !en.Restorable {
			t.Fatalf("entry = %+v, err = %v", en, err)
		}
		if _, err := os.Stat(admin); err != nil {
			t.Errorf("admin dir must survive: %v", err)
		}
	})
}

// TestCheckDetachedHeadCannotVerify: a failing for-each-ref is unknown, not
// safe, so the check refuses and nothing is touched.
func TestCheckDetachedHeadCannotVerify(t *testing.T) {
	fx := newWTFixture(t)
	p := fx.add("det", "")
	f := fx.finding(p, findings.ActionPruneWorktrees)
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	fx.env.Git = failingRunner{Runner: fx.git, fail: "for-each-ref"}
	_, err := fx.plan(pruneWorktrees{}, f)
	wantSkip(t, err, "cannot verify that the detached HEAD")
	en, err := pruneWorktrees{}.Apply(context.Background(), fx.env, Step{Finding: f})
	if err != nil || en.Status != session.StatusSkipped {
		t.Errorf("Apply = %+v, %v", en, err)
	}
	fx.env.Git = fx.git
	if !fx.registered(p) {
		t.Error("registration must be untouched")
	}
}

func TestIgnoredSummaryTotals(t *testing.T) {
	tests := []struct {
		in   []string
		want string
	}{
		{[]string{"a", "b"}, "a, b"},
		{[]string{"a", "b", "c"}, "a, b, c"},
		{[]string{"a", "b", "c", "d", "e"}, "a, b, c and 2 more (5 in total)"},
	}
	for _, tt := range tests {
		if got := ignoredSummary(tt.in); got != tt.want {
			t.Errorf("ignoredSummary(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestRemoveWorktreePlanCountsEntries: the plan states how many uncommitted
// and ignored entries move to the trash, not only the first few names.
func TestRemoveWorktreePlanCountsEntries(t *testing.T) {
	fx := newWTFixture(t)
	fx.repo.WriteFile(".gitignore", "*.cache\n")
	fx.repo.CommitAll("ignore caches", testutil.BaseTime)
	fx.env.Force = true
	p := fx.add("wt", "feat")
	for i := 0; i < 5; i++ {
		testutil.WriteFile(t, p, fmt.Sprintf("f%d.cache", i), "x")
		testutil.WriteFile(t, p, fmt.Sprintf("new%d.txt", i), "x")
	}
	step, err := fx.plan(removeWorktree{}, fx.removeFinding(p))
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, step.Description, "5 uncommitted entries", "and 2 more (5 in total)")
}
