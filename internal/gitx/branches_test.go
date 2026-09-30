package gitx_test

import (
	"context"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// branchFixture builds a repository with a remote and a branch of every
// upstream state.
func branchFixture(t *testing.T) (*testutil.Repo, string) {
	t.Helper()
	repo := testutil.NewRepoWithRemote(t)

	repo.Checkout("main")
	repo.Git("checkout", "-q", "-b", "feat/x")
	repo.Commit("x.txt", "x", "feat x", at(1))
	repo.Push("feat/x")

	repo.Git("checkout", "-q", "-b", "gone-branch", "main")
	repo.Commit("g.txt", "g", "gone", at(2))
	repo.Push("gone-branch")
	repo.DeleteRemoteBranch("gone-branch")
	repo.Fetch()

	repo.Git("checkout", "-q", "-b", "local-only", "main")
	repo.Commit("l.txt", "l", "local", at(3))

	repo.Git("checkout", "-q", "-b", "fix#12/ünï-cödé", "main")
	repo.Commit("u.txt", "u", "unicode", at(4))

	// main moves on after the branches were cut, so only origin/main (and the
	// symbolic origin/HEAD) contain its new tip.
	repo.Checkout("main")
	repo.Commit("m.txt", "m", "main moves", at(5))
	repo.Push("main")
	wt := repo.AddWorktree("wt", "in-worktree")
	return repo, wt
}

func TestListBranches(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	requireGit(t, r, 2, 23)
	repo, wt := branchFixture(t)

	branches, err := openRepo(t, r, repo.Dir).ListBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]gitx.Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}

	tests := []struct {
		name     string
		upstream string
		gone     bool
		worktree string
		track    string
	}{
		{"main", "origin/main", false, repo.Dir, ""},
		{"feat/x", "origin/feat/x", false, "", ""},
		{"gone-branch", "origin/gone-branch", true, "", "[gone]"},
		{"local-only", "", false, "", ""},
		{"fix#12/ünï-cödé", "", false, "", ""},
		{"in-worktree", "", false, wt, ""},
	}
	if len(branches) != len(tests) {
		t.Errorf("got %d branches, want %d", len(branches), len(tests))
	}
	for _, tc := range tests {
		b, ok := byName[tc.name]
		if !ok {
			t.Errorf("branch %q missing", tc.name)
			continue
		}
		if b.Upstream != tc.upstream || b.UpstreamGone != tc.gone || b.WorktreePath != tc.worktree || b.Track != tc.track {
			t.Errorf("%s = %+v", tc.name, b)
		}
		if len(b.Tip) != 40 || b.Date.IsZero() {
			t.Errorf("%s: tip/date not set: %+v", tc.name, b)
		}
	}
	if got := byName["feat/x"].Date.UTC(); !got.Equal(at(1)) {
		t.Errorf("feat/x date = %v, want %v", got, at(1))
	}
}

func TestListBranchesSingleGitCall(t *testing.T) {
	cr := &countRunner{inner: execRunner(t)}
	repo, _ := branchFixture(t)
	handle := openRepo(t, cr, repo.Dir)
	before := cr.n.Load()
	if _, err := handle.ListBranches(context.Background()); err != nil {
		t.Fatal(err)
	}
	// One for-each-ref plus at most git version; never one call per branch.
	if used := cr.n.Load() - before; used != 1 {
		t.Errorf("ListBranches used %d git calls, want 1", used)
	}
}

func TestListRemoteBranches(t *testing.T) {
	r := execRunner(t)
	repo, _ := branchFixture(t)
	remotes, err := openRepo(t, r, repo.Dir).ListRemoteBranches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, rb := range remotes {
		names = append(names, rb.Name)
		if len(rb.Tip) != 40 || rb.Date.IsZero() {
			t.Errorf("incomplete remote branch %+v", rb)
		}
	}
	want := []string{"origin/feat/x", "origin/main"}
	if len(names) != len(want) || names[0] != want[0] || names[1] != want[1] {
		t.Errorf("remote branches = %v, want %v (origin/HEAD excluded, gone branch pruned)", names, want)
	}
}

