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
// runner. Squash verdicts go to v, which the apply run reuses (see
// gitx.Verdicts). Without a runner there is nothing to share and ctx is
// returned.
func withPlanSnapshot(ctx context.Context, env *Env, v *gitx.Verdicts) context.Context {
	if env == nil || env.Git == nil {
		return ctx
	}
	cache := gitx.NewCache(env.Git)
	cache.ShareVerdicts(v)
	return context.WithValue(ctx, planSnapshotKey{}, &planSnapshot{cache: cache})
}

func planSnapshotFrom(ctx context.Context) *planSnapshot {
	s, _ := ctx.Value(planSnapshotKey{}).(*planSnapshot)
	return s
}

// runFactsKey carries the gitx.RunFacts of one apply run (see withRunFacts).
type runFactsKey struct{}

// withRunFacts returns a context carrying fresh run facts for env's git
// runner. The executor creates them once after confirmation, so the re-plan
// and Apply of every item keep their live checks but share the answers that
// cannot change within the run (gitx.RunFacts lists them). It also marks the
// context as the apply pass, in which delete-branch hands the decision of the
// re-plan to Apply instead of evaluating twice in a row. v holds the squash
// verdicts the plan pass of the same executor computed.
func withRunFacts(ctx context.Context, env *Env, v *gitx.Verdicts) context.Context {
	if env == nil || env.Git == nil {
		return ctx
	}
	return context.WithValue(ctx, runFactsKey{}, gitx.NewRunFacts(env.Git, v))
}

func runFactsFrom(ctx context.Context) *gitx.RunFacts {
	f, _ := ctx.Value(runFactsKey{}).(*gitx.RunFacts)
	return f
}
