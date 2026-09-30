package action

import (
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// detachedAtFeature leaves a detached worktree at the tip of a feature branch
// with one commit and deletes the branch, so no ref holds the commit. With
// landed, the commit is first rebase-merged into main under a new id, like an
// agent worktree left at a pre-rebase commit whose work is on main.
func detachedAtFeature(fx *wtFixture, landed bool) string {
	fx.t.Helper()
	fx.repo.Git("checkout", "-q", "-b", "feat")
	fx.repo.Commit("feat.txt", "feat", "work", testutil.BaseTime.Add(time.Hour))
	p := fx.add("det", "")
	fx.repo.Git("checkout", "-q", "main")
	if landed {
		fx.repo.RebaseMerge("feat", testutil.BaseTime.Add(2*time.Hour))
	}
	fx.repo.Git("branch", "-D", "feat")
	return p
}

// TestRemoveWorktreeDetachedPatchEquivalent covers issue #255: the apply-time
// re-verification accepts a detached HEAD that no ref holds when all its
// commits are patch-equivalent to the base, and still refuses genuinely unique
// commits, also with --force and when squash detection is off.
func TestRemoveWorktreeDetachedPatchEquivalent(t *testing.T) {
	tests := []struct {
		name    string
		landed  bool
		mode    config.MergeMode
		force   bool
		noCfg   bool
		applied bool
	}{
		{name: "landed under another id", landed: true, mode: config.MergeAncestorSquash, applied: true},
		{name: "unique commit", landed: false, mode: config.MergeAncestorSquash},
		{name: "unique commit even with force", landed: false, mode: config.MergeAncestorSquash, force: true},
		{name: "squash detection off", landed: true, mode: config.MergeAncestor},
		{name: "no configuration", landed: true, noCfg: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newWTFixture(t)
			fx.env.Force = tt.force
			if !tt.noCfg {
				fx.env.Config = config.Default()
				fx.env.Config.Detectors.MergedBranch.Mode = tt.mode
			}
			p := detachedAtFeature(fx, tt.landed)
			f := fx.removeFinding(p)
			if tt.applied {
				en, err := fx.apply(removeWorktree{}, f)
				if err != nil || en.Status != session.StatusApplied {
					t.Fatalf("Apply = %+v, %v; want applied", en, err)
				}
				if exists(p) || fx.registered(p) {
					t.Error("worktree still exists")
				}
				return
			}
			_, err := fx.plan(removeWorktree{}, f)
			wantSkip(t, err, "would become unreachable")
			if !exists(p) || !fx.registered(p) {
				t.Error("refused worktree was modified")
			}
		})
	}
}
