package gitx_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// fakeBinary installs an executable POSIX shell script named name in a fresh
// directory and returns its path. Tests use it as a stand-in for git or gh
// where a real process cannot misbehave on demand (hang, stay silent).
func fakeBinary(t *testing.T, name, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the fake binary")
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunTimeoutIsReadable used to surface as an *Error with exit -1 and no
// stderr; now it names the command and the elapsed time and still matches
// context.DeadlineExceeded.
func TestRunTimeoutIsReadable(t *testing.T) {
	r := &gitx.ExecRunner{Path: fakeBinary(t, "git", "exec sleep 30"), Timeout: 300 * time.Millisecond}
	_, err := r.Run(context.Background(), t.TempDir(), "gc", "--prune=now")
	var terr *gitx.TimeoutError
	if !errors.As(err, &terr) {
		t.Fatalf("err = %T %v, want *TimeoutError", err, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("a timeout must match context.DeadlineExceeded")
	}
	for _, want := range []string{"git gc --prune=now", "timed out after"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q lacks %q", err.Error(), want)
		}
	}
	var gerr *gitx.Error
	if errors.As(err, &gerr) {
		t.Error("a timeout must not be reported as a git exit status")
	}
}

// TestRunCancelIsNotATimeout keeps Ctrl-C distinguishable from a deadline.
func TestRunCancelIsNotATimeout(t *testing.T) {
	r := &gitx.ExecRunner{Path: fakeBinary(t, "git", "exec sleep 30")}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(200*time.Millisecond, cancel)
	_, err := r.Run(ctx, t.TempDir(), "status")
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want a non-timeout failure", err)
	}
}

// TestWithTimeoutOverridesDefaultBound covers the longer bound of maintenance:
// the same slow command fails under the runner default and passes under an
// explicit or disabled bound.
func TestWithTimeoutOverridesDefaultBound(t *testing.T) {
	r := &gitx.ExecRunner{Path: fakeBinary(t, "git", "sleep 1\necho done"), Timeout: 200 * time.Millisecond}
	dir := t.TempDir()
	if _, err := r.Run(context.Background(), dir, "gc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("default bound: err = %v, want a timeout", err)
	}
	for name, d := range map[string]time.Duration{"longer": 30 * time.Second, "disabled": -1} {
		out, err := r.Run(gitx.WithTimeout(context.Background(), d), dir, "gc")
		if err != nil || out != "done" {
			t.Errorf("%s: out %q, err %v", name, out, err)
		}
	}
	// A deadline on the context still beats the override.
	ctx, cancel := context.WithTimeout(gitx.WithTimeout(context.Background(), -1), 200*time.Millisecond)
	defer cancel()
	if _, err := r.Run(ctx, dir, "gc"); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("explicit deadline: err = %v, want a timeout", err)
	}
}

// TestPipeHasDefaultTimeout: Pipe used to have no bound of its own, unlike
// Run, so a hung producer stalled a history scan forever.
func TestPipeHasDefaultTimeout(t *testing.T) {
	r := &gitx.ExecRunner{Path: fakeBinary(t, "git", "exec sleep 30"), Timeout: 300 * time.Millisecond}
	for name, limit := range map[string]int64{"unlimited": 0, "limited": 1 << 20} {
		start := time.Now()
		err := gitx.PipeLimit(context.Background(), r, t.TempDir(), []string{"log"}, []string{"cat-file"}, limit, func(string) {})
		var terr *gitx.TimeoutError
		if !errors.As(err, &terr) || !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want *TimeoutError", name, err)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s: pipe ran %v past a 300ms bound", name, d)
		}
	}
}

// TestPipeConsumerExitsEarlyWithSilentProducer used to block until the
// deadline: exec's stdin copy goroutine waited on the silent producer.
func TestPipeConsumerExitsEarlyWithSilentProducer(t *testing.T) {
	body := "case \"$3\" in\n  cat-file) exec sleep 30 ;;\n  *) exit 0 ;;\nesac"
	r := &gitx.ExecRunner{Path: fakeBinary(t, "git", body)}
	for name, limit := range map[string]int64{"unlimited": 0, "limited": 1 << 20} {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		start := time.Now()
		err := gitx.PipeLimit(ctx, r, t.TempDir(), []string{"cat-file"}, []string{"patch-id"}, limit, func(string) {})
		cancel()
		if err != nil {
			t.Errorf("%s: err = %v, want nil", name, err)
		}
		if d := time.Since(start); d > 10*time.Second {
			t.Errorf("%s: pipe blocked for %v after the consumer exited", name, d)
		}
	}
}

