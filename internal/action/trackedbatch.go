package action

import (
	"context"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// trackedBatch holds the answer of the tracked-files check for all trash
// targets of one plan or one apply run. Without it every target paid for its
// own `git ls-files`, twice at plan time (dry run plus re-plan) and again in
// Apply, each call scaling with the size of the index.
//
// The batch is built once per repository: at plan time for the whole plan,
// and once more at the start of an apply, so the apply-time answer is live
// (taken after the confirmation prompt) but not repeated for every item.
// Plan and Apply deliberately share no memo: a cached plan-time answer could
// be stale when the user confirms, so each phase pays one batch, not one call
// per target.
type trackedBatch struct {
	answers map[string]trackedAnswer
}

// trackedAnswer is the outcome for one path: either a verdict or the error
// that made the check inconclusive (which callers treat as unknown).
type trackedAnswer struct {
	tracked bool
	err     error
}

type trackedBatchKey struct{}

func withTrackedBatch(ctx context.Context, b *trackedBatch) context.Context {
	return context.WithValue(ctx, trackedBatchKey{}, b)
}

func trackedBatchFrom(ctx context.Context) *trackedBatch {
	b, _ := ctx.Value(trackedBatchKey{}).(*trackedBatch)
	return b
}

// lookup returns the batched answer for path; ok is false for paths the
// batch did not cover, which fall back to a single check.
func (b *trackedBatch) lookup(path string) (trackedAnswer, bool) {
	if b == nil {
		return trackedAnswer{}, false
	}
	a, ok := b.answers[path]
	return a, ok
}

// batchTrackedCheck runs the tracked-files check once per repository for the
// given trash target paths. Paths outside any repository, or whose
// repository cannot be determined, are left out and use the single check
// (which produces the same verdicts). With fewer than two paths batching
// gains nothing and ctx is returned unchanged.
func batchTrackedCheck(ctx context.Context, env *Env, paths []string) context.Context {
	if env.Git == nil || len(paths) < 2 {
		return ctx
	}
	perRepo := map[string][]string{}
	for _, p := range paths {
		root, ok := repoRootOf(p)
		if !ok {
			continue
		}
		perRepo[root] = append(perRepo[root], p)
	}
	b := &trackedBatch{answers: map[string]trackedAnswer{}}
	for root, ps := range perRepo {
		b.checkRepo(ctx, env, root, ps)
	}
	if len(b.answers) == 0 {
		return ctx
	}
	return withTrackedBatch(ctx, b)
}

// checkRepo asks git about ps (absolute paths inside root) in one go. A git
// failure is recorded for every path, so all of them stay unknown.
func (b *trackedBatch) checkRepo(ctx context.Context, env *Env, root string, ps []string) {
	rels := make([]string, 0, len(ps))
	kept := make([]string, 0, len(ps))
	for _, p := range ps {
		rel, err := filepath.Rel(root, p)
		if err != nil || !findings.IsWithin(root, p) {
			continue
		}
		rels = append(rels, filepath.ToSlash(rel))
		kept = append(kept, p)
	}
	tracked, err := gitx.TrackedUnder(ctx, env.Git, root, rels)
	for i, p := range kept {
		if err != nil {
			b.answers[p] = trackedAnswer{err: err}
			continue
		}
		b.answers[p] = trackedAnswer{tracked: tracked[rels[i]]}
	}
}

// repoRootOf finds the repository root around a trash target, the same way
// checkTracked does.
func repoRootOf(path string) (string, bool) {
	root, err := scope.FindRepoRoot(repoLookupStart(path))
	if err != nil {
		return "", false
	}
	return root, true
}

// batchTrackedForItems is batchTrackedForFindings for the steps of an apply.
func (e *Executor) batchTrackedForItems(ctx context.Context, items []Item) context.Context {
	fs := make([]findings.Finding, len(items))
	for i, it := range items {
		fs[i] = it.Step.Finding
	}
	return e.batchTrackedForFindings(ctx, fs)
}

// batchTrackedForFindings collects the trash targets of cands that survive
// the static checks and batches their tracked check. The batch is only a
// pre-warm of the per-step check: a target this function skips or resolves
// differently from the step path is simply absent from the batch and falls
// back to the single check, so drift between the two costs speed, never
// correctness.
func (e *Executor) batchTrackedForFindings(ctx context.Context, cands []findings.Finding) context.Context {
	var paths []string
	for _, f := range cands {
		if f.SuggestedAction.Type != findings.ActionTrash {
			continue
		}
		path, err := resolveTarget(e.env, f.Path)
		if err != nil || refuseTarget(e.env, path) != nil {
			continue
		}
		paths = append(paths, path)
	}
	return batchTrackedCheck(ctx, e.env, paths)
}
