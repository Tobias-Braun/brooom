package gitx_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func failingGH(context.Context, string, []string, ...string) ([]byte, error) {
	return nil, errors.New("gh: not logged in")
}

func TestOpenPRBranches(t *testing.T) {
	repo := testutil.NewRepo(t)
	runner := execRunner(t)

	tests := []struct {
		name      string
		gh        gitx.GHRunner
		timeout   time.Duration
		wantKnown bool
		wantPR    []string
	}{
		{
			name: "success",
			gh: func(_ context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
				return []byte(`[{"headRefName":"feat/x"},{"headRefName":"fix#1"}]`), nil
			},
			wantKnown: true,
			wantPR:    []string{"feat/x", "fix#1"},
		},
		{
			name: "no open PRs",
			gh: func(context.Context, string, []string, ...string) ([]byte, error) {
				return []byte(`[]`), nil
			},
			wantKnown: true,
		},
		{name: "gh fails", gh: failingGH},
		{
			name: "bad json",
			gh: func(context.Context, string, []string, ...string) ([]byte, error) {
				return []byte(`not json`), nil
			},
		},
		{
			name:    "timeout",
			timeout: 20 * time.Millisecond,
			gh: func(ctx context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
		},
		{
			name:    "timeout even when the runner ignores the context",
			timeout: 20 * time.Millisecond,
			// The stub answers successfully, but only once the deadline has
			// passed (it waits for it instead of sleeping a guessed time), so
			// the outcome does not depend on scheduling: a late answer must
			// still count as a timeout.
			gh: func(ctx context.Context, _ string, _ []string, _ ...string) ([]byte, error) {
				<-ctx.Done()
				return []byte(`[{"headRefName":"late"}]`), nil
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handle := openRepo(t, runner, repo.Dir)
			info := handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{GH: tc.gh, Timeout: tc.timeout})
			if info.Known != tc.wantKnown {
				t.Fatalf("Known = %v, want %v", info.Known, tc.wantKnown)
			}
			for _, b := range []string{"feat/x", "fix#1", "late", "other"} {
				if got, want := info.HasOpenPR(b), slices.Contains(tc.wantPR, b); got != want {
					t.Errorf("HasOpenPR(%q) = %v, want %v", b, got, want)
				}
			}
		})
	}
}

func TestOpenPRBranchesCommandAndEnv(t *testing.T) {
	repo := testutil.NewRepo(t)
	var gotArgs, gotEnv []string
	var gotDir string
	gh := func(_ context.Context, dir string, env []string, args ...string) ([]byte, error) {
		gotArgs, gotEnv, gotDir = args, env, dir
		return []byte(`[]`), nil
	}
	openRepo(t, execRunner(t), repo.Dir).OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{GH: gh})

	want := []string{"pr", "list", "--state", "open", "--json", "headRefName", "--limit", "500"}
	if !slices.Equal(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
	for _, e := range []string{"GH_PROMPT_DISABLED=1", "NO_COLOR=1", "GH_PAGER=cat"} {
		if !slices.Contains(gotEnv, e) {
			t.Errorf("env %v lacks %s", gotEnv, e)
		}
	}
	if gotDir != repo.Dir {
		t.Errorf("dir = %q, want %q", gotDir, repo.Dir)
	}
}

func TestOpenPRBranchesMissingGH(t *testing.T) {
	repo := testutil.NewRepo(t)
	handle := openRepo(t, execRunner(t), repo.Dir)
	t.Setenv("PATH", t.TempDir()) // no gh here
	if info := handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{}); info.Known {
		t.Error("missing gh must yield Known=false")
	}
}

func TestOpenPRBranchesMemoized(t *testing.T) {
	repo := testutil.NewRepo(t)
	handle := cachedRepo(t, execRunner(t), repo.Dir)
	calls := 0
	gh := func(context.Context, string, []string, ...string) ([]byte, error) {
		calls++
		return []byte(`[]`), nil
	}
	for i := 0; i < 3; i++ {
		handle.OpenPRBranches(context.Background(), repo.Dir, gitx.PROptions{GH: gh})
	}
	if calls != 1 {
		t.Errorf("gh called %d times, want 1", calls)
	}
}
