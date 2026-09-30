package gitbloat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// blobCacheVersion is bumped when the cached layout changes; files of another
// version are ignored and rebuilt.
const blobCacheVersion = 1

// maxBlobCacheBytes caps the size of a cache file that is read, so a broken
// or hostile file can never exhaust memory.
const maxBlobCacheBytes = 1 << 20

// blobCacheDoc is the on-disk document, one per repository (common git dir).
type blobCacheDoc struct {
	Version int    `json:"version"`
	Key     string `json:"key"`
	Total   int    `json:"total"`
	Blobs   []blob `json:"blobs"`
}

// blobCacheKey fingerprints everything the blob scan result depends on: the
// tips of all refs (refs/replace/* included, so a replacement changes the key)
// and HEAD (rev-list --all walks exactly those), the set of packs with their
// sizes (a repack or fetch changes it), the files that change which history is
// visible (alternates, shallow, grafts; see historyFilesFingerprint) and the
// threshold. Any failure to read the state yields ok = false, which simply
// disables caching.
//
// Known limitation: objects that live in an alternate object store are not
// fingerprinted, only the alternates file itself, so a repack of the borrowed
// repository is not noticed. A scan of such a repository is at worst stale
// until its own refs or packs change.
func blobCacheKey(ctx context.Context, info *repoInfo, min int64) (key string, ok bool) {
	refs, err := info.repo.Runner.Run(ctx, info.repo.Dir, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return "", false
	}
	// A detached HEAD is not a ref but is walked by --all; an unborn HEAD
	// fails and contributes nothing.
	head, _ := info.repo.Runner.Run(ctx, info.repo.Dir, "rev-parse", "-q", "--verify", "HEAD")
	packs, err := packFingerprint(info.repo.Common)
	if err != nil {
		return "", false
	}
	hist, err := historyFilesFingerprint(info.repo.Common)
	if err != nil {
		return "", false
	}
	h := sha256.New()
	fmt.Fprintf(h, "v%d\nmin=%d\nhead=%s\n%s\n%s\n%s", blobCacheVersion, min, head, refs, packs, hist)
	return hex.EncodeToString(h.Sum(nil)), true
}

// historyFiles are the files below the common git dir whose content changes
// what rev-list --objects --all reaches without touching any ref or pack.
var historyFiles = []string{
	filepath.Join("objects", "info", "alternates"),
	"shallow",
	filepath.Join("info", "grafts"),
}

// historyFilesFingerprint hashes the content of historyFiles; a missing file
// contributes a fixed marker so adding or removing one changes the result.
func historyFilesFingerprint(common string) (string, error) {
	var lines []string
	for _, rel := range historyFiles {
		data, err := os.ReadFile(filepath.Join(common, rel))
		switch {
		case os.IsNotExist(err):
			lines = append(lines, rel+" -")
		case err != nil:
			return "", err
		default:
			sum := sha256.Sum256(data)
			lines = append(lines, rel+" "+hex.EncodeToString(sum[:]))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// packFingerprint lists the pack files of the object store with their sizes,
// sorted, so it is independent of directory order.
func packFingerprint(common string) (string, error) {
	des, err := os.ReadDir(filepath.Join(common, "objects", "pack"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var lines []string
	for _, de := range des {
		if !strings.HasSuffix(de.Name(), ".pack") {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			return "", err
		}
		lines = append(lines, de.Name()+" "+strconv.FormatInt(fi.Size(), 10))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n"), nil
}

// blobCachePath names the cache file of a repository below cacheDir.
func blobCachePath(cacheDir, common string) string {
	sum := sha256.Sum256([]byte(common))
	return filepath.Join(cacheDir, "gitbloat-blobs-"+hex.EncodeToString(sum[:])[:16]+".json")
}

// loadBlobCache returns the cached scan when the file exists and was written
// for exactly this key. Every problem (missing, oversized, corrupt, other key
// or implausible content) is a miss.
func loadBlobCache(file, key string, min int64) (blobScan, bool) {
	doc, ok := readBlobCache(file)
	if !ok || doc.Version != blobCacheVersion || doc.Key != key || doc.Total < len(doc.Blobs) || len(doc.Blobs) > maxBlobFindings {
		return blobScan{}, false
	}
	for _, b := range doc.Blobs {
		if b.SHA == "" || b.Size < min {
			return blobScan{}, false
		}
	}
	return blobScan{blobs: doc.Blobs, total: doc.Total}, true
}

// readBlobCache reads and decodes the cache file, size capped.
func readBlobCache(file string) (blobCacheDoc, bool) {
	var doc blobCacheDoc
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxBlobCacheBytes {
		return doc, false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return doc, false
	}
	return doc, json.Unmarshal(data, &doc) == nil
}

// storeBlobCache writes the scan atomically (temp file plus rename). The
// cache is best effort: a failed write must never fail a scan.
func storeBlobCache(file, key string, s blobScan) {
	data, err := json.Marshal(blobCacheDoc{Version: blobCacheVersion, Key: key, Total: s.total, Blobs: s.blobs})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "gitbloat-blobs-*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), file) != nil {
		_ = os.Remove(tmp.Name())
	}
}
