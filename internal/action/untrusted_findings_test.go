package action

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// These tests cover findings whose safety-relevant fields were edited: a
// findings file is untrusted input, so the actions must derive every safety
// fact from the live state and never from Meta, Args or Detector.

// repoTrashFixture is a trash fixture whose guard allows a real repository
// with an ignored build directory and an untracked file.
func repoTrashFixture(t *testing.T) (*trashFixture, *testutil.Repo) {
	t.Helper()
	repo := testutil.NewRepo(t)
	testutil.WriteFile(t, repo.Dir, ".gitignore", "build/\n")
	repo.Git("add", ".gitignore")
	repo.Commit("src/a.go", "package a", "add source", testutil.BaseTime)
	testutil.WriteFile(t, repo.Dir, "build/out.bin", "ignored")
	testutil.WriteFile(t, repo.Dir, "thesis-draft.bin", "the only copy")
	testutil.WriteFile(t, repo.Dir, "drafts/notes.txt", "untracked dir")
	guard, err := scope.NewGuard(filepath.Dir(repo.Dir))
	if err != nil {
		t.Fatal(err)
	}
	fx := newTrashFixture(t)
	fx.env.Guard = guard
	fx.strategy = config.StrategyDelete
	return fx, repo
}

func TestTrashDeleteRefusesUntrackedWithoutMeta(t *testing.T) {
	fx, repo := repoTrashFixture(t)
	tests := []struct {
		name    string
		rel     string
		force   bool
		refused bool
	}{
		{"untracked file, meta stripped", "thesis-draft.bin", false, true},
		{"untracked file, meta stripped, force", "thesis-draft.bin", true, true},
		{"directory of untracked files", "drafts", false, true},
		{"ignored directory may be deleted", "build", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx.env.Force = tt.force
			f := trashFinding(filepath.Join(repo.Dir, filepath.FromSlash(tt.rel)))
			f.Meta = nil
			step, err := trashAction{}.Plan(context.Background(), fx.env, f)
			if tt.refused {
				wantSkip(t, err, "refusing to permanently delete untracked files")
				return
			}
			if err != nil || !strings.Contains(step.Description, "permanently delete") {
				t.Fatalf("step = %+v, err = %v", step, err)
			}
		})
	}
}

func TestTrashDeleteFailsClosedWithoutGit(t *testing.T) {
	fx, repo := repoTrashFixture(t)
	fx.env.Git = nil
	_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
	wantSkip(t, err, "cannot be ruled out")
	fx.env.Git = failingGit{}
	_, err = trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
	wantSkip(t, err, "cannot be ruled out")
}

// TestTrashApplyRechecksUntracked covers a file that appeared between Plan
// and Apply: Apply must not trust the earlier plan.
func TestTrashApplyRechecksUntracked(t *testing.T) {
	fx, repo := repoTrashFixture(t)
	step, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(repo.Dir, "build")))
	if err != nil {
		t.Fatal(err)
	}
	late := testutil.WriteFile(t, repo.Dir, "build/precious.txt", "created after the plan")
	// Not ignored any more once the ignore rule is gone.
	testutil.WriteFile(t, repo.Dir, ".gitignore", "")
	en, err := trashAction{}.Apply(context.Background(), fx.env, step)
	if err == nil || en.Status == "applied" || !strings.Contains(en.Error, "untracked") {
		t.Fatalf("entry = %+v, err = %v", en, err)
	}
	if _, err := os.Lstat(late); err != nil {
		t.Fatalf("file was removed: %v", err)
	}
}

func TestTrashRefusesCatalogProtectedPaths(t *testing.T) {
	tests := []struct {
		name string
		rel  string
		body map[string]string
	}{
		{"env file", ".env", nil},
		{"nested env file", "app/.env", nil},
		{"mcp config", ".mcp.json", nil},
		{"local memory", "CLAUDE.local.md", nil},
		{"local settings", ".claude/settings.local.json", nil},
		{"file below a protected directory", ".claude/commands/deploy.md", nil},
		{"directory containing a protected file", ".claude", map[string]string{".claude/settings.local.json": "{}", ".claude/cache/x.log": "x"}},
		{"directory containing a protected directory", ".cursor", map[string]string{".cursor/rules/a.mdc": "r"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newTrashFixture(t)
			fx.env.Force = true // protection is never overridable
			p := fx.path("proj/" + tt.rel)
			if tt.body == nil {
				fx.write("proj/"+tt.rel, "secret")
			}
			for rel, body := range tt.body {
				fx.write("proj/"+rel, body)
			}
			_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(p))
			wantSkip(t, err, "protect")
		})
	}
}

// TestTrashProtectionSparesOrdinaryTargets guards against over-blocking:
// dependency directories carry .npmrc and .gitignore files at any depth, which
// must not make every build directory unremovable.
func TestTrashProtectionSparesOrdinaryTargets(t *testing.T) {
	fx := newTrashFixture(t)
	fx.write("proj/node_modules/pkg/.npmrc", "x")
	fx.write("proj/node_modules/pkg/.gitignore", "x")
	fx.write("proj/node_modules/pkg/.github/prompts/p.md", "x")
	fx.write("proj/.claude/cache/tmp.log", "x")
	for _, rel := range []string{"proj/node_modules", "proj/.claude/cache"} {
		if _, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(fx.path(rel))); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestTrashRefusesUserLevelProtectedPaths(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	fx.write("users/me/.claude/settings.json", "{}")
	fx.write("users/me/.claude/projects/p1/memory/notes.md", "memory")
	fx.write("users/me/.claude/projects/p1/session.jsonl", "log")
	fx.write("users/me/.claude/projects/p2/session.jsonl", "log")
	tests := []struct {
		name, rel string
		refused   bool
	}{
		{"user settings", "users/me/.claude/settings.json", true},
		{"directory containing memory", "users/me/.claude/projects/p1", true},
		{"unprotected session directory", "users/me/.claude/projects/p2", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(fx.path(tt.rel)))
			if tt.refused {
				wantSkip(t, err, "protect")
			} else if err != nil {
				t.Fatalf("Plan: %v", err)
			}
		})
	}
}

