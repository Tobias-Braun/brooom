package buildartifacts

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// fixture is a project tree (a git repository or a plain folder) with a
// fixed scan time and a configuration the tests tweak before running the
// detector.
type fixture struct {
	t      *testing.T
	dir    string
	repo   *testutil.Repo
	runner gitx.Runner
	now    time.Time
	cfg    *config.Config
	force  bool
}

// newFixture isolates git and Brooom from the developer's machine. With git
// true the tree is a repository whose initial commit is at BaseTime (200
// days before the scan time).
func newFixture(t *testing.T, git bool) *fixture {
	t.Helper()
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	home := testutil.ResolvedTempDir(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BROOOM_HOME", filepath.Join(home, ".brooom"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(home, ".gitconfig-none"))
	f := &fixture{t: t, runner: runner, now: testutil.BaseTime.AddDate(0, 0, 200), cfg: config.Default()}
	if git {
		f.repo = testutil.NewRepo(t)
		f.dir = f.repo.Dir
	} else {
		f.dir = testutil.ResolvedTempDir(t)
	}
	return f
}

func (f *fixture) daysAgo(n int) time.Time { return f.now.Add(-time.Duration(n) * 24 * time.Hour) }

// write creates files (with content "x") below the tree.
func (f *fixture) write(rels ...string) {
	f.t.Helper()
	for _, rel := range rels {
		testutil.WriteFile(f.t, f.dir, rel, "x")
	}
}

// settle sets the mtime of every file and directory (except .git) to when,
// so that nothing depends on the wall clock of the test run.
func (f *fixture) settle(when time.Time) {
	f.t.Helper()
	err := filepath.WalkDir(f.dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		return os.Chtimes(p, when, when)
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

// touch sets the mtime of one file.
func (f *fixture) touch(rel string, when time.Time) {
	f.t.Helper()
	testutil.SetMTime(f.t, filepath.Join(f.dir, filepath.FromSlash(rel)), when)
}

// commit commits everything at the given time.
func (f *fixture) commit(when time.Time) {
	f.t.Helper()
	f.repo.CommitAll("work", when)
}

func (f *fixture) target() scope.Target {
	kind := scope.TargetProject
	if f.repo != nil {
		kind = scope.TargetRepo
	}
	return scope.Target{Kind: kind, Path: f.dir, Scope: findings.Scope{Type: findings.ScopeRepo, Path: f.dir}}
}

func (f *fixture) env(allowed ...string) *detect.Env {
	f.t.Helper()
	guard, err := scope.NewGuard(append([]string{f.dir}, allowed...)...)
	if err != nil {
		f.t.Fatal(err)
	}
	return &detect.Env{Config: f.cfg, Git: f.runner, Repos: gitx.NewCache(f.runner), Guard: guard, Now: f.now, Force: f.force}
}

// run executes the detector and returns the findings in emission order.
func (f *fixture) run() []findings.Finding {
	f.t.Helper()
	out, err := f.runWith(context.Background(), f.env(), f.target())
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func (f *fixture) runWith(ctx context.Context, env *detect.Env, target scope.Target) ([]findings.Finding, error) {
	var out []findings.Finding
	err := New().Detect(ctx, env, target, func(x findings.Finding) { out = append(out, x) })
	return out, err
}

// byRel runs the detector and keys the findings by their slash path
// relative to the tree.
func (f *fixture) byRel() map[string]findings.Finding {
	f.t.Helper()
	out := map[string]findings.Finding{}
	for _, x := range f.run() {
		rel, err := filepath.Rel(f.dir, x.Path)
		if err != nil {
			f.t.Fatal(err)
		}
		out[filepath.ToSlash(rel)] = x
	}
	return out
}

// rels returns the sorted relative paths of the findings.
func rels(m map[string]findings.Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func evidenceOf(f findings.Finding, code string) (findings.Evidence, bool) {
	for _, e := range f.Evidence {
		if e.Code == code {
			return e, true
		}
	}
	return findings.Evidence{}, false
}

func hasEvidence(f findings.Finding, code string) bool {
	_, ok := evidenceOf(f, code)
	return ok
}

// gitInit makes the tree a repository without any commit.
func (f *fixture) gitInit() {
	f.t.Helper()
	cmd := exec.Command("git", "init", "-q", "-b", "main", f.dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		f.t.Fatalf("git init: %v\n%s", err, out)
	}
}