// TestLimitReaderBoundsBytes asserts the byte bound itself with a counting
// reader: nothing beyond the cap is handed on and the source is not drained.
func TestLimitReaderBoundsBytes(t *testing.T) {
	const limit, chunk = 4096, 1024
	var read atomic.Int64
	src := countingReader{n: &read, chunk: chunk}
	var cancelled atomic.Bool
	lr := gitx.NewLimitReader(src, limit, func() { cancelled.Store(true) })

	var delivered int64
	buf := make([]byte, chunk)
	var err error
	for err == nil {
		var n int
		n, err = lr.Read(buf)
		delivered += int64(n)
	}
	if !errors.Is(err, gitx.ErrOutputLimit) || !cancelled.Load() {
		t.Fatalf("err = %v, cancelled = %v; want ErrOutputLimit and a cancel", err, cancelled.Load())
	}
	if delivered > limit {
		t.Errorf("delivered %d bytes, cap is %d", delivered, limit)
	}
	if got := read.Load(); got > limit+chunk {
		t.Errorf("read %d bytes from the producer, want at most %d", got, limit+chunk)
	}
}

// countingReader is an endless source that counts what was read from it.
type countingReader struct {
	n     *atomic.Int64
	chunk int
}

func (c countingReader) Read(p []byte) (int, error) {
	n := min(len(p), c.chunk)
	c.n.Add(int64(n))
	return n, nil
}

var _ io.Reader = countingReader{}

// failOn fails every git call with an argument starting with one of prefixes
// using the given stderr, like a git that hits the problem, and runs the rest
// for real.
type failOn struct {
	*gitx.ExecRunner
	prefixes []string
	stderr   string
}

func (r failOn) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if slices.ContainsFunc(args, func(a string) bool {
		return slices.ContainsFunc(r.prefixes, func(p string) bool { return strings.HasPrefix(a, p) })
	}) {
		return "", &gitx.Error{Args: args, Dir: dir, ExitCode: 128, Stderr: r.stderr}
	}
	return r.ExecRunner.Run(ctx, dir, args...)
}

// TestMergedIntoOnlyMapsPartialCloneToNotMerged: "unable to read" and "bad
// object" also mean real corruption; they used to be cached as "not merged".
func TestMergedIntoOnlyMapsPartialCloneToNotMerged(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.Git("checkout", "-q", "-b", "feat")
	repo.Commit("f.txt", "x\n", "feat", at(1))
	repo.Checkout("main")
	base := execRunner(t)

	tests := []struct {
		name    string
		stderr  string
		wantErr bool
	}{
		{"corruption: unable to read", "fatal: unable to read 1234abcd", true},
		{"corruption: bad object", "fatal: bad object refs/heads/feat", true},
		{"corruption: missing blob", "error: missing blob object 'abc'", true},
		{"partial clone: promisor", "fatal: could not fetch abc from promisor remote", false},
		{"partial clone: lazy fetch off", "fatal: lazy fetching disabled; some objects may not be available", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Both ancestry queries fail the same way: the batched for-each-ref
			// and the single merge-base fallback.
			r := failOn{ExecRunner: base, prefixes: []string{"--merged=", "--is-ancestor"}, stderr: tc.stderr}
			handle, err := gitx.Open(context.Background(), r, repo.Dir)
			if err != nil {
				t.Fatal(err)
			}
			res, err := handle.MergedInto(context.Background(), "refs/heads/main", "refs/heads/feat", false)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if res.Merged {
				t.Errorf("an unknown answer must never be merged: %+v", res)
			}
		})
	}
}

