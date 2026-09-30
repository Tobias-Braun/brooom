package action

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/procs"
)

// openBatch is the result of one open-file check for all trash targets of a
// plan. Without it every trash finding paid for its own check: a full /proc
// scan on Linux and a fresh lsof budget per directory on macOS.
type openBatch struct {
	// paths are the targets that were actually checked.
	paths []string
	set   map[string]bool
	res   map[string]bool
	// err is the (possibly nil) error of the shared check; it applies to
	// every path in it, exactly as it would to a single-path call.
	err error
}

type openBatchKey struct{}

// withOpenBatch stores b in ctx for checkOpen.
func withOpenBatch(ctx context.Context, b *openBatch) context.Context {
	return context.WithValue(ctx, openBatchKey{}, b)
}

func openBatchFrom(ctx context.Context) *openBatch {
	b, _ := ctx.Value(openBatchKey{}).(*openBatch)
	return b
}

// lookup answers checkOpen from the shared result. ok is false when the path
// was not part of the batch and needs a single-path check.
//
// A path below a checked directory was deliberately left out of the scan
// (dropCovered removes it from the plan anyway). If the directory has no open
// file below it, the inner path cannot have one either; if it does, the
// answer for the inner path is unknown and falls back to a single check.
func (b *openBatch) lookup(path string) (res map[string]bool, ok bool, err error) {
	if b.set[path] {
		return map[string]bool{path: b.res[path]}, true, b.err
	}
	for _, outer := range b.paths {
		if findings.IsWithin(outer, path) && !b.res[outer] {
			return map[string]bool{path: false}, true, b.err
		}
	}
	return nil, false, nil
}

// batchOpenCheck runs procs.OpenFiles once for every trash target of cands
// that survives the static checks Plan would apply first. Refused targets
// (outside scope, roots, .git, ...) and targets inside another target are
// left out, so the scan only covers what can actually be trashed. It returns
// ctx unchanged when there is nothing to check.
func (e *Executor) batchOpenCheck(ctx context.Context, cands []findings.Finding) context.Context {
	var paths []string
	for _, f := range cands {
		if f.SuggestedAction.Type != findings.ActionTrash {
			continue
		}
		path, err := resolveTarget(e.env, f.Path)
		if err != nil || refuseTarget(e.env, path) != nil {
			continue
		}
		paths = append(paths, path)
	}
	paths = outermostPaths(paths)
	if len(paths) < 2 {
		// A single target gains nothing from batching; Plan checks it itself.
		return ctx
	}
	checkCtx := ctx
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		checkCtx, cancel = context.WithTimeout(ctx, procs.Budget(len(paths)))
		defer cancel()
	}
	res, err := openFilesFn(checkCtx, paths)
	// The budget only bounds this call; the original ctx (with the caller's
	// cancellation) carries the result on to the per-finding checks.
	return withOpenBatch(ctx, &openBatch{paths: paths, set: setOf(paths), res: res, err: err})
}

func setOf(paths []string) map[string]bool {
	set := make(map[string]bool, len(paths))
	for _, p := range paths {
		set[p] = true
	}
	return set
}

// outermostPaths returns the deduplicated paths that do not lie inside
// another path of the list. Sorting with a trailing separator keeps every
// directory directly in front of its descendants ("a", "a-b", "a/c" would
// otherwise separate "a" from "a/c").
func outermostPaths(paths []string) []string {
	sep := string(filepath.Separator)
	slices.SortFunc(paths, func(a, b string) int { return strings.Compare(a+sep, b+sep) })
	paths = slices.Compact(paths)
	var out []string
	for _, p := range paths {
		if len(out) > 0 && findings.IsWithin(out[len(out)-1], p) {
			continue
		}
		out = append(out, p)
	}
	return out
}
