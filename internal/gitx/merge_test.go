package gitx_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// featWithTwoCommits creates branch feat with commits touching a.txt and
// b.txt and leaves main checked out.
func featWithTwoCommits(r *testutil.Repo) {
	r.Git("checkout", "-q", "-b", "feat")
	r.Commit("a.txt", "a\n", "feat: a", at(1))
	r.Commit("b.txt", "b\n", "feat: b", at(2))
	r.Checkout("main")
}

func TestMergedInto(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)

	tests := []struct {
		name       string
		setup      func(r *testutil.Repo)
		wantMerged bool
		wantMethod string
		// withoutSquash is the expected Merged when includeSquash is false.
		withoutSquash bool
	}{
		{
			name: "fast-forward merge",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.Git("merge", "--ff-only", "-q", "feat")
			},
			wantMerged: true, wantMethod: gitx.MethodAncestor, withoutSquash: true,
		},
		{
			name: "merge commit",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.Commit("main.txt", "m\n", "main moves", at(3))
				r.GitAt(at(4), "merge", "--no-ff", "-q", "-m", "merge feat", "feat")
			},
			wantMerged: true, wantMethod: gitx.MethodAncestor, withoutSquash: true,
		},
		{
			name: "squash merge of a multi-commit branch",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.SquashMerge("feat", "squash feat", at(3))
			},
			wantMerged: true, wantMethod: gitx.MethodSquash,
		},
		{
			name: "rebase merge with a moved base",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.Commit("main.txt", "m\n", "main moves", at(3))
				r.RebaseMerge("feat", at(4))
			},
			wantMerged: true, wantMethod: gitx.MethodRebase,
		},
		{
			name: "rebase merge of a branch with an empty commit",
			setup: func(r *testutil.Repo) {
				r.Git("checkout", "-q", "-b", "feat")
				r.Commit("a.txt", "a\n", "feat: a", at(1))
				r.CommitAll("feat: empty", at(2))
				r.Commit("b.txt", "b\n", "feat: b", at(2))
				r.Checkout("main")
				r.Commit("main.txt", "m\n", "main moves", at(3))
				r.RebaseMerge("feat", at(4))
			},
			wantMerged: true, wantMethod: gitx.MethodRebase,
		},
		{
			name: "partially cherry-picked branch is not merged",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.GitAt(at(3), "cherry-pick", r.Git("rev-parse", "feat~1"))
			},
		},
		{
			name: "squash-looking branch with a later extra commit",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.SquashMerge("feat", "squash feat", at(3))
				r.Checkout("feat")
				r.Commit("c.txt", "c\n", "feat: late", at(4))
				r.Checkout("main")
			},
		},
		{
			name: "diverged branch is not merged",
			setup: func(r *testutil.Repo) {
				featWithTwoCommits(r)
				r.Commit("main.txt", "m\n", "main moves", at(3))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := testutil.NewRepo(t)
			tc.setup(repo)
			handle := openRepo(t, r, repo.Dir)

			got, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", true)
			if err != nil {
				t.Fatal(err)
			}
			if got.Merged != tc.wantMerged || got.Method != tc.wantMethod {
				t.Errorf("MergedInto(squash) = %+v, want merged=%v method=%q", got, tc.wantMerged, tc.wantMethod)
			}
			plain, err := handle.MergedInto(ctx, "refs/heads/main", "refs/heads/feat", false)
			if err != nil || plain.Merged != tc.withoutSquash {
				t.Errorf("MergedInto(no squash) = %+v, %v; want merged=%v", plain, err, tc.withoutSquash)
			}
		})
	}
}

