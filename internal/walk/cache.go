package walk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// cacheVersion is bumped whenever the record layout changes; files of
	// another version are ignored and rebuilt.
	cacheVersion = 1
	// maxCacheBytes caps the size of a cache file that is read. A larger
	// file is treated as corrupt so a broken cache can never exhaust memory.
	maxCacheBytes = 32 << 20
	// maxCacheDirs caps the number of directory records persisted per
	// queried path; huge trees are recomputed instead of cached.
	maxCacheDirs = 250_000
)

// linkRecord is a multiply-linked file of a directory. Hard links are kept
// out of the per-directory totals so they can be deduplicated across the
// whole tree at aggregation time, also when the record comes from the cache.
type linkRecord struct {
	ID    string `json:"id"`
	Bytes int64  `json:"bytes"`
}

// dirRecord is what the cache remembers about one directory. It only covers
// the entries directly inside it, subdirectories have records of their own,
// so a changed subdirectory invalidates just itself.
type dirRecord struct {
	MtimeUnixNano int64 `json:"mtime_unix_nano"`
	// ID is dev:ino on unix and empty on Windows.
	ID string `json:"id"`
	// Racy records were written within racyWindow of the scan start: a
	// modification in the same timestamp tick would be invisible, so they
	// are always re-read (the idea behind git's racy index entries).
	Racy         bool         `json:"racy"`
	DirectBytes  int64        `json:"direct_bytes"`
	DirectFiles  int          `json:"direct_files"`
	DirectNewest int64        `json:"direct_newest"`
	Links        []linkRecord `json:"links,omitempty"`
	// Subdirs are base names of the real subdirectories.
	Subdirs []string `json:"subdirs"`
}

// cacheFile is the on-disk document, one per queried path.
type cacheFile struct {
	Version int    `json:"version"`
	Root    string `json:"root"`
	// Dirs maps the forward-slash path relative to the queried root (empty
	// for the root itself) to its record.
	Dirs map[string]*dirRecord `json:"dirs"`
}

// cacheLocks serialises writers of the same cache file within a process;
// across processes the last atomic rename wins.
var cacheLocks sync.Map // cache file path -> *sync.Mutex

// cacheFilePath names the cache file of an absolute queried path.
func cacheFilePath(cacheDir, absPath string) string {
	sum := sha256.Sum256([]byte(absPath))
	return filepath.Join(cacheDir, "dirsize-v1-"+hex.EncodeToString(sum[:])[:16]+".json")
}

// loadCache returns the usable records for absPath. Every problem (missing,
// too large, corrupt, wrong version or root, implausible content) yields nil
// so the caller silently rebuilds.
func loadCache(file, absPath string) map[string]*dirRecord {
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxCacheBytes {
		return nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil
	}
	if cf.Version != cacheVersion || cf.Root != absPath || !validRecords(cf.Dirs) {
		return nil
	}
	return cf.Dirs
}

// validRecords rejects documents that would make the sizer stat unexpected
// locations, such as subdirectory names containing separators or "..".
func validRecords(dirs map[string]*dirRecord) bool {
	for _, rec := range dirs {
		if rec == nil {
			return false
		}
		for _, name := range rec.Subdirs {
			if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
				return false
			}
		}
	}
	return true
}

// storeCache writes the records atomically (temp file in the same directory
// plus rename). The cache is best effort: failures are returned so tests can
// see them, but DirSize ignores them.
func storeCache(file, absPath string, dirs map[string]*dirRecord) error {
	if len(dirs) > maxCacheDirs {
		return nil
	}
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Root: absPath, Dirs: dirs})
	if err != nil {
		return fmt.Errorf("walk: encode cache: %w", err)
	}
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("walk: create cache dir %s: %w", dir, err)
	}
	mu, _ := cacheLocks.LoadOrStore(file, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	tmp, err := os.CreateTemp(dir, "dirsize-*.tmp") // created 0600
	if err != nil {
		return fmt.Errorf("walk: write cache in %s: %w", dir, err)
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("walk: write cache %s: %w", tmp.Name(), firstErr(werr, cerr))
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("walk: replace cache %s: %w", file, err)
	}
	return nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
