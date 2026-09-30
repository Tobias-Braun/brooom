package gitbloat

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// reflogUsage is the combined size of all reflogs of a repository.
type reflogUsage struct {
	size  int64
	mtime time.Time
}

// reflogDirs lists <common>/logs plus the per-worktree logs below
// <common>/worktrees/*/logs, where linked worktrees keep their HEAD reflog.
// The directory is read instead of globbed so metacharacters in the path
// cannot break the lookup.
func reflogDirs(commonDir string) []string {
	dirs := []string{filepath.Join(commonDir, "logs")}
	entries, err := os.ReadDir(filepath.Join(commonDir, "worktrees"))
	if err != nil {
		return dirs
	}
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(commonDir, "worktrees", e.Name(), "logs"))
		}
	}
	return dirs
}

// measureReflogs sums the sizes of all reflog directories. Missing ones
// (reflogs disabled, no worktrees) count as zero. Freshness is requested
// because the newest mtime feeds LastModified and reflogs are appended to in
// place.
func measureReflogs(ctx context.Context, env *detect.Env, commonDir string) (reflogUsage, error) {
	var u reflogUsage
	for _, dir := range reflogDirs(commonDir) {
		sum, err := walk.DirSize(ctx, dir, walk.Options{CacheDir: env.CacheDir, Fresh: true})
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return reflogUsage{}, err
		}
		u.size += sum.SizeBytes
		if sum.NewestModTime.After(u.mtime) {
			u.mtime = sum.NewestModTime
		}
	}
	return u, nil
}

// reflogFinding reports reflogs above the threshold. Entries older than the
// expiry cannot be sized cheaply, so the whole size is the upper bound of
// what expiring frees.
func (d *Detector) reflogFinding(ctx context.Context, env *detect.Env, info *repoInfo) (*findings.Finding, error) {
	u, err := memoized(info.repo, "git-bloat/reflog", func() (reflogUsage, error) {
		return measureReflogs(ctx, env, info.repo.Common)
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("git-bloat: sizing reflogs of %s: %w", info.repo.Common, err)
	}
	if u.size <= info.cfg.ReflogThresholdBytes {
		return nil, nil
	}
	f := info.base(findings.KindGitReflog, "")
	f.SizeBytes = u.size
	setTime(env, &f, u.mtime)
	f.Evidence = append(f.Evidence,
		findings.Evidence{Code: "reflog_size_bytes", Message: fmt.Sprintf("reflogs use %s (threshold %s)", output.FormatSize(u.size), output.FormatSize(info.cfg.ReflogThresholdBytes)), Value: u.size},
		findings.Evidence{Code: "upper_bound", Message: "the saving is an upper bound: the size of entries older than " + info.cfg.ReflogExpire + " is not known without expiring", Value: true},
	)
	expire := info.cfg.ReflogExpire
	f.SuggestedAction = findings.SuggestedAction{
		Type:    findings.ActionGitReflogExpire,
		Args:    map[string]string{"expire": expire},
		Command: gitx.ReflogExpireCommand(expire),
		Reason: "expiring reflog entries older than " + expire + " shrinks the logs; expired reflog entries can no longer be used to recover deleted branches or reset commits " +
			"(stash entries are uncommitted work and are kept)",
	}
	return &f, nil
}
