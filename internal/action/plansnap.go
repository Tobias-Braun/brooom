package action

import (
	"context"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// planSnapshot is the git state one Plan pass shares between its findings: a
// gitx.Cache hands out one memoizing repository handle per repository, so the
// branch listing, base branch, worktree list, open pull requests and merge
// queries are read once per pass instead of once per finding. Before, every
// delete-branch finding re-opened the repository and listed all refs, which is
// superlinear in the ref count and runs before the user confirms anything.
//
// The snapshot lives in the context of a single planSteps call. Apply and the
// re-plan that precedes it never receive one, so the live per-finding checks
// stay: sharing a snapshot between Plan and Apply would defeat their purpose.
type planSnapshot struct {
	cache *gitx.Cache
}

type planSnapshotKey struct{}

// withPlanSnapshot returns a context carrying a fresh snapshot for env's git
// runner. Without a runner there is nothing to share and ctx is returned.
func withPlanSnapshot(ctx context.Context, env *Env) context.Context {
	if env == nil || env.Git == nil {
		return ctx
	}
	return context.WithValue(ctx, planSnapshotKey{}, &planSnapshot{cache: gitx.NewCache(env.Git)})
}

func planSnapshotFrom(ctx context.Context) *planSnapshot {
	s, _ := ctx.Value(planSnapshotKey{}).(*planSnapshot)
	return s
}
