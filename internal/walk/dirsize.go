package walk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// racyWindow is how close to the scan start a directory mtime may be before
// its record is considered unreliable (coarse filesystem timestamps).
const racyWindow = 2 * time.Second

// now and lstat are seams for tests.
var (
	now   = time.Now
	lstat = os.Lstat
)

// DirSize computes the summary of the directory tree at path. Symlinks are
// not followed and count as their own size; hard links are counted once.
// A regular file or symlink at path yields its own size with Files = 1 and a
// nonexistent path is an error.
//
// With Options.CacheDir set, per-directory records are reused when the
// directory's mtime and identity are unchanged, so an unchanged tree costs
// one Lstat per directory and no directory read. NewestModTime from a cached
// (non-Fresh) DirSize is a lower-bound hint. Callers that use it for age
// thresholds, recently_modified or LastModified of paths whose files may be
// modified in place (logs, transcripts, caches, ignored data) must pass
// Fresh: true. Sizing and ranking may use the cache. File sizes inside
// directories whose mtime did not change may likewise be stale (in-place
// appends do not change the directory mtime), so cached SizeBytes is
// suitable for sizing and ranking only.
func DirSize(ctx context.Context, path string, opts Options) (DirSummary, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return DirSummary{}, fmt.Errorf("walk: %s: %w", path, err)
	}
	fi, err := lstat(abs)
	if err != nil {
		return DirSummary{}, fmt.Errorf("walk: %s: %w", abs, err)
	}
	if !fi.IsDir() {
		e := entryFromInfo(abs, fi)
		size := e.Allocated
		if e.IsSymlink() {
			size = e.Size
		}
		return DirSummary{SizeBytes: size, Files: 1, NewestModTime: e.ModTime}, nil
	}

	s := &sizer{root: abs, opts: opts, start: now(), recs: map[string]*dirRecord{}}
	var cfile string
	if opts.CacheDir != "" {
		cfile = cacheFilePath(opts.CacheDir, abs)
		if !opts.Fresh {
			s.old = loadCache(cfile, abs)
		}
	}
	if err := runPool(ctx, opts.workers(), "", s.process); err != nil {
		return DirSummary{}, err
	}
	if cfile != "" {
		// Best effort by design: a failed write must never fail a scan.
		_ = storeCache(cfile, abs, s.recs)
	}
	return s.aggregate(), nil
}

// sizer holds the state shared by the workers of one DirSize call.
type sizer struct {
	root  string
	opts  Options
	start time.Time
	old   map[string]*dirRecord // records loaded from the cache (read only)

	mu   sync.Mutex
	recs map[string]*dirRecord // records of every directory visited
}

// process handles one directory: it either reuses its cached record after a
// single Lstat or re-reads the directory, then queues the subdirectories.
func (s *sizer) process(rel string, submit func(string)) {
	path := s.root
	if rel != "" {
		path = filepath.Join(s.root, filepath.FromSlash(rel))
	}
	// The Lstat comes before any read: a change during the read then leaves
	// a recorded mtime older than the directory, forcing a re-read later.
	fi, err := lstat(path)
	if err != nil || !fi.IsDir() {
		return // vanished or replaced: its record is dropped
	}
	id := fileIDOf(fi).String()
	mtime := fi.ModTime().UnixNano()

	rec := s.old[rel]
	if rec == nil || rec.Racy || rec.MtimeUnixNano != mtime || rec.ID != id {
		rec = s.scan(path, mtime, id)
	}
	s.mu.Lock()
	s.recs[rel] = rec
	s.mu.Unlock()
	for _, name := range rec.Subdirs {
		submit(joinRel(rel, name))
	}
}

// scan reads one directory into a fresh record.
func (s *sizer) scan(path string, mtime int64, id string) *dirRecord {
	rec := &dirRecord{
		MtimeUnixNano: mtime,
		ID:            id,
		Racy:          mtime >= s.start.Add(-racyWindow).UnixNano(),
		Subdirs:       []string{},
	}
	entries, err := readDir(path, func(string, error) {})
	if err != nil {
		rec.Racy = true // partial listing: never trust it next time
	}
	for _, e := range entries {
		rec.add(e)
	}
	return rec
}

// add folds one directory entry into the record.
func (r *dirRecord) add(e Entry) {
	if e.IsDir() {
		r.Subdirs = append(r.Subdirs, e.Name)
		return
	}
	if t := e.ModTime.UnixNano(); t > r.DirectNewest {
		r.DirectNewest = t
	}
	switch {
	case e.IsSymlink():
		r.DirectBytes += e.Size
	case e.Type.IsRegular() && e.fid.ok && e.fid.nlink > 1:
		r.Links = append(r.Links, linkRecord{ID: e.fid.String(), Bytes: e.Allocated})
	case e.Type.IsRegular():
		r.DirectBytes += e.Allocated
		r.DirectFiles++
	}
}

// aggregate sums all visited records, deduplicating hard links across the
// whole tree.
func (s *sizer) aggregate() DirSummary {
	var sum DirSummary
	var newest int64
	seen := map[string]struct{}{}
	for rel, rec := range s.recs {
		sum.SizeBytes += rec.DirectBytes
		sum.Files += rec.DirectFiles
		newest = max(newest, rec.DirectNewest)
		if rel != "" {
			// A directory's own mtime counts, except the queried root's.
			newest = max(newest, rec.MtimeUnixNano)
		}
		for _, l := range rec.Links {
			if _, dup := seen[l.ID]; dup {
				continue
			}
			seen[l.ID] = struct{}{}
			sum.SizeBytes += l.Bytes
			sum.Files++
		}
	}
	if newest != 0 {
		sum.NewestModTime = time.Unix(0, newest)
	}
	return sum
}
