package mergedbranch_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/detectors/mergedbranch"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// gitDir runs git with the test identity in dir.
func gitDir(t *testing.T, l *testutil.BareLayout, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-C", dir}, args...)...)
	cmd.Env = l.Repo.Env(testutil.BaseTime)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// TestBareAnchorLayout is the regression test for issue #200: with the bare
// repository plus linked worktrees layout the first `git worktree list` entry
// is bare, which used to fail every target with "gitx: bare repository".
func TestBareAnchorLayout(t *testing.T) {
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BROOOM_HOME", home)
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	l := testutil.NewBareLayout(t)
	// topic gets its own commit and is merged into main, then main is pushed.
	gitDir(t, l, l.Feat, "checkout", "-q", "-b", "topic")
	gitDir(t, l, l.Feat, "commit", "-q", "--allow-empty", "-m", "work")
	gitDir(t, l, l.Feat, "checkout", "-q", "feat/x")
	gitDir(t, l, l.Main, "merge", "-q", "--no-ff", "-m", "merge topic", "topic")
	gitDir(t, l, l.Main, "push", "-q", "origin", "main")
	gitDir(t, l, l.Bare, "fetch", "-q", "origin")

	guard, err := scope.NewGuard(l.Root)
	if err != nil {
		t.Fatal(err)
	}
	env := &detect.Env{
		Config: config.Default(), Git: runner, Repos: gitx.NewCache(runner), Guard: guard,
		Now: testutil.BaseTime.Add(60 * 24 * time.Hour),
	}
	target := scope.Target{Kind: scope.TargetRepo, Path: l.Main, Scope: findings.Scope{Type: findings.ScopeRoot, Path: l.Root}}
	var got []findings.Finding
	err = mergedbranch.New().Detect(context.Background(), env, target, func(f findings.Finding) { got = append(got, f) })
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	f := byRef(got, "topic")
	if f == nil {
		t.Fatalf("topic not reported, got %v", refs(got))
	}
	if f.Path != l.Bare {
		t.Errorf("path = %q, want the bare anchor %q", f.Path, l.Bare)
	}
}
