package gitx_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// hangingScript installs an executable named name in a fresh PATH directory.
// It starts a background grandchild that inherits stdout and stderr and
// outlives the deadline, then hangs itself: the situation in which
// exec.Cmd.Wait blocks on the pipe long after the context ended.
func hangingScript(t *testing.T, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell script as the fake binary")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nsleep 20 &\nsleep 20\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestExecRunnerHonoursDeadlineWithPipeHolder used to block for the whole
// grandchild lifetime (20 s here) instead of the 500 ms deadline.
func TestExecRunnerHonoursDeadlineWithPipeHolder(t *testing.T) {
	r := &gitx.ExecRunner{Path: hangingScript(t, "git")}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := r.Run(ctx, t.TempDir(), "status")
	if err == nil {
		t.Fatal("want an error after the deadline")
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("Run blocked for %v past a 500ms deadline", d)
	}
}

// TestHangingGHIsBounded runs the real execGH through OpenPRBranches with a
// fake gh whose grandchild holds the pipe.
func TestHangingGHIsBounded(t *testing.T) {
	gh := hangingScript(t, "gh")
	t.Setenv("PATH", filepath.Dir(gh)+string(os.PathListSeparator)+os.Getenv("PATH"))
	repo := testutil.NewRepo(t)
	handle := openRepo(t, execRunner(t), repo.Dir)

	start := time.Now()
	info := handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{Timeout: 500 * time.Millisecond})
	if info.Known {
		t.Error("a hanging gh must yield unknown PR information")
	}
	if d := time.Since(start); d > 8*time.Second {
		t.Errorf("OpenPRBranches blocked for %v past a 500ms timeout", d)
	}
}

// ghCounter is a GHRunner that counts calls and answers with reply.
type ghCounter struct {
	n     atomic.Int64
	reply func(ctx context.Context) ([]byte, error)
}

func (g *ghCounter) run(ctx context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
	g.n.Add(1)
	return g.reply(ctx)
}

func hangUntilDone(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// cacheRepos opens n independent repositories through one Cache (one scan).
func cacheRepos(t *testing.T, n int) []*gitx.Repo {
	t.Helper()
	cache := gitx.NewCache(execRunner(t))
	var repos []*gitx.Repo
	for i := 0; i < n; i++ {
		r, err := cache.Repo(context.Background(), testutil.NewRepo(t).Dir)
		if err != nil {
			t.Fatal(err)
		}
		repos = append(repos, r)
	}
	return repos
}

// TestGHBreakerStopsAfterFirstTimeout: 20 repositories with a hanging gh used
// to cost 20 timeouts; now the first one trips the breaker. The assertion is
// on the number of gh calls, not on elapsed time: the wall-clock cap it
// replaced included creating the repositories and failed at 2.1s against 2s on
// a slow Windows runner (issue #180). gh hangs until its own timeout, so one
// call is exactly one timeout.
func TestGHBreakerStopsAfterFirstTimeout(t *testing.T) {
	gh := &ghCounter{reply: hangUntilDone}
	repos := cacheRepos(t, 6)
	for _, r := range repos {
		info := r.OpenPRBranches(context.Background(), r.Dir, gitx.PROptions{GH: gh.run, Timeout: 300 * time.Millisecond})
		if info.Known || info.HasOpenPR("x") {
			t.Fatalf("hanging gh must be unknown, got %+v", info)
		}
	}
	if n := gh.n.Load(); n != 1 {
		t.Errorf("gh was called %d times for %d repositories, want 1 (one timeout)", n, len(repos))
	}
}

func TestGHBreakerConcurrentReposShareOneProbe(t *testing.T) {
	gh := &ghCounter{reply: hangUntilDone}
	repos := cacheRepos(t, 6)
	done := make(chan struct{})
	for _, r := range repos {
		go func() {
			r.OpenPRBranches(context.Background(), r.Dir, gitx.PROptions{GH: gh.run, Timeout: 300 * time.Millisecond})
			done <- struct{}{}
		}()
	}
	for range repos {
		<-done
	}
	if n := gh.n.Load(); n != 1 {
		t.Errorf("gh was called %d times by concurrent repos, want 1", n)
	}
}

func TestGHBreakerVerdicts(t *testing.T) {
	tests := []struct {
		name      string
		first     func(context.Context) ([]byte, error)
		wantCalls int64
	}{
		{"missing binary trips", func(context.Context) ([]byte, error) { return nil, errors.New("gh not found in PATH") }, 1},
		{"success keeps gh in use", func(context.Context) ([]byte, error) { return []byte(`[]`), nil }, 3},
		{"bad output is repository specific", func(context.Context) ([]byte, error) { return []byte(`nope`), nil }, 3},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gh := &ghCounter{reply: tc.first}
			for _, r := range cacheRepos(t, 3) {
				r.OpenPRBranches(context.Background(), r.Dir, gitx.PROptions{GH: gh.run})
			}
			if n := gh.n.Load(); n != tc.wantCalls {
				t.Errorf("gh calls = %d, want %d", n, tc.wantCalls)
			}
		})
	}
}

// TestGHBreakerIgnoresParentCancellation: Ctrl-C says nothing about gh, so
// the next repository still gets to ask.
func TestGHBreakerIgnoresParentCancellation(t *testing.T) {
	repos := cacheRepos(t, 2)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	repos[0].OpenPRBranches(ctx, repos[0].Dir, gitx.PROptions{GH: (&ghCounter{reply: hangUntilDone}).run})
	ok := &ghCounter{reply: func(context.Context) ([]byte, error) { return []byte(`[{"headRefName":"feat/x"}]`), nil }}
	info := repos[1].OpenPRBranches(context.Background(), repos[1].Dir, gitx.PROptions{GH: ok.run})
	if !info.HasOpenPR("feat/x") || ok.n.Load() != 1 {
		t.Errorf("breaker must stay closed after a cancelled parent, got %+v (%d calls)", info, ok.n.Load())
	}
}

// TestUncachedHandlesIgnoreBreaker: actions re-check open PRs live.
func TestUncachedHandlesIgnoreBreaker(t *testing.T) {
	gh := &ghCounter{reply: hangUntilDone}
	repo := testutil.NewRepo(t)
	for i := 0; i < 2; i++ {
		handle := openRepo(t, execRunner(t), repo.Dir)
		handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{GH: gh.run, Timeout: 50 * time.Millisecond})
	}
	if n := gh.n.Load(); n != 2 {
		t.Errorf("uncached handles called gh %d times, want 2", n)
	}
}
