package gitbloat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

const (
	// looseSavingsPercent estimates what gc saves of the loose objects' disk
	// usage: it packs them with delta compression and drops the per-file
	// block overhead, so roughly half is reclaimed. An estimate, not a
	// promise; the action measures the real value.
	looseSavingsPercent = 50
	// packSavingsPercent estimates the gain of consolidating many packs
	// into one (cross-pack deltas, shared indexes).
	packSavingsPercent = 10
)

// objectFindings reports loose objects and pack fragmentation from the
// memoized count-objects result.
func (d *Detector) objectFindings(ctx context.Context, env *detect.Env, info *repoInfo) ([]findings.Finding, error) {
	stats, err := info.repo.CountObjects(ctx)
	if err != nil {
		return nil, fmt.Errorf("git-bloat: count-objects in %s: %w", info.repo.Dir, err)
	}
	var out []findings.Finding
	mtime := objectsMTime(info.repo.Common)
	if stats.Count > int64(info.cfg.LooseObjectsThreshold) {
		out = append(out, looseFinding(env, info, stats.Count, stats.Size, stats.Garbage, mtime))
	}
	if stats.Packs > int64(info.cfg.PackCountThreshold) {
		out = append(out, packsFinding(env, info, stats.Packs, stats.SizePack, mtime))
	}
	return out, nil
}

// objectsMTime returns the mtime of the object store directory, or the zero
// time. It is a cheap lower bound of "newest object": git only touches this
// directory when it creates a fan-out subdirectory.
func objectsMTime(commonDir string) time.Time {
	fi, err := os.Stat(filepath.Join(commonDir, "objects"))
	if err != nil {
		return time.Time{}
	}
	return fi.ModTime()
}

// gcAction is the suggested action shared by the loose and pack findings.
func gcAction(info *repoInfo, reason string) findings.SuggestedAction {
	prune := info.cfg.PruneExpire
	return findings.SuggestedAction{
		Type:    findings.ActionGitGC,
		Args:    map[string]string{"prune": prune},
		Command: "git gc --prune=" + prune,
		Reason:  reason + "; " + gcReflogNote,
	}
}

func setTime(env *detect.Env, f *findings.Finding, mtime time.Time) {
	if mtime.IsZero() {
		return
	}
	t := mtime
	f.LastModified = &t
	f.AgeDays = env.AgeDays(t)
}

func looseFinding(env *detect.Env, info *repoInfo, count, size, garbage int64, mtime time.Time) findings.Finding {
	f := info.base(findings.KindGitObjects, "")
	saving := size * looseSavingsPercent / 100
	f.SizeBytes = saving
	setTime(env, &f, mtime)
	f.Evidence = append(f.Evidence,
		findings.Evidence{Code: "loose_object_count", Message: fmt.Sprintf("%d loose objects (threshold %d)", count, info.cfg.LooseObjectsThreshold), Value: count},
		findings.Evidence{Code: "loose_size_bytes", Message: "loose objects use " + humanBytes(size), Value: size},
		findings.Evidence{Code: "estimated_savings", Message: fmt.Sprintf("about %d%% of the loose size (%s) is estimated to be reclaimed by packing", looseSavingsPercent, humanBytes(saving)), Value: saving},
	)
	if garbage > 0 {
		f.Evidence = append(f.Evidence, findings.Evidence{Code: "garbage", Message: fmt.Sprintf("%d unrecognised files in the object store", garbage), Value: garbage})
	}
	f.SuggestedAction = gcAction(info, "packing loose objects and pruning unreachable ones older than "+info.cfg.PruneExpire+" saves disk space")
	return f
}

func packsFinding(env *detect.Env, info *repoInfo, packs, size int64, mtime time.Time) findings.Finding {
	f := info.base(findings.KindGitPacks, "")
	saving := size * packSavingsPercent / 100
	f.SizeBytes = saving
	setTime(env, &f, mtime)
	f.Evidence = append(f.Evidence,
		findings.Evidence{Code: "pack_count", Message: fmt.Sprintf("%d packs (threshold %d)", packs, info.cfg.PackCountThreshold), Value: packs},
		findings.Evidence{Code: "pack_size_bytes", Message: "packs use " + humanBytes(size), Value: size},
		findings.Evidence{Code: "estimated_savings", Message: fmt.Sprintf("about %d%% of the pack size (%s) is estimated to be reclaimed by repacking", packSavingsPercent, humanBytes(saving)), Value: saving},
	)
	f.SuggestedAction = gcAction(info, "repacking consolidates many small packs into one")
	return f
}