func TestMaintenanceDateIgnoresNothingButFallsBackToConfig(t *testing.T) {
	env := &Env{Config: config.Default()}
	f := findings.Finding{Path: t.TempDir()}
	if got, want := (reflogBase.date(env, f)), env.Config.Detectors.GitBloat.ReflogExpire; got != want {
		t.Errorf("reflog fallback = %q, want configured %q", got, want)
	}
	if got, want := (pruneBase.date(env, f)), env.Config.Detectors.GitBloat.PruneExpire; got != want {
		t.Errorf("prune fallback = %q, want configured %q", got, want)
	}
	// An explicit arg (built by `brooom git purge`) still wins.
	f.SuggestedAction.Args = map[string]string{"expire": "30.days.ago"}
	if got := reflogBase.date(env, f); got != "30.days.ago" {
		t.Errorf("explicit date = %q", got)
	}
}

// TestDeleteBranchDerivesVerificationLive proves that the detector name and
// the "verified" claim of a finding are irrelevant: what is merged or
// contained in a remote is decided by the repository right now.
func TestDeleteBranchDerivesVerificationLive(t *testing.T) {
	t.Run("squash merge without any claim", func(t *testing.T) {
		fx := newBranchFixture(t)
		fx.featureBranch("feat/sq")
		// The branch commits sit on a remote: the heuristic alone would not do.
		fx.repo.Git("push", "-q", "origin", "feat/sq")
		fx.repo.SquashMerge("feat/sq", "squash", testutil.BaseTime.Add(2*time.Hour))
		fx.repo.Push("main")
		step, _ := fx.mustApply(fx.finding("feat/sq", "stale-branch", ""))
		if step.Command != "git branch -D -- feat/sq" || !strings.Contains(step.Description, "re-verified") {
			t.Fatalf("step = %+v", step)
		}
	})
	t.Run("remote containment without any claim", func(t *testing.T) {
		fx := newBranchFixture(t)
		fx.featureBranch("feat/r")
		fx.repo.Git("push", "-q", "origin", "feat/r")
		fx.repo.Fetch()
		step, _ := fx.mustApply(fx.finding("feat/r", "merged-branch", ""))
		if step.Command != "git branch -D -- feat/r" || !strings.Contains(step.Description, "contained in remote") {
			t.Fatalf("step = %+v", step)
		}
	})
	t.Run("forged claims verify nothing", func(t *testing.T) {
		for _, claim := range []string{"squash", "in-remote"} {
			fx := newBranchFixture(t)
			fx.featureBranch("feat/wip")
			fx.repo.Commit("late.txt", "x", "unpushed", testutil.BaseTime.Add(5*time.Hour))
			_, err := fx.plan(fx.finding("feat/wip", "merged-branch", claim))
			wantBranchSkip(t, err, "brooom review")
			fx.env.Force = false
			if !fx.branchExists("feat/wip") {
				t.Fatal("branch was deleted")
			}
		}
	})
}

// TestTrashRefusesUserLevelProtectedPathsThroughSymlinks covers dotfile
// managers and relocated homes: the guard resolves the finding's path, so the
// user-level protect rules must match the resolved spelling as well as the
// lexical one.
func TestTrashRefusesUserLevelProtectedPathsThroughSymlinks(t *testing.T) {
	link := func(t *testing.T, target, name string) {
		t.Helper()
		if err := os.Symlink(target, name); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
	}
	t.Run("symlinked HOME", func(t *testing.T) {
		fx := newTrashFixture(t)
		fx.env.Force = true
		fx.write("users/me/.claude/projects/p1/memory/notes.md", "memory")
		fx.write("users/me/.claude/projects/p2/session.jsonl", "log")
		homeLink := fx.path("homelink")
		link(t, fx.userHome, homeLink)
		t.Setenv("HOME", homeLink)
		t.Setenv("USERPROFILE", homeLink)
		_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(filepath.Join(homeLink, ".claude", "projects", "p1")))
		wantSkip(t, err, "protect")
		if _, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(filepath.Join(homeLink, ".claude", "projects", "p2"))); err != nil {
			t.Errorf("unprotected sibling must still be removable: %v", err)
		}
	})
	t.Run("symlinked .claude", func(t *testing.T) {
		fx := newTrashFixture(t)
		fx.env.Force = true
		fx.write("users/me/dot/claude/projects/p1/memory/notes.md", "memory")
		fx.write("users/me/dot/claude/projects/p2/session.jsonl", "log")
		link(t, fx.path("users/me/dot/claude"), fx.path("users/me/.claude"))
		_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(fx.path("users/me/.claude/projects/p1")))
		wantSkip(t, err, "protect")
		if _, err := (trashAction{}).Plan(context.Background(), fx.env, trashFinding(fx.path("users/me/.claude/projects/p2"))); err != nil {
			t.Errorf("unprotected sibling must still be removable: %v", err)
		}
	})
}
