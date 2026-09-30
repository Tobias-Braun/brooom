package action

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// storeSize is the measured disk usage of what maintenance can shrink.
type storeSize struct {
	// objects is loose size + pack size + garbage size from count-objects.
	objects int64
	// logs is the size of all reflog directories; only measured when the
	// action can change it.
	logs int64
}

// reclaimedBy returns how much smaller after is, per part clamped at zero: a
// repack that temporarily grows one part must not offset a real saving in the
// other, and a negative number would be reported as "reclaimed".
func (s storeSize) reclaimedBy(after storeSize) int64 {
	return max(s.objects-after.objects, 0) + max(s.logs-after.logs, 0)
}

// measureStore measures the object store with `git count-objects -v` and,
// when withLogs is set, the reflog directories with a fresh (cache-bypassing)
// walk: reflogs are appended to in place, which directory mtimes do not show.
func measureStore(ctx context.Context, env *Env, repo *gitx.Repo, withLogs bool) (storeSize, error) {
	stats, err := gitx.CountObjects(ctx, env.Git, repo.Dir)
	if err != nil {
		return storeSize{}, fmt.Errorf("git count-objects in %s: %w", repo.Dir, err)
	}
	s := storeSize{objects: stats.Size + stats.SizePack + stats.SizeGarbage}
	if withLogs {
		if s.logs, err = logsSize(ctx, repo); err != nil {
			return storeSize{}, err
		}
	}
	return s, nil
}

// logsSize sums <common>/logs and the logs of every linked worktree. Missing
// directories (reflogs disabled, no worktrees) count as zero.
func logsSize(ctx context.Context, repo *gitx.Repo) (int64, error) {
	var total int64
	for _, dir := range gitx.WorktreeGitDirs(repo.Common) {
		sum, err := walk.DirSize(ctx, filepath.Join(dir, "logs"), walk.Options{Fresh: true})
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("measure reflogs of %s: %w", repo.Common, err)
		}
		total += sum.SizeBytes
	}
	return total, nil
}

// unreachable is what `git prune -n` would delete.
type unreachable struct {
	count int
	// bytes is the combined (uncompressed) size of the sampled objects and
	// sampled how many were sized; sampled < count means bytes is a lower bound.
	bytes   int64
	sampled int
}

// dryRunPrune lists the objects `git prune -n --expire=<date>` would delete
// and sizes up to maxSizedObjects of them with `git cat-file --batch-check`.
// A runner that cannot feed stdin only loses the size.
func dryRunPrune(ctx context.Context, env *Env, repo *gitx.Repo, date string) (unreachable, error) {
	out, err := env.Git.Run(ctx, repo.Dir, "prune", "-n", "--expire="+date)
	if err != nil {
		return unreachable{}, dateRejected(err, date)
	}
	var ids []string
	for _, l := range gitx.Lines(out) {
		if fields := strings.Fields(l); len(fields) > 0 {
			ids = append(ids, fields[0])
		}
	}
	u := unreachable{count: len(ids)}
	if len(ids) == 0 {
		return u, nil
	}
	sample := ids[:min(len(ids), maxSizedObjects)]
	res, err := gitx.RunInput(ctx, env.Git, repo.Dir, strings.NewReader(strings.Join(sample, "\n")+"\n"), "cat-file", "--batch-check")
	if err != nil {
		return u, nil //nolint:nilerr // sizes are informational; the count is what matters
	}
	u.bytes, u.sampled = sumBatchCheck(res)
	return u, nil
}

// sumBatchCheck adds up the size column of `cat-file --batch-check` output
// ("<oid> <type> <size>"); lines of missing objects ("<oid> missing") are
// not counted.
func sumBatchCheck(out string) (total int64, n int) {
	for _, l := range gitx.Lines(out) {
		fields := strings.Fields(l)
		if len(fields) != 3 {
			continue
		}
		var size int64
		if _, err := fmt.Sscan(fields[2], &size); err != nil || size < 0 {
			continue
		}
		total += size
		n++
	}
	return total, n
}

// dryRunReflog counts the entries `git reflog expire --dry-run --verbose`
// reports as "would prune".
func dryRunReflog(ctx context.Context, env *Env, repo *gitx.Repo, date string) (int, error) {
	out, err := env.Git.Run(ctx, repo.Dir, "reflog", "expire", "--dry-run", "--verbose", "--expire="+date, "--all")
	if err != nil {
		return 0, dateRejected(err, date)
	}
	n := 0
	for _, l := range gitx.Lines(out) {
		if strings.HasPrefix(strings.TrimSpace(l), "would prune") {
			n++
		}
	}
	return n, nil
}
