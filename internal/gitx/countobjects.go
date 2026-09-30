package gitx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// ObjectStats is the parsed output of `git count-objects -v`. All sizes are
// bytes: git reports KiB, which the parser converts.
type ObjectStats struct {
	// Count is the number of loose objects and Size their disk usage.
	Count int64
	Size  int64
	// InPack is the number of objects stored in packs.
	InPack int64
	// Packs is the number of pack files and SizePack their disk usage.
	Packs    int64
	SizePack int64
	// PrunePackable is the number of loose objects that also exist in a pack.
	PrunePackable int64
	// Garbage is the number of files in the object store git does not
	// recognise (for example leftovers of an interrupted repack) and
	// SizeGarbage their disk usage.
	Garbage     int64
	SizeGarbage int64
}

// ParseCountObjects parses `git count-objects -v` output. Unknown keys (such
// as "alternate") are ignored so newer git versions keep working; a value
// that is not a non-negative integer is an error naming the offending line.
func ParseCountObjects(out string) (ObjectStats, error) {
	var s ObjectStats
	for _, line := range Lines(out) {
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field, kib := s.field(strings.TrimSpace(key))
		if field == nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		if err != nil || n < 0 {
			return ObjectStats{}, fmt.Errorf("gitx: unexpected count-objects line %q", line)
		}
		if kib {
			n *= 1024
		}
		*field = n
	}
	return s, nil
}

// field maps a count-objects key to the struct field it fills and whether the
// value is a size in KiB.
func (s *ObjectStats) field(key string) (dst *int64, kib bool) {
	switch key {
	case "count":
		return &s.Count, false
	case "size":
		return &s.Size, true
	case "in-pack":
		return &s.InPack, false
	case "packs":
		return &s.Packs, false
	case "size-pack":
		return &s.SizePack, true
	case "prune-packable":
		return &s.PrunePackable, false
	case "garbage":
		return &s.Garbage, false
	case "size-garbage":
		return &s.SizeGarbage, true
	}
	return nil, false
}

// CountObjects runs `git count-objects -v` in dir. It is read-only and takes
// no locks.
func CountObjects(ctx context.Context, r Runner, dir string) (ObjectStats, error) {
	out, err := r.Run(ctx, dir, "count-objects", "-v")
	if err != nil {
		return ObjectStats{}, err
	}
	return ParseCountObjects(out)
}

// CountObjects returns the object statistics of the repository, memoized on
// handles from a Cache so linked worktrees of one repository share the result.
func (r *Repo) CountObjects(ctx context.Context) (ObjectStats, error) {
	return cached(r, &r.objectStats, struct{}{}, func() (ObjectStats, error) {
		return CountObjects(ctx, r.Runner, r.Dir)
	})
}

// Memo returns the result of f for key, computing it once per repository on
// handles from a Cache (uncached handles always call f). It lets detectors
// share their own expensive per-repository results, for example a history
// scan, between the worktree targets of one repository. Keys must be unique
// to the caller and the value type must be the same for every call of a key.
func (r *Repo) Memo(key string, f func() (any, error)) (any, error) {
	return cached(r, &r.extra, key, f)
}
