package procs

import (
	"context"
	"errors"
	"strings"
	"sync"
)

// Listing loads the name of every open file of the system once. It is the
// mechanism behind a Snapshot and is only available where one listing is much
// cheaper than a query per path (macOS lsof).
type Listing func(ctx context.Context) ([]string, error)

// Snapshot answers open-file questions for a whole scan from one memoised
// listing instead of one lsof run per target. Detectors of a scan share it
// through detect.Env; concurrent use is safe.
//
// It is a conservative pre-filter, not a lock: the listing is taken at the
// first query, so a file opened later is not seen. Whatever acts on a
// finding (the executor's Plan-time check) still asks the live system for
// its own target. Where the platform has no cheap listing, or the listing is
// unavailable, OpenAmong falls back to OpenFiles per call, so the answer is
// never less complete than without a snapshot.
type Snapshot struct {
	load Listing

	mu     sync.Mutex
	loaded bool
	idx    *nameIndex
	err    error
}

// NewSnapshot returns a Snapshot backed by the platform listing. On platforms
// without one every query is answered by OpenFiles.
func NewSnapshot() *Snapshot { return &Snapshot{load: platformListing} }

// NewSnapshotFrom returns a Snapshot backed by the given listing; tests use
// it to count loads and to simulate open files.
func NewSnapshotFrom(load Listing) *Snapshot { return &Snapshot{load: load} }

// OpenAmong has the contract of OpenFiles: a key for every valid input path,
// true when a process has the file open (or, for a directory, anything below
// it or the directory itself), and a non-nil error meaning "unknown" for the
// entries that are false. Files are matched exactly, directories by prefix.
func (s *Snapshot) OpenAmong(ctx context.Context, paths []string) (map[string]bool, error) {
	if s == nil || s.load == nil {
		return OpenFiles(ctx, paths)
	}
	idx, err := s.listing(ctx, len(paths))
	if errors.Is(err, ErrUnavailable) {
		return OpenFiles(ctx, paths)
	}
	files, dirs, res, unchecked, cerr := classify(ctx, paths)
	if cerr != nil {
		return nil, cerr
	}
	if idx != nil {
		idx.mark(files, dirs, res)
	}
	if err == nil && unchecked {
		err = ErrIncomplete
	}
	return res, err
}

// listing returns the memoised index, loading it on first use. A load that
// ended because the caller's context did is not remembered, so one cancelled
// query cannot poison the rest of the scan. Names read before a timeout are
// kept: they are true positives, and the error stays attached.
func (s *Snapshot) listing(ctx context.Context, n int) (*nameIndex, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded {
		return s.idx, s.err
	}
	parent := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, Budget(n))
		defer cancel()
	}
	names, err := s.load(ctx)
	err = normalizeErr(ctx, err)
	if names != nil {
		s.idx = newNameIndex(names)
	}
	s.err = err
	// Only the caller's own cancellation is not remembered; a listing that
	// ran into the time budget is, otherwise every later query would pay
	// the same slow listing again.
	s.loaded = parent.Err() == nil
	return s.idx, err
}

// nameIndex holds the listed names for matching.
type nameIndex struct {
	names []string
	// exact and folded serve file lookups: exact as printed, folded
	// lower-cased for case-insensitive volumes.
	exact  map[string]struct{}
	folded map[string]struct{}
}

func newNameIndex(names []string) *nameIndex {
	ix := &nameIndex{names: names, exact: make(map[string]struct{}, len(names)), folded: make(map[string]struct{}, len(names))}
	for _, n := range names {
		ix.exact[n] = struct{}{}
		ix.folded[strings.ToLower(n)] = struct{}{}
	}
	return ix
}

// mark sets res for every file and directory the listing reports.
func (ix *nameIndex) mark(files, dirs []string, res map[string]bool) {
	for _, f := range files {
		for _, sp := range lsofSpellings(f) {
			_, exact := ix.exact[sp]
			_, folded := ix.folded[strings.ToLower(sp)]
			if exact || folded {
				res[f] = true
				break
			}
		}
	}
	for _, d := range dirs {
		if anyBelow(ix.names, dirPrefix(d)) {
			res[d] = true
		}
	}
}