func TestRemoteContaining(t *testing.T) {
	ctx := context.Background()
	cr := &countRunner{inner: execRunner(t)}
	repo, _ := branchFixture(t)
	handle := cachedRepo(t, cr, repo.Dir)

	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{"pushed feature", "feat/x", "origin/feat/x", false},
		// origin/HEAD sorts before origin/main; it must not shadow the real branch.
		{"main behind origin/HEAD", "main", "origin/main", false},
		{"unpushed", "local-only", "", false},
		{"unresolvable", "no-such-ref", "", true},
	}
	for _, tc := range tests {
		got, err := handle.RemoteContaining(ctx, tc.ref)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%s: RemoteContaining(%q) = %q, %v; want %q (err %v)", tc.name, tc.ref, got, err, tc.want, tc.wantErr)
		}
	}

	before := cr.n.Load()
	if got, _ := handle.RemoteContaining(ctx, "feat/x"); got != "origin/feat/x" {
		t.Errorf("memoized result = %q", got)
	}
	if cr.n.Load() != before {
		t.Error("second RemoteContaining call started a git process")
	}
}

func TestRemoteContainingWithoutRemote(t *testing.T) {
	repo := testutil.NewRepo(t)
	got, err := openRepo(t, execRunner(t), repo.Dir).RemoteContaining(context.Background(), "main")
	if err != nil || got != "" {
		t.Errorf("got %q, %v; want empty", got, err)
	}
}

func TestUnpushedAndNeverPushed(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo, _ := branchFixture(t)
	repo.Checkout("feat/x")
	repo.Commit("more.txt", "m", "unpushed on feat", at(9))
	repo.Checkout("main")
	handle := openRepo(t, r, repo.Dir)

	tests := []struct {
		branch      string
		unpushed    int
		contained   bool
		neverPushed bool
	}{
		{"main", 0, true, false},
		{"feat/x", 1, false, false},
		{"gone-branch", 1, false, false}, // upstream configured, remote branch deleted
		{"local-only", 1, false, true},
		{"in-worktree", 0, true, true}, // at main's commit: contained, but never pushed under its own name
	}
	branches, err := handle.ListBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]gitx.Branch{}
	for _, b := range branches {
		byName[b.Name] = b
	}
	for _, tc := range tests {
		n, err := handle.UnpushedCount(ctx, tc.branch)
		if err != nil || n != tc.unpushed {
			t.Errorf("%s: UnpushedCount = %d, %v; want %d", tc.branch, n, err, tc.unpushed)
		}
		ok, err := handle.ContainedInRemotes(ctx, tc.branch)
		if err != nil || ok != tc.contained {
			t.Errorf("%s: ContainedInRemotes = %v, %v; want %v", tc.branch, ok, err, tc.contained)
		}
		np, err := handle.NeverPushed(ctx, byName[tc.branch])
		if err != nil || np != tc.neverPushed {
			t.Errorf("%s: NeverPushed = %v, %v; want %v", tc.branch, np, err, tc.neverPushed)
		}
	}
	if _, err := handle.UnpushedCount(ctx, "no-such-ref"); err == nil {
		t.Error("unresolvable ref must be an error, not zero")
	}
}

func TestCommitTime(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.Commit("a.txt", "a", "second", at(5))
	handle := openRepo(t, execRunner(t), repo.Dir)
	got, err := handle.CommitTime(context.Background(), repo.Head())
	if err != nil || !got.UTC().Equal(at(5)) {
		t.Errorf("CommitTime = %v, %v; want %v", got, err, at(5))
	}
	if _, err := handle.CommitTime(context.Background(), "deadbeef"); err == nil {
		t.Error("expected an error for an unknown commit")
	}
}
