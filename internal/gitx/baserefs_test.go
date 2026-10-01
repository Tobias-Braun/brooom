package gitx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestBaseCandidatesOneListing pins the cost of base resolution: origin/HEAD
// and every configured candidate used to be probed with one rev-parse each
// (up to nine processes for the default list); one for-each-ref answers all.
func TestBaseCandidatesOneListing(t *testing.T) {
	repo := testutil.NewRepoWithRemote(t)
	r := &countRunner{inner: execRunner(t)}
	h := openRepo(t, r, repo.Dir)
	r.n.Store(0)
	bases, err := h.BaseCandidates(context.Background(), []string{"main", "master", "develop", "trunk"})
	if err != nil {
		t.Fatal(err)
	}
	if got := candidateRefs(bases); len(got) != 2 || got[0] != "origin/main" {
		t.Fatalf("candidates = %v", got)
	}
	if n := r.n.Load(); n != 1 {
		t.Errorf("%d git calls, want 1", n)
	}
}

// TestBaseRefsExactNames: for-each-ref patterns also match refs below a name,
// so a branch release/1 must not make "release" a base.
func TestBaseRefsExactNames(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.Git("branch", "release/1", "main")
	_, err := openRepo(t, execRunner(t), repo.Dir).DefaultBase(context.Background(), []string{"release"})
	if !errors.Is(err, gitx.ErrNoBase) {
		t.Fatalf("err = %v, want ErrNoBase", err)
	}
}

// TestBaseRefsDanglingOriginHead: origin/HEAD pointing at a missing branch is
// ignored like before, and the configured names decide.
func TestBaseRefsDanglingOriginHead(t *testing.T) {
	repo := testutil.NewRepoWithRemote(t)
	repo.Git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/gone")
	b, err := openRepo(t, execRunner(t), repo.Dir).DefaultBase(context.Background(), []string{"main"})
	if err != nil || b.Ref != "origin/main" || b.Source != gitx.BaseSourceConfigRemote {
		t.Fatalf("base = %+v, err = %v", b, err)
	}
}

// TestBaseRefsAnnotatedTagIsPeeled: a candidate ref holding an annotated tag
// of a commit counts, as `rev-parse <ref>^{commit}` did.
func TestBaseRefsAnnotatedTagIsPeeled(t *testing.T) {
	repo := testutil.NewRepo(t)
	repo.Git("tag", "-a", "-m", "v1", "v1", "main")
	tag := repo.Git("rev-parse", "refs/tags/v1")
	// Git refuses non-commits under refs/heads/, not under refs/remotes/.
	repo.Git("update-ref", "refs/remotes/origin/trunk", tag)
	b, err := openRepo(t, execRunner(t), repo.Dir).DefaultBase(context.Background(), []string{"trunk"})
	if err != nil || b.FullRef != "refs/remotes/origin/trunk" {
		t.Fatalf("base = %+v, err = %v", b, err)
	}
}
