package action

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// oldStash records a stash entry whose reflog timestamp lies far in the past,
// so every realistic expiry (and git's own gc defaults) considers it expired.
// It returns the stash commit id.
func (fx *maintFixture) oldStash(when time.Time, msg string) string {
	fx.t.Helper()
	fx.repo.WriteFile("stashed.txt", "base")
	fx.repo.CommitAll("track stashed.txt", when)
	fx.repo.WriteFile("stashed.txt", "uncommitted work: "+msg)
	fx.repo.GitAt(when, "stash", "push", "-q", "-m", msg)
	return fx.repo.Git("rev-parse", "refs/stash")
}

func (fx *maintFixture) stashCount() int {
	fx.t.Helper()
	return len(gitx.Lines(fx.repo.Git("stash", "list")))
}

var stashTestTime = time.Date(2001, 1, 1, 12, 0, 0, 0, time.UTC)

func TestGitReflogExpireKeepsOldStashEntries(t *testing.T) {
	fx := newMaintFixture(t)
	fx.oldStash(stashTestTime, "one")
	tip := fx.oldStash(stashTestTime.Add(time.Hour), "two")
	if fx.stashCount() != 2 {
		t.Fatalf("setup: %d stashes", fx.stashCount())
	}
	before := fx.reflogLines()
	f := fx.finding(findings.ActionGitReflogExpire, map[string]string{"expire": "2010-01-01"})
	step := mustPlan(t, fx, f)
	if !strings.Contains(step.Description, "2 stash entries") || !strings.Contains(step.Description, "kept") {
		t.Errorf("description %q must count the protected stash entries", step.Description)
	}
	if _, err := act(t, findings.ActionGitReflogExpire).Apply(context.Background(), fx.env, step); err != nil {
		t.Fatal(err)
	}
	if got := fx.stashCount(); got != 2 {
		t.Errorf("stash entries after expiry = %d, want both kept", got)
	}
	if !fx.hasObject(tip) {
		t.Error("the stash commit is gone")
	}
	if after := fx.reflogLines(); after >= before {
		t.Errorf("HEAD reflog lines %d -> %d, other reflogs must still be expired", before, after)
	}
}

func TestGitGCKeepsOldStashEntries(t *testing.T) {
	fx := newMaintFixture(t)
	fx.looseCommits(5)
	// git's built-in default already spares the stash reflog; a repository
	// (or user) that opted stashes into gc's expiry is what gc must not obey.
	fx.repo.Git("config", "gc.refs/stash.reflogExpire", "now")
	fx.repo.Git("config", "gc.refs/stash.reflogExpireUnreachable", "now")
	oldest := fx.oldStash(stashTestTime, "one")
	fx.oldStash(stashTestTime.Add(time.Hour), "two")
	f := fx.finding(findings.ActionGitGC, map[string]string{"prune": "now"})
	step := mustPlan(t, fx, f)
	if !strings.Contains(step.Description, "2 stash entries") || !strings.Contains(step.Description, "kept") {
		t.Errorf("description %q must warn about the stash entries gc would expire", step.Description)
	}
	if _, err := act(t, findings.ActionGitGC).Apply(context.Background(), fx.env, step); err != nil {
		t.Fatal(err)
	}
	if got := fx.stashCount(); got != 2 {
		t.Errorf("stash entries after gc = %d, want both kept", got)
	}
	if !fx.hasObject(oldest) {
		t.Error("the oldest stash commit was pruned")
	}
}

func TestGitReflogExpireWithoutStashesDescribesNoStash(t *testing.T) {
	fx := newMaintFixture(t)
	f := fx.finding(findings.ActionGitReflogExpire, map[string]string{"expire": "2030-01-01"})
	step := mustPlan(t, fx, f)
	if strings.Contains(step.Description, "stash") {
		t.Errorf("description %q mentions stashes although there are none", step.Description)
	}
}

// TestGitGCKeepsWorktreeRegistrationOfMissingDetachedWorktree: gc runs its own
// `git worktree prune` after gc.worktreePruneExpire (3 months by default),
// which would drop the registration of a long-missing detached worktree and
// with it the only reference to its HEAD commit.
func TestGitGCKeepsWorktreeRegistrationOfMissingDetachedWorktree(t *testing.T) {
	fx := newMaintFixture(t)
	fx.looseCommits(3)
	p := fx.add("det", "")
	fx.gitOut(p, "-c", "user.name=t", "-c", "user.email=t@e", "-c", "commit.gpgsign=false",
		"commit", "--allow-empty", "-q", "-m", "unique")
	head := fx.gitOut(p, "rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	if err := os.RemoveAll(p); err != nil {
		t.Fatal(err)
	}
	// Age the admin dir past the default expiry: git decides by the mtime of
	// the worktree's index file (gitdir is aged too for versions that look
	// at it).
	old := time.Now().AddDate(-1, 0, 0)
	for _, name := range []string{"index", "gitdir"} {
		admin := filepath.Join(fx.repo.Dir, ".git", "worktrees", "det", name)
		if err := os.Chtimes(admin, old, old); err != nil {
			t.Fatal(err)
		}
	}
	f := fx.finding(findings.ActionGitGC, map[string]string{"prune": "now"})
	step := mustPlan(t, fx, f)
	if _, err := act(t, findings.ActionGitGC).Apply(context.Background(), fx.env, step); err != nil {
		t.Fatal(err)
	}
	if !fx.registered(p) {
		t.Error("gc dropped the registration of the missing detached worktree")
	}
	if !fx.hasObject(head) {
		t.Error("the unique detached commit was pruned")
	}
}

func TestGCArgsProtectWorktreeRegistrations(t *testing.T) {
	got := strings.Join(gcArgs("now"), " ")
	for _, want := range []string{"-c gc.worktreePruneExpire=never", "gc --quiet --prune=now", "gc.refs/stash.reflogExpire=never"} {
		if !strings.Contains(got, want) {
			t.Errorf("gcArgs = %q, missing %q", got, want)
		}
	}
	if strings.Index(got, "gc.worktreePruneExpire=never") > strings.Index(got, " gc --quiet") {
		t.Errorf("gcArgs = %q: -c options must precede the subcommand", got)
	}
}
