package gitx_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// bigFeature creates a squash-merged branch whose diff is roughly 200 KiB, so
// a low cap makes the check give up while a normal cap finds the squash.
func bigFeature(t *testing.T) *testutil.Repo {
	t.Helper()
	repo := testutil.NewRepo(t)
	repo.Git("checkout", "-q", "-b", "feat")
	repo.Commit("lock.txt", strings.Repeat("generated line of a lockfile\n", 7000), "feat: lock", at(1))
	repo.Checkout("main")
	repo.SquashMerge("feat", "squash feat", at(2))
	return repo
}

func TestSquashDetectionDiffCap(t *testing.T) {
	ctx := context.Background()
	repo := bigFeature(t)
	r := execRunner(t)

	normal := openRepo(t, r, repo.Dir)
	res, err := normal.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true)
	if err != nil || res.Method != gitx.MethodSquash {
		t.Fatalf("without a cap hit: %+v, %v; want squash", res, err)
	}

	capped := openRepo(t, r, repo.Dir)
	gitx.SetDiffLimit(capped, 1024)
	res, err = capped.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true)
	if err != nil {
		t.Fatal(err)
	}
	if res.Merged || !res.Truncated {
		t.Errorf("over the cap: %+v; want not merged and Truncated", res)
	}
}

func TestPipeLimitKillsBothProcesses(t *testing.T) {
	repo := bigFeature(t)
	r := execRunner(t)

	var lines int
	start := time.Now()
	// The blob is ~200 KiB; a 4 KiB cap must stop the producer early.
	err := gitx.PipeLimit(context.Background(), r, repo.Dir,
		[]string{"cat-file", "-p", "feat:lock.txt"}, []string{"patch-id", "--stable"}, 4096,
		func(string) { lines++ })
	if !errors.Is(err, gitx.ErrOutputLimit) {
		t.Fatalf("PipeLimit = %v, want ErrOutputLimit", err)
	}
	if time.Since(start) > 30*time.Second {
		t.Errorf("kill was not prompt: %v", time.Since(start))
	}

	// Within the cap the same call succeeds (patch-id prints nothing for
	// non-diff input) and reports no error.
	if err := gitx.PipeLimit(context.Background(), r, repo.Dir,
		[]string{"cat-file", "-p", "feat:lock.txt"}, []string{"patch-id", "--stable"}, 1<<20,
		func(string) {}); err != nil {
		t.Errorf("within the cap: %v", err)
	}
}

func TestExecRunnerMaxOutput(t *testing.T) {
	repo := bigFeature(t)
	base := execRunner(t)
	bounded := &gitx.ExecRunner{Path: base.Path, MaxOutput: 4096}

	_, err := bounded.Run(context.Background(), repo.Dir, "cat-file", "-p", "feat:lock.txt")
	if !errors.Is(err, gitx.ErrOutputLimit) {
		t.Errorf("Run = %v, want ErrOutputLimit", err)
	}
	out, err := bounded.Run(context.Background(), repo.Dir, "rev-parse", "HEAD")
	if err != nil || len(out) != 40 {
		t.Errorf("small output: %q, %v", out, err)
	}
}
