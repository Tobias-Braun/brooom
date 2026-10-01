package gitx

import (
	"context"
	"sync"
)

// RunFacts is the state the uncached handles of one apply run share. Actions
// re-check the live repository before every change, so their handles never
// memoize branch tips, ancestry, upstreams or worktrees. A few answers cannot
// change in a way that matters within one run, though, and recomputing them
// for every item made deleting twenty branches take half a minute:
//
//   - the git version;
//   - which configured base refs exist (a run never deletes a base branch;
//     the base commit itself is still resolved live by every merge check);
//   - the open pull requests, asked once per repository after confirmation;
//   - squash/rebase verdicts, which are pure functions of the resolved base
//     and tip commits (see Verdicts). They are kept in memory only and never
//     read from the on-disk scan cache, so a forged cache file still cannot
//     influence what a deletion is verified against.
//
// A RunFacts is safe for concurrent use.
type RunFacts struct {
	runner Runner
	// shared supplies the git version and the gh breaker; RunFacts never
	// hands out its memoizing handles.
	shared   *Cache
	verdicts *Verdicts

	mu    sync.Mutex
	repos map[string]*repoFacts
}

// repoFacts is the per-repository part of RunFacts, keyed by common dir so
// all worktrees of a repository share it.
type repoFacts struct {
	bases      memo[string, Base]
	candidates memo[string, []Base]
	prs        memo[struct{}, PRInfo]
}

// NewRunFacts returns empty run facts using runner. Verdicts computed by this
// process before, typically by the plan pass that preceded the confirmation,
// are reused when v is given; nil starts an empty store.
func NewRunFacts(runner Runner, v *Verdicts) *RunFacts {
	if v == nil {
		v = NewVerdicts()
	}
	return &RunFacts{runner: runner, shared: NewCache(runner), verdicts: v, repos: map[string]*repoFacts{}}
}

// OpenAnchor is gitx.OpenAnchor for one item of the run: an uncached handle
// whose run-wide facts (see RunFacts) are shared with the other items.
func (f *RunFacts) OpenAnchor(ctx context.Context, dir string) (*Repo, error) {
	top, common, err := resolveAnchor(ctx, f.runner, dir)
	if err != nil {
		return nil, err
	}
	v, err := f.shared.gitVersion(ctx)
	if err != nil {
		return nil, err
	}
	r := &Repo{Runner: f.runner, Dir: top, Common: common, known: &v, gh: f.shared.gh, facts: f.forRepo(common), verdicts: f.verdicts}
	if err := r.requireGit(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

func (f *RunFacts) forRepo(common string) *repoFacts {
	f.mu.Lock()
	defer f.mu.Unlock()
	rf, ok := f.repos[common]
	if !ok {
		rf = &repoFacts{}
		f.repos[common] = rf
	}
	return rf
}

// verdictCache stores definite squash/rebase verdicts by verdictKey.
type verdictCache interface {
	get(key string) (MergeResult, bool)
	put(key string, res MergeResult)
}

// Verdicts is an in-memory squash/rebase verdict store that one process
// shares between the plan pass and the apply run. A verdict is a pure
// function of the resolved base and tip commits (verdictKey), and only this
// process's own definite answers are stored, so reusing one moments later is
// as good as recomputing it. Unlike the on-disk scan cache nothing outside the
// process can put an answer here.
type Verdicts struct {
	mu sync.Mutex
	m  map[string]MergeResult
}

// NewVerdicts returns an empty store.
func NewVerdicts() *Verdicts { return &Verdicts{} }

// ShareVerdicts makes every handle this Cache creates afterwards use v for
// squash verdicts instead of the on-disk store. It must be called before the
// first Repo call.
func (c *Cache) ShareVerdicts(v *Verdicts) {
	if v != nil {
		c.verdicts = v
	}
}

func (v *Verdicts) get(key string) (MergeResult, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	res, ok := v.m[key]
	return res, ok
}

func (v *Verdicts) put(key string, res MergeResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.m == nil {
		v.m = map[string]MergeResult{}
	}
	v.m[key] = res
}

// sharedBases memoizes a DefaultBase answer in the run facts when the handle
// has some, else in the handle's own memo (cached handles only).
func (r *Repo) sharedBases(own *memo[string, Base], key string, f func() (Base, error)) (Base, error) {
	if r.facts != nil {
		return r.facts.bases.do(key, f)
	}
	return cached(r, own, key, f)
}

// sharedCandidates is sharedBases for BaseCandidates.
func (r *Repo) sharedCandidates(key string, f func() ([]Base, error)) ([]Base, error) {
	if r.facts != nil {
		return r.facts.candidates.do(key, f)
	}
	return cached(r, &r.candidates, key, f)
}

// sharedPRs is sharedBases for the open pull requests.
func (r *Repo) sharedPRs(f func() (PRInfo, error)) PRInfo {
	m := &r.prs
	if r.facts != nil {
		m = &r.facts.prs
	} else if !r.memoize {
		info, _ := f()
		return info
	}
	info, _ := m.do(struct{}{}, f)
	return info
}
