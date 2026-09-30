package gitx_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// lookup returns the last value set for key in env, like os/exec does.
func lookup(env []string, key string) (string, bool) {
	val, ok := "", false
	for _, e := range env {
		if k, v, found := strings.Cut(e, "="); found && k == key {
			val, ok = v, true
		}
	}
	return val, ok
}

func TestEnvStripsRepositorySelection(t *testing.T) {
	base := []string{
		"PATH=/bin", "HOME=/home/x",
		"GIT_DIR=/foreign/.git", "GIT_WORK_TREE=/foreign", "GIT_INDEX_FILE=/foreign/.git/index",
		"GIT_COMMON_DIR=/foreign/.git", "GIT_OBJECT_DIRECTORY=/o", "GIT_ALTERNATE_OBJECT_DIRECTORIES=/a",
		"GIT_NAMESPACE=ns", "GIT_PREFIX=sub/", "GIT_CEILING_DIRECTORIES=/c",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1", "GIT_GLOB_PATHSPECS=1", "GIT_LITERAL_PATHSPECS=1",
		"GIT_NOGLOB_PATHSPECS=1", "GIT_ICASE_PATHSPECS=1",
		"GIT_CONFIG_PARAMETERS='core.x=y'", "GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=core.a", "GIT_CONFIG_VALUE_0=b",
		"GIT_AUTHOR_NAME=keep",
	}
	env := gitx.Env(base)
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX", "GIT_CEILING_DIRECTORIES",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_GLOB_PATHSPECS", "GIT_LITERAL_PATHSPECS",
		"GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS", "GIT_CONFIG_PARAMETERS",
	} {
		if v, ok := lookup(env, k); ok {
			t.Errorf("%s survived as %q", k, v)
		}
	}
	for k, want := range map[string]string{"PATH": "/bin", "HOME": "/home/x", "GIT_AUTHOR_NAME": "keep"} {
		if got, _ := lookup(env, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	// The caller's slice must not be modified.
	if !slices.Contains(base, "GIT_DIR=/foreign/.git") {
		t.Error("Env modified its input")
	}
}

func TestEnvForcesReadOnlyConfig(t *testing.T) {
	env := gitx.Env([]string{"GIT_CONFIG_COUNT=2", "GIT_CONFIG_KEY_0=a.b", "GIT_CONFIG_VALUE_0=c", "GIT_CONFIG_KEY_1=core.fsmonitor", "GIT_CONFIG_VALUE_1=/evil"})
	for k, want := range map[string]string{
		"GIT_NO_LAZY_FETCH": "1", "GIT_OPTIONAL_LOCKS": "0", "GIT_CONFIG_COUNT": "1",
		"GIT_CONFIG_KEY_0": "core.fsmonitor", "GIT_CONFIG_VALUE_0": "false",
	} {
		if got, _ := lookup(env, k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if _, ok := lookup(env, "GIT_CONFIG_KEY_1"); ok {
		t.Error("inherited GIT_CONFIG_KEY_1 survived")
	}
}

// TestRunnerIgnoresForeignGitDir reproduces a direnv/hook environment whose
// GIT_DIR points at another repository: git must still act on the target.
func TestRunnerIgnoresForeignGitDir(t *testing.T) {
	r := execRunner(t)
	target := testutil.NewRepo(t)
	target.WriteFile("a.txt", "a")
	target.CommitAll("a", at(0))
	foreign := testutil.NewRepo(t)
	foreign.WriteFile("b.txt", "b")
	foreign.CommitAll("b", at(0))
	t.Setenv("GIT_DIR", filepath.Join(foreign.Dir, ".git"))
	t.Setenv("GIT_INDEX_FILE", filepath.Join(foreign.Dir, ".git", "index"))

	out, err := r.Run(context.Background(), target.Dir, "ls-files", "-z")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") || strings.Contains(out, "b.txt") {
		t.Errorf("ls-files = %q, want the target's files (read the foreign index?)", out)
	}
}

func TestIsMissingObject(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", os.ErrNotExist, false},
		{"unable to read", &gitx.Error{Stderr: "fatal: unable to read 1234"}, true},
		{"promisor", &gitx.Error{Stderr: "fatal: could not fetch abc from promisor remote"}, true},
		{"bad ref", &gitx.Error{Stderr: "fatal: Not a valid object name nope"}, false},
	} {
		if got := gitx.IsMissingObject(tc.err); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestRunnerTimeout checks the default bound applies to contexts without a
// deadline, using a git alias that hangs.
func TestRunnerTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the hanging alias uses sh")
	}
	r := execRunner(t)
	r.Timeout = 300 * time.Millisecond
	repo := testutil.NewRepo(t)
	start := time.Now()
	_, err := r.Run(context.Background(), repo.Dir, "-c", "alias.hang=!sleep 30", "hang")
	if err == nil {
		t.Fatal("expected the hung command to fail")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %v, timeout not applied", time.Since(start))
	}
}

// TestRunnerDoesNotRunFsmonitor checks that a configured fsmonitor hook is
// never executed by read-only status calls.
func TestRunnerDoesNotRunFsmonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fsmonitor hook is a POSIX shell script")
	}
	r := execRunner(t)
	repo := testutil.NewRepo(t)
	repo.WriteFile("a.txt", "a")
	repo.CommitAll("a", at(0))
	marker := filepath.Join(testutil.ResolvedTempDir(t), "ran")
	hook := filepath.Join(testutil.ResolvedTempDir(t), "hook.sh")
	script := "#!/bin/sh\necho ran >> '" + marker + "'\nprintf '\\0'\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.Git("config", "core.fsmonitor", hook)

	if _, err := openRepo(t, r, repo.Dir).IsDirty(context.Background(), repo.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("core.fsmonitor hook was executed by a read-only scan")
	}
}