func TestIsAncestor(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	handle := openRepo(t, execRunner(t), repo.Dir)

	if ok, err := handle.IsAncestor(ctx, "refs/heads/main", "refs/heads/feat"); err != nil || !ok {
		t.Errorf("main is an ancestor of feat: %v, %v", ok, err)
	}
	if ok, err := handle.IsAncestor(ctx, "refs/heads/feat", "refs/heads/main"); err != nil || ok {
		t.Errorf("feat is not an ancestor of main: %v, %v", ok, err)
	}
	// A bad ref is an error (exit 128), never silently "false".
	if ok, err := handle.IsAncestor(ctx, "refs/heads/no-such-ref", "refs/heads/main"); err == nil || ok {
		t.Errorf("bad ref: %v, %v; want an error", ok, err)
	}
}

func TestSquashMergedIsReadOnly(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	repo.SquashMerge("feat", "squash feat", at(3))
	before := repo.Git("for-each-ref") + repo.Git("count-objects", "-v")

	res, err := openRepo(t, execRunner(t), repo.Dir).SquashMerged(ctx, "refs/heads/main", "refs/heads/feat")
	if err != nil || res.Method != gitx.MethodSquash {
		t.Fatalf("SquashMerged = %+v, %v", res, err)
	}
	if after := repo.Git("for-each-ref") + repo.Git("count-objects", "-v"); after != before {
		t.Errorf("repository changed:\n%s\n--\n%s", before, after)
	}
}

// noInput hides ExecRunner's RunInput so patch-id cannot be used.
type noInput struct{ gitx.Runner }

func TestSquashMergedUnknownWithoutInput(t *testing.T) {
	repo := testutil.NewRepo(t)
	featWithTwoCommits(repo)
	repo.SquashMerge("feat", "squash feat", at(3))
	handle := openRepo(t, noInput{execRunner(t)}, repo.Dir)

	res, err := handle.MergedInto(context.Background(), "refs/heads/main", "refs/heads/feat", true)
	if err != nil || res.Merged {
		t.Errorf("without stdin support the answer must be not merged, got %+v, %v", res, err)
	}
}

func TestSquashMergedRefusesUnknownRefs(t *testing.T) {
	repo := testutil.NewRepo(t)
	handle := openRepo(t, execRunner(t), repo.Dir)
	res, err := handle.SquashMerged(context.Background(), "refs/heads/main", "refs/heads/no-such-branch")
	if err == nil || res.Merged {
		t.Errorf("got %+v, %v; want error and not merged", res, err)
	}
}

func TestSquashMergedUnrelatedHistories(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.Git("checkout", "-q", "--orphan", "orphan")
	repo.Commit("o.txt", "o", "orphan root", at(1))
	repo.Checkout("main")
	res, err := openRepo(t, execRunner(t), repo.Dir).MergedInto(context.Background(), "refs/heads/main", "refs/heads/orphan", true)
	if err != nil || res.Merged {
		t.Errorf("unrelated histories: %+v, %v; want not merged", res, err)
	}
}

// TestSquashMergedFakeRunnerFailure feeds a failing patch-id through a fake
// to prove failures never turn into "merged".
func TestSquashMergedFakeRunnerFailure(t *testing.T) {
	fake := patchFail{fakeRunner(func(args []string) (string, error) {
		switch {
		case args[0] == "rev-parse":
			return strings.Repeat("a", 40), nil
		case args[0] == "merge-base" && len(args) == 3:
			return strings.Repeat("b", 40), nil
		case args[0] == "rev-list":
			return "1", nil
		default:
			return "diff --git a/x b/x\n", nil
		}
	})}
	repo := &gitx.Repo{Runner: fake, Dir: "."}
	got, err := repo.SquashMerged(context.Background(), "refs/heads/main", "refs/heads/feat")
	if err == nil || got.Merged {
		t.Errorf("patch-id failure: %+v, %v; want error and not merged", got, err)
	}
	if errors.Is(err, gitx.ErrInputUnsupported) {
		t.Error("fake implements RunInput; error should come from patch-id")
	}
}

// patchFail is a fakeRunner whose RunInput always fails.
type patchFail struct{ fakeRunner }

func (patchFail) RunInput(_ context.Context, _ string, _ io.Reader, _ ...string) (string, error) {
	return "", errors.New("patch-id crashed")
}
