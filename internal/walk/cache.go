package walk

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	// cacheVersion is bumped whenever the record layout changes; files of
	// another version are ignored and rebuilt. History:
	//   1: initial layout (per-directory totals, hard link records)
	//   2: added HasGit
	//   3: added Incomplete
	//   4: renamed HasGit to HasVCS and widened it (other VCS metadata, bare
	//      repository shape)
	//   5: DirectBytes counts the blocks of the directories themselves
	cacheVersion = 5
	// maxCacheDirs caps the number of directory records persisted per
	// queried path; huge trees are recomputed instead of cached.
	maxCacheDirs = 250_000
)

// maxCacheBytes caps the size of a cache file. A larger file is treated as
// corrupt when read, so a broken cache can never exhaust memory, and is never
// written, because a file that cannot be read back only costs a marshal and a
// write on every scan. It is a variable so tests can use small documents.
var maxCacheBytes int64 = 32 << 20

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
	// HasVCS is true when the directory directly contains VCS metadata
	// (.git as directory or file, .hg, .jj, .svn) or has the shape of a bare
	// git repository.
	HasVCS bool `json:"has_vcs,omitempty"`
	// Incomplete is true when the directory (or an entry of it) could not
	// be read, so the totals above undercount.
	Incomplete bool `json:"incomplete,omitempty"`
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
	if encodedSizeLowerBound(dirs) > maxCacheBytes {
		// The document is certainly too large: skip the marshal, which for
		// a huge tree costs more than the scan of an unchanged one.
		return dropOversizedCache(file)
	}
	data, err := marshalCache(cacheFile{Version: cacheVersion, Root: absPath, Dirs: dirs})
	if err != nil {
		return fmt.Errorf("walk: encode cache: %w", err)
	}
	if int64(len(data)) > maxCacheBytes {
		return dropOversizedCache(file)
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

// marshalCache is a seam so tests can prove that an oversized tree is never
// encoded.
var marshalCache = func(cf cacheFile) ([]byte, error) { return json.Marshal(cf) }

// minRecordJSON is the encoded size of the smallest possible record body; the
// real ones only add to it.
const minRecordJSON = len(`{"mtime_unix_nano":0,"id":"","racy":false,"direct_bytes":0,"direct_files":0,"direct_newest":0,"subdirs":null}`)

// encodedSizeLowerBound returns a size the encoded document cannot fall
// below: the fixed part of every record plus its key and subdirectory names.
// It is cheap (no allocation) and lets storeCache skip the marshal when the
// tree is far beyond maxCacheBytes.
func encodedSizeLowerBound(dirs map[string]*dirRecord) int64 {
	var n int64
	for rel, rec := range dirs {
		n += int64(len(rel)+minRecordJSON) + 4 // quoted key, colon, comma
		if rec == nil {
			continue
		}
		for _, name := range rec.Subdirs {
			n += int64(len(name)) + 3
		}
	}
	return n
}

// dropOversizedCache removes the stale file: loadCache would refuse a file of
// this size, and its records describe a tree that no longer fits.
func dropOversizedCache(file string) error {
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("walk: remove oversized cache %s: %w", file, err)
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
