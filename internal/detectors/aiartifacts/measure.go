package aiartifacts

import (
	"context"
	"os"
	"sync/atomic"
	"time"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// item is a candidate that survived the safety checks and the age and size
// filters, with everything the risk checks and the finding need.
type item struct {
	cand     candidate
	path     string
	size     int64
	mod      time.Time
	age      int
	flags    []findings.RiskFlag
	evidence []findings.Evidence
}

// minAgeDays is the effective minimum age of an entry: the entry's own
// min_age_days, else the detector's, else the global threshold. The order
// follows the milestone notes literally; a user who wants another age for one
// tool adds an extra entry or disables the tool.
func minAgeDays(cfg *config.Config, e catalog.Entry) int {
	if e.MinAgeDays != nil {
		return *e.MinAgeDays
	}
	if cfg.Detectors.AIArtifacts.MinAgeDays != nil {
		return *cfg.Detectors.AIArtifacts.MinAgeDays
	}
	return cfg.Thresholds.MinAgeDays
}

// measureAll sizes and ages every candidate and drops the unsafe and the
// young or small ones. Only cancellation is an error.
func (r *run) measureAll(ctx context.Context, cands []candidate) ([]*item, error) {
	var items []*item
	for _, c := range cands {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		it, ok, err := r.measure(ctx, c)
		if err != nil {
			return nil, err
		}
		if ok {
			items = append(items, it)
		}
	}
	return items, nil
}

// measure resolves the path through the guard and sizes and ages it. Paths
// outside the guard or that vanished are skipped silently.
func (r *run) measure(ctx context.Context, c candidate) (*item, bool, error) {
	path, ok := r.resolve(c)
	if !ok {
		return nil, false, nil
	}
	it := &item{cand: c, path: path}
	var err error
	if c.isDir {
		ok, err = r.measureDir(ctx, it)
	} else {
		ok = measureFile(it)
	}
	if err != nil || !ok {
		return nil, false, err
	}
	it.age = r.env.AgeDays(it.mod)
	if it.age < minAgeDays(r.cfg, c.entry) || it.size < r.cfg.Thresholds.MinSizeBytes {
		return nil, false, nil
	}
	return it, true, nil
}

// resolve validates the candidate path with the guard. Only the link is ever
// the subject of a symlink finding, so for symlinks the parent is resolved and
// the final element kept. A path outside the guard (or one that vanished) is
// not a failure of the target, the candidate is just skipped.
func (r *run) resolve(c candidate) (string, bool) {
	resolve := r.env.Guard.Resolve
	if c.symlink {
		resolve = r.env.Guard.ResolveParent
	}
	path, err := resolve(c.path)
	return path, err == nil
}

// measureFile takes size and mtime from an Lstat, so a symlink is sized and
// dated as the link itself.
func measureFile(it *item) bool {
	fi, err := os.Lstat(it.path)
	if err != nil {
		return false
	}
	it.size, it.mod = walk.LeafSize(fi), fi.ModTime()
	return true
}

// measureDir applies the directory-specific safety rules and sizes it. The
// size pass is Fresh because transcripts and logs are written in place, which
// leaves cached mtimes stale; its HasVCS signal replaces a second traversal
// for the nested-repository rule.
func (r *run) measureDir(ctx context.Context, it *item) (bool, error) {
	protected, err := r.containsProtected(ctx, it.path)
	if err != nil || protected {
		return false, err
	}
	sum, err := walk.DirSize(ctx, it.path, walk.Options{
		Fresh:       true,
		Concurrency: r.cfg.Scan.Concurrency,
	})
	if err != nil {
		return false, ctx.Err()
	}
	if sum.HasVCS {
		return false, nil
	}
	it.size, it.mod = sum.SizeBytes, sum.NewestModTime
	// The directory's own mtime counts too: it moves when entries are
	// added or removed, and it is the only date an empty directory has.
	if fi, err := os.Lstat(it.path); err == nil && fi.ModTime().After(it.mod) {
		it.mod = fi.ModTime()
	}
	return true, nil
}

// containsProtected reports whether anything below dir is protected. Failing
// to read part of the tree counts as "yes": an unreadable subtree could hide
// a tool's configuration, and the safe answer is to keep the directory.
func (r *run) containsProtected(ctx context.Context, dir string) (bool, error) {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var hit, unreadable atomic.Bool
	err := walk.Walk(wctx, dir, walk.Options{Concurrency: r.cfg.Scan.Concurrency}, func(e walk.Entry) walk.Decision {
		if r.protected(e.Path) {
			hit.Store(true)
			cancel()
			return walk.SkipDir
		}
		return walk.Continue
	}, func(string, error) { unreadable.Store(true) })
	if hit.Load() || unreadable.Load() {
		return true, nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return true, nil
	}
	return false, nil
}
