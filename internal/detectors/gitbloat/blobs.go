package gitbloat

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
)

const (
	// blobScanTimeout bounds the history scan per repository: walking every
	// object of a huge monorepo can take minutes, and a slow detector must not
	// stall the whole scan. On timeout the blob findings are dropped.
	blobScanTimeout = 20 * time.Second
	// maxBlobFindings caps blob findings per repository to the largest ones.
	maxBlobFindings = 20
)

// blob is one large object found in history.
type blob struct {
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
	// Path is the first path that referenced the blob.
	Path string `json:"path,omitempty"`
}

// blobScan is the memoized result of one history scan.
type blobScan struct {
	// blobs are the blobs of at least the threshold, largest first.
	blobs []blob
	// total is the number of such blobs before capping.
	total int
	// failure is why the scan gave up (timeout, git error); empty on success.
	// Failures are memoized per repository handle but never cached on disk.
	failure string
}

// parseBlobLine parses one `cat-file --batch-check` line of the format
// "%(objecttype) %(objectname) %(objectsize) %(rest)". Other object types and
// "missing" lines yield ok = false.
func parseBlobLine(line string) (b blob, ok bool) {
	fields := strings.SplitN(line, " ", 4)
	if len(fields) < 3 || fields[0] != "blob" {
		return blob{}, false
	}
	size, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return blob{}, false
	}
	b = blob{SHA: fields[1], Size: size}
	if len(fields) == 4 {
		b.Path = fields[3]
	}
	return b, true
}

// scanBlobs streams the history through rev-list | cat-file and collects the
// blobs of at least min bytes. rev-list prints each object once, with the first
// path it was reached by.
func scanBlobs(ctx context.Context, r gitx.Runner, dir string, min int64) (blobScan, error) {
	var found []blob
	err := gitx.Pipe(ctx, r, dir,
		[]string{"rev-list", "--objects", "--all"},
		[]string{"cat-file", "--batch-check=%(objecttype) %(objectname) %(objectsize) %(rest)"},
		func(line string) {
			if b, ok := parseBlobLine(line); ok && b.Size >= min {
				found = append(found, b)
			}
		})
	if err != nil {
		return blobScan{}, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].Size != found[j].Size {
			return found[i].Size > found[j].Size
		}
		return found[i].SHA < found[j].SHA
	})
	total := len(found)
	if total > maxBlobFindings {
		found = found[:maxBlobFindings]
	}
	return blobScan{blobs: found, total: total}, nil
}

// blobFindings reports the largest blobs in history, for information only:
// Brooom never rewrites history, so there is no suggested action.
//
// A timeout or other scan failure does not fail the target's other findings,
// but it is returned as an error (alongside no blob findings) so it shows up
// as a scan error: "no large blobs" must be distinguishable from "gave up".
// Successful scans are cached on disk (see blobcache.go), so the expensive
// history walk is repeated only after refs or packs change. Cancellation of
// the parent context is returned as the context error.
func (d *Detector) blobFindings(ctx context.Context, env *detect.Env, info *repoInfo) ([]findings.Finding, error) {
	min := info.cfg.LargeBlobBytes
	if min <= 0 {
		return nil, nil
	}
	key := "git-bloat/blobs/" + strconv.FormatInt(min, 10)
	scan, err := memoized(info.repo, key, func() (blobScan, error) {
		return d.cachedScan(ctx, env, info, min)
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	if scan.failure != "" {
		// The failure is memoized for the repository, so every linked
		// worktree target sees it; only the first one reports it. Known
		// limits: the memo error is discarded (the flag constructor cannot
		// fail), and if the first target's run is canceled before it gets
		// here, the report moves to a later target.
		flag, _ := memoized(info.repo, key+"/reported", func() (*atomic.Bool, error) { return new(atomic.Bool), nil })
		if !flag.CompareAndSwap(false, true) {
			return nil, nil
		}
		// A note: the other checks of the repository are intact.
		return nil, detect.Note(fmt.Errorf("git-bloat: large blob scan of %s incomplete (%s); large blobs were not checked", info.repo.Dir, scan.failure))
	}
	out := make([]findings.Finding, 0, len(scan.blobs))
	for _, b := range scan.blobs {
		out = append(out, blobFinding(info, b, scan.total))
	}
	return out, nil
}

// cachedScan returns the on-disk result when refs, packs and threshold are
// unchanged and otherwise scans the history under the blob timeout, storing a
// successful result. A failure other than parent cancellation is returned as
// blobScan.failure, not as an error.
func (d *Detector) cachedScan(ctx context.Context, env *detect.Env, info *repoInfo, min int64) (blobScan, error) {
	var file, ckey string
	if env.CacheDir != "" {
		if k, ok := blobCacheKey(ctx, info, min); ok {
			ckey, file = k, blobCachePath(env.CacheDir, info.repo.Common)
			if s, hit := loadBlobCache(file, ckey, min); hit {
				return s, nil
			}
		}
	}
	sctx, cancel := context.WithTimeout(ctx, d.blobTimeout)
	defer cancel()
	s, err := scanBlobs(sctx, info.repo.Runner, info.repo.Dir, min)
	if err != nil {
		if ctx.Err() != nil {
			return blobScan{}, err
		}
		if sctx.Err() != nil {
			return blobScan{failure: "timed out after " + d.blobTimeout.String()}, nil
		}
		return blobScan{failure: err.Error()}, nil
	}
	if file != "" {
		storeBlobCache(file, ckey, s)
	}
	return s, nil
}

func blobFinding(info *repoInfo, b blob, total int) findings.Finding {
	f := info.base(findings.KindGitLargeBlob, b.SHA)
	f.Confidence = findings.ConfidenceLow
	f.Evidence = append(f.Evidence, findings.Evidence{
		Code:    "large_blob",
		Message: fmt.Sprintf("blob %s is %s (threshold %s)", shortSHA(b.SHA), output.FormatSize(b.Size), output.FormatSize(info.cfg.LargeBlobBytes)),
		Value:   b.Size,
	})
	if total > maxBlobFindings {
		f.Evidence = append(f.Evidence, findings.Evidence{
			Code:    "truncated",
			Message: fmt.Sprintf("showing the %d largest of %d large blobs", maxBlobFindings, total),
			Value:   total,
		})
	}
	f.Meta = map[string]string{"blob_size": strconv.FormatInt(b.Size, 10)}
	if b.Path != "" {
		f.Meta["blob_path"] = b.Path
	}
	f.SuggestedAction = findings.SuggestedAction{
		Type:   findings.ActionNone,
		Reason: "removing a blob from history means rewriting it, for example with git filter-repo; history rewriting is out of scope for Brooom",
	}
	return f
}

func shortSHA(s string) string {
	if len(s) > 10 {
		return s[:10]
	}
	return s
}
