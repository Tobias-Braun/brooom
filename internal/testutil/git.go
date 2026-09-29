// Package testutil provides helpers for tests that need real git
// repositories and directory trees. Repositories are created in t.TempDir()
// with a fixed identity, fixed dates and no user or system git config, so
// tests are deterministic on every OS and in CI.
package testutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Repo is a throwaway git repository for tests.
type Repo struct {
	t   testing.TB
	Dir string
	// home isolates git from the developer's global config.
	home string
}

// BaseTime is the fixed reference time commits default to.
var BaseTime = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// NewRepo creates a repository with an initial commit on branch "main" in a
// temporary directory. The returned Dir is symlink-resolved (macOS TMPDIR
// lives below a symlink), so it can be compared with Brooom's resolved paths.
func NewRepo(t testing.TB) *Repo {
	t.Helper()
	dir := ResolvedTempDir(t)
	r := &Repo{t: t, Dir: dir, home: ResolvedTempDir(t)}
	r.Git("init", "-q", "-b", "main")
	r.WriteFile("README.md", "# test\n")
	r.CommitAll("initial commit", BaseTime)
	return r
}

// ResolvedTempDir returns t.TempDir() with symlinks resolved.
func ResolvedTempDir(t testing.TB) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Env returns the environment used for git commands in tests. It isolates
// git from global/system config and fixes identity and dates.
func (r *Repo) Env(when time.Time) []string {
	date := when.Format(time.RFC3339)
	return append(os.Environ(),
		"HOME="+r.home,
		"USERPROFILE="+r.home,
		"XDG_CONFIG_HOME="+filepath.Join(r.home, ".config"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(r.home, ".gitconfig-none"),
		"GIT_AUTHOR_NAME=Brooom Test",
		"GIT_AUTHOR_EMAIL=test@brooom.invalid",
		"GIT_COMMITTER_NAME=Brooom Test",
		"GIT_COMMITTER_EMAIL=test@brooom.invalid",
		"GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_DATE="+date,
		"GIT_TERMINAL_PROMPT=0",
		"LC_ALL=C",
	)
}

// Git runs git in the repository at BaseTime and returns trimmed stdout. It
// fails the test on error.
func (r *Repo) Git(args ...string) string {
	r.t.Helper()
	return r.GitAt(BaseTime, args...)
}

// GitAt runs git with author/committer dates set to when.
func (r *Repo) GitAt(when time.Time, args ...string) string {
	r.t.Helper()
	full := append([]string{"-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Dir
	cmd.Env = r.Env(when)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFile writes content to a path relative to the repository, creating
// parent directories.
func (r *Repo) WriteFile(rel, content string) string {
	r.t.Helper()
	return WriteFile(r.t, r.Dir, rel, content)
}

// CommitAll stages everything and commits with the given date.
func (r *Repo) CommitAll(msg string, when time.Time) {
	r.t.Helper()
	r.GitAt(when, "add", "-A")
	r.GitAt(when, "commit", "-q", "--allow-empty", "-m", msg)
}

// Branch creates a branch at the current HEAD without checking it out.
func (r *Repo) Branch(name string) {
	r.t.Helper()
	r.Git("branch", name)
}

// Checkout switches to an existing branch.
func (r *Repo) Checkout(name string) {
	r.t.Helper()
	r.Git("checkout", "-q", name)
}

// Head returns the full SHA of HEAD.
func (r *Repo) Head() string {
	r.t.Helper()
	return r.Git("rev-parse", "HEAD")
}

// WriteFile writes content to dir/rel, creating parent directories, and
// returns the absolute path.
func WriteFile(t testing.TB, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// SetMTime sets the access and modification time of a path.
func SetMTime(t testing.TB, path string, when time.Time) {
	t.Helper()
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}