// TestGHRunsWithSanitizedEnvironment: gh spawns git and used to inherit the
// caller's GIT_DIR through an unsanitized os.Environ().
func TestGHRunsWithSanitizedEnvironment(t *testing.T) {
	gh := fakeBinary(t, "gh", `if [ -n "$GIT_DIR" ] || [ -n "$GIT_INDEX_FILE" ]; then
  echo '[{"headRefName":"leaked"}]'
else
  echo '[{"headRefName":"clean"}]'
fi`)
	t.Setenv("PATH", filepath.Dir(gh)+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := testutil.NewRepo(t)
	handle := openRepo(t, execRunner(t), repo.Dir)
	t.Setenv("GIT_DIR", "/foreign/.git")
	t.Setenv("GIT_INDEX_FILE", "/foreign/.git/index")

	info := handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{})
	if !info.Known || !info.HasOpenPR("clean") || info.HasOpenPR("leaked") {
		t.Errorf("gh saw the caller's GIT_* variables: %+v", info)
	}
}

// TestBreakerOpensAfterFirstSuccess: once gh had answered, a later network
// failure used to leave the breaker closed, costing a timeout per repository.
func TestBreakerOpensAfterFirstSuccess(t *testing.T) {
	cache := gitx.NewCache(execRunner(t))
	var calls atomic.Int32
	gh := func(context.Context, string, []string, ...string) ([]byte, error) {
		if calls.Add(1) == 1 {
			return []byte(`[]`), nil
		}
		return nil, errors.New("dial tcp: i/o timeout")
	}
	var infos []gitx.PRInfo
	for i := 0; i < 4; i++ {
		repo := testutil.NewRepo(t)
		handle, err := cache.Repo(context.Background(), repo.Dir)
		if err != nil {
			t.Fatal(err)
		}
		infos = append(infos, handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{GH: gh}))
	}
	if !infos[0].Known {
		t.Error("the first call succeeded and must be known")
	}
	for i, info := range infos[1:] {
		if info.Known {
			t.Errorf("repo %d: want unknown after gh went down", i+1)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("gh called %d times, want 2 (the breaker must open after the failure)", got)
	}
}

// TestSafeDirectoryCommandQuoting checks the hint is pasteable for paths with
// spaces and quotes on every OS.
func TestSafeDirectoryCommandQuoting(t *testing.T) {
	const prefix = "git config --global --add safe.directory "
	tests := []struct {
		goos, dir, want string
	}{
		{"linux", "/w/repo", "/w/repo"},
		{"linux", "/w/my repo", "'/w/my repo'"},
		{"darwin", "/Users/o'neil/repo", `'/Users/o'\''neil/repo'`},
		{"linux", "/w/a;rm -rf x", "'/w/a;rm -rf x'"},
		{"windows", `C:\work\repo`, `C:\work\repo`},
		{"windows", `C:\my work\repo`, `"C:\my work\repo"`},
	}
	for _, tc := range tests {
		if got := gitx.SafeDirectoryCommand(tc.dir, tc.goos); got != prefix+tc.want {
			t.Errorf("%s %q: got %q, want %q", tc.goos, tc.dir, got, prefix+tc.want)
		}
	}
	err := &gitx.UnsafeRepoError{Dir: "/w/my repo", Stderr: "dubious ownership"}
	if runtime.GOOS != "windows" && !strings.Contains(err.Error(), prefix+"'/w/my repo'") {
		t.Errorf("error text %q lacks the quoted command", err.Error())
	}
}

// TestStrippedEnvIsCaseInsensitiveOnWindows: environment names are case
// insensitive on Windows, so "git_dir" must go there and stay elsewhere.
func TestStrippedEnvIsCaseInsensitiveOnWindows(t *testing.T) {
	tests := []struct {
		goos, entry string
		want        bool
	}{
		{"windows", "git_dir=C:\\x", true},
		{"windows", "Git_Index_File=C:\\x", true},
		{"windows", "GIT_CONFIG_KEY_3=a", true},
		{"windows", "git_config_value_0=a", true},
		{"windows", "git_config_count=1", true},
		{"windows", "GIT_AUTHOR_NAME=keep", false},
		{"linux", "git_dir=/x", false},
		{"linux", "GIT_DIR=/x", true},
	}
	for _, tc := range tests {
		if got := gitx.StrippedOS(tc.entry, tc.goos); got != tc.want {
			t.Errorf("%s %q: stripped = %v, want %v", tc.goos, tc.entry, got, tc.want)
		}
	}
}
