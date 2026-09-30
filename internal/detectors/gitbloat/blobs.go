package gitbloat

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
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
	sha  string
	size int64
	// path is the first path that referenced the blob.
	path string
}

// blobScan is the memoized result of one history scan.
type blobScan struct {
	// blobs are the blobs of at least the threshold, largest first.
	blobs []blob
	// total is the number of such blobs before capping.
	total int
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
	b = blob{sha: fields[1], size: size}
	if len(fields) == 4 {
		b.path = fields[3]
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
			if b, ok := parseBlobLine(line); ok && b.size >= min {
				found = append(found, b)
			}
		})
	if err != nil {
		return blobScan{}, err
	}
	sort.Slice(found, func(i, j int) bool {
		if found[i].size != found[j].size {
			return found[i].size > found[j].size
		}
		return found[i].sha < found[j].sha
	})
	total := len(found)
	if total > maxBlobFindings {
		found = found[:maxBlobFindings]
	}
	return blobScan{blobs: found, total: total}, nil
}

// blobFindings reports the largest blobs in history, for information only:
// Brooom never rewrites history, so there is no suggested action. Timeout and
// other scan failures drop the blob findings without failing the target, while
// cancellation of the parent context is returned.
func (d *Detector) blobFindings(ctx context.Context, env *detect.Env, info *repoInfo) ([]findings.Finding, error) {
	min := info.cfg.LargeBlobBytes
	if min <= 0 {
		return nil, nil
	}
	key := "git-bloat/blobs/" + strconv.FormatInt(min, 10)
	scan, err := memoized(info.repo, key, func() (blobScan, error) {
		sctx, cancel := context.WithTimeout(ctx, d.blobTimeout)
		defer cancel()
		s, err := scanBlobs(sctx, info.repo.Runner, info.repo.Dir, min)
		if err != nil && ctx.Err() == nil {
			// Timeout or a scan failure (for example missing objects in a
			// partial clone): report nothing rather than something wrong.
			return blobScan{}, nil
		}
		return s, err
	})
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	out := make([]findings.Finding, 0, len(scan.blobs))
	for _, b := range scan.blobs {
		out = append(out, blobFinding(info, b, scan.total))
	}
	return out, nil
}

func blobFinding(info *repoInfo, b blob, total int) findings.Finding {
	f := info.base(findings.KindGitLargeBlob, b.sha)
	f.Confidence = findings.ConfidenceLow
	f.Evidence = append(f.Evidence, findings.Evidence{
		Code:    "large_blob",
		Message: fmt.Sprintf("blob %s is %s (threshold %s)", shortSHA(b.sha), humanBytes(b.size), humanBytes(info.cfg.LargeBlobBytes)),
		Value:   b.size,
	})
	if total > maxBlobFindings {
		f.Evidence = append(f.Evidence, findings.Evidence{
			Code:    "truncated",
			Message: fmt.Sprintf("showing the %d largest of %d large blobs", maxBlobFindings, total),
			Value:   total,
		})
	}
	f.Meta = map[string]string{"blob_size": strconv.FormatInt(b.size, 10)}
	if b.path != "" {
		f.Meta["blob_path"] = b.path
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
