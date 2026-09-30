package walk

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	// DefaultCacheMaxAge is how long a DirSize cache file may go unused
	// before it counts as stale. A file that is still being read is touched
	// by every scan, so only caches of abandoned roots reach this age.
	DefaultCacheMaxAge = 30 * 24 * time.Hour

	// cacheGlob matches the cache files of cacheFilePath.
	cacheGlob = "dirsize-v1-*.json"
	// tmpGlob matches the temp files of an interrupted storeCache.
	tmpGlob = "dirsize-*.tmp"
	// tmpMaxAge keeps the temp file of a write that may still be running.
	tmpMaxAge = time.Hour

	// verdictSubdir is where gitx stores squash verdicts. Their key contains
	// the base sha, so every new base commit orphans them; they are pruned by
	// age like the size caches.
	verdictSubdir = "verdicts"
	// verdictGlob matches verdict files, verdictTmpGlob the temp files of an
	// interrupted verdictStore.put.
	verdictGlob    = "*.json"
	verdictTmpGlob = "tmp-*"
)

// StaleCacheFile is a cache file PruneCache found unneeded.
type StaleCacheFile struct {
	Path      string
	SizeBytes int64
	// Reason says why it is stale: "unused for N days", "root no longer
	// exists", "unreadable or oversized" or "interrupted write".
	Reason string
}

// PruneOptions tunes ListStaleCache and PruneCache.
type PruneOptions struct {
	// MaxAge is the unused time after which a file is stale; zero means
	// DefaultCacheMaxAge.
	MaxAge time.Duration
	// Now is the reference time; zero means time.Now().
	Now time.Time
	// CheckRoots also marks files whose queried root no longer exists. It
	// needs to open every file, which is why the automatic pruning during a
	// scan leaves it off.
	CheckRoots bool
}

// ListStaleCache lists cache files below dir that are safe to delete: not
// used for MaxAge (mtime, see sizer.persist), pointing at a root that no
// longer exists (CheckRoots), unreadable or larger than a cache file can be,
// and temp files of interrupted writes. Only regular files with the names
// Brooom itself writes are considered; symlinks and anything else in dir are
// left alone. A missing dir is not an error.
func ListStaleCache(dir string, o PruneOptions) ([]StaleCacheFile, error) {
	if o.MaxAge <= 0 {
		o.MaxAge = DefaultCacheMaxAge
	}
	if o.Now.IsZero() {
		o.Now = time.Now()
	}
	out, err := staleByGlob(dir, cacheGlob, tmpGlob, o)
	if err != nil {
		return nil, err
	}
	// Verdict files are tiny and never need a root check: age alone decides.
	vo := o
	vo.CheckRoots = false
	verdicts, err := staleByGlob(filepath.Join(dir, verdictSubdir), verdictGlob, verdictTmpGlob, vo)
	return append(out, verdicts...), err
}

// staleByGlob classifies the files of dir matching the cache glob and the
// temp file glob.
func staleByGlob(dir, cache, tmp string, o PruneOptions) ([]StaleCacheFile, error) {
	var out []StaleCacheFile
	for _, g := range []string{cache, tmp} {
		matches, err := filepath.Glob(filepath.Join(dir, g))
		if err != nil {
			return nil, err
		}
		for _, m := range matches {
			if f, ok := staleReason(m, g == tmp, o); ok {
				out = append(out, f)
			}
		}
	}
	return out, nil
}

// staleReason classifies one candidate file.
func staleReason(path string, isTmp bool, o PruneOptions) (StaleCacheFile, bool) {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return StaleCacheFile{}, false
	}
	f := StaleCacheFile{Path: path, SizeBytes: fi.Size()}
	age := o.Now.Sub(fi.ModTime())
	switch {
	case isTmp:
		f.Reason = "interrupted write"
		return f, age > tmpMaxAge
	case age > o.MaxAge:
		f.Reason = "unused for " + strconv.Itoa(int(age.Hours()/24)) + " days"
		return f, true
	case fi.Size() > maxCacheBytes:
		f.Reason = "unreadable or oversized"
		return f, true
	case o.CheckRoots:
		return rootReason(f)
	}
	return f, false
}

// rootReason marks the file stale when it cannot be parsed or its root is
// gone. Errors other than "does not exist" (permissions, a removed volume)
// keep the file, because the root may come back.
func rootReason(f StaleCacheFile) (StaleCacheFile, bool) {
	root, ok := readCacheRoot(f.Path)
	if !ok {
		f.Reason = "unreadable or oversized"
		return f, true
	}
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		f.Reason = "root no longer exists"
		return f, true
	}
	return f, false
}

// readCacheRoot reads the version and root of a cache file without decoding
// its records (up to 32 MiB). storeCache writes them in that order, and a file
// that does not start that way is not one of ours.
func readCacheRoot(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return "", false
	}
	var version int
	var root string
	if !readField(dec, "version", &version) || !readField(dec, "root", &root) {
		return "", false
	}
	return root, version == cacheVersion && root != ""
}

// readField consumes the next object member, which must be named key.
func readField(dec *json.Decoder, key string, dst any) bool {
	tok, err := dec.Token()
	if err != nil || tok != key {
		return false
	}
	return dec.Decode(dst) == nil
}

// PruneCache deletes what ListStaleCache reports and returns the deleted
// files. The first error stops nothing: every file is attempted and the
// errors are joined, so one locked file cannot keep the rest around.
func PruneCache(dir string, o PruneOptions) ([]StaleCacheFile, error) {
	stale, err := ListStaleCache(dir, o)
	if err != nil {
		return nil, err
	}
	return RemoveStaleCache(stale)
}

// RemoveStaleCache deletes the listed files, see PruneCache. Files that are
// already gone count as removed.
func RemoveStaleCache(stale []StaleCacheFile) ([]StaleCacheFile, error) {
	var removed []StaleCacheFile
	var errs []error
	for _, f := range stale {
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, f)
	}
	return removed, errors.Join(errs...)
}

// pruned remembers the cache dirs already pruned by this process.
var pruned sync.Map

// pruneOnce removes age-stale cache files the first time this process writes
// into dir, so abandoned roots do not accumulate for users who never run
// `brooom purge`. Best effort: errors are ignored.
func pruneOnce(dir string) {
	if _, done := pruned.LoadOrStore(dir, true); done {
		return
	}
	_, _ = PruneCache(dir, PruneOptions{})
}
