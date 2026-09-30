package gitx_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

// TestBareAnchor covers the bare repository plus linked worktrees layout
// (issue #200): the anchor is the bare directory, MainWorktree still refuses
// it, and only the anchor-aware openers accept the bare directory.
func TestBareAnchor(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	requireGit(t, r, 2, 23)
	l := testutil.NewBareLayout(t)

	linked := openRepo(t, r, l.Feat)
	path, bare, err := linked.Anchor(ctx)
	if err != nil || !bare || path != l.Bare {
		t.Fatalf("Anchor = %q, %v, %v; want %q, true", path, bare, err, l.Bare)
	}
	if _, err := linked.MainWorktree(ctx); !errors.Is(err, gitx.ErrBareRepo) {
		t.Errorf("MainWorktree err = %v, want ErrBareRepo", err)
	}

	assertAnchorOpeners(t, r, l, linked.Common)
}

// assertAnchorOpeners checks that the strict openers still refuse the bare
// directory while the anchor-aware ones accept it.
func assertAnchorOpeners(t *testing.T, r gitx.Runner, l *testutil.BareLayout, common string) {
	t.Helper()
	ctx := context.Background()
	if _, err := gitx.Open(ctx, r, l.Bare); !errors.Is(err, gitx.ErrBareRepo) {
		t.Errorf("Open(bare) err = %v, want ErrBareRepo", err)
	}
	anchor, err := gitx.OpenAnchor(ctx, r, l.Bare)
	if err != nil {
		t.Fatal(err)
	}
	if anchor.Dir != l.Bare || anchor.Common != common {
		t.Errorf("anchor handle Dir %q Common %q, want %q / %q", anchor.Dir, anchor.Common, l.Bare, common)
	}
	wts, err := anchor.ListWorktrees(ctx)
	if err != nil || len(wts) != 3 {
		t.Fatalf("worktrees = %+v, %v", wts, err)
	}

	cached, err := gitx.NewCache(r).AnchorRepo(ctx, l.Bare)
	if err != nil || cached.Dir != l.Bare {
		t.Errorf("AnchorRepo = %v, %v", cached, err)
	}
	if _, err := gitx.NewCache(r).Repo(ctx, l.Bare); !errors.Is(err, gitx.ErrBareRepo) {
		t.Errorf("Cache.Repo(bare) err = %v, want ErrBareRepo", err)
	}
}

// TestOpenAnchorOrdinaryAndMissing: OpenAnchor is Open for everything that
// is not a bare repository, including the not-a-repository error.
func TestOpenAnchorOrdinaryAndMissing(t *testing.T) {
	ctx := context.Background()
	r := execRunner(t)
	repo := testutil.NewRepo(t)
	h, err := gitx.OpenAnchor(ctx, r, repo.Dir)
	if err != nil || h.Dir != repo.Dir {
		t.Fatalf("OpenAnchor = %v, %v", h, err)
	}
	if _, err := gitx.OpenAnchor(ctx, r, testutil.ResolvedTempDir(t)); !errors.Is(err, gitx.ErrNotRepo) {
		t.Errorf("err = %v, want ErrNotRepo", err)
	}
}
