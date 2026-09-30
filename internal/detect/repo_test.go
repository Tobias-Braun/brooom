package detect_test

import (
	"context"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestEnvRepo(t *testing.T) {
	runner, err := gitx.NewExecRunner()
	if err != nil {
		t.Skip("git not installed:", err)
	}
	repo := testutil.NewRepo(t)
	ctx := context.Background()

	plain := &detect.Env{Git: runner}
	a, err := plain.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := plain.Repo(ctx, repo.Dir); a == b {
		t.Error("without a cache every call must open a fresh handle")
	}

	shared := &detect.Env{Git: runner, Repos: gitx.NewCache(runner)}
	c, err := shared.Repo(ctx, repo.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if d, _ := shared.Repo(ctx, repo.Dir); c != d {
		t.Error("with a cache the handle must be shared")
	}
}
