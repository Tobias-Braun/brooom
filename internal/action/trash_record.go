package action

import (
	"fmt"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/trash"
)

// checkRecordPaths validates the source side of a trash record independently
// of the scope guard, which only ever vets the destination. A manifest is a
// file others can edit, so StoredPath and InfoPath must be clean absolute
// paths (no relative spelling or ".." that a trasher could resolve against
// the working directory), must not sit inside VCS metadata, and the stored
// copy must be unrelated to the destination: restoring an item into itself
// or over its own ancestor is never a legitimate undo. Which locations are
// genuine trash directories, and that none of their components is a symlink,
// is the trasher's job (Trasher.Restore), because only it knows the layout.
// The VCS-component test is a manifest sanity check, not a security boundary:
// no genuine trash directory contains such a component, so a hit means the
// manifest was edited or corrupted.
func checkRecordPaths(rec trash.Record) error {
	if rec.StoredPath == "" {
		return nil // nothing is moved; the trasher reports a missing copy
	}
	paths := []struct{ what, path string }{{"stored", rec.StoredPath}}
	if rec.InfoPath != "" {
		paths = append(paths, struct{ what, path string }{"info", rec.InfoPath})
	}
	for _, p := range paths {
		if !filepath.IsAbs(p.path) || filepath.Clean(p.path) != p.path {
			return fmt.Errorf("undo: refusing to restore: %s path %q is not a clean absolute path", p.what, p.path)
		}
		if insideVCSDir(p.path) {
			return fmt.Errorf("undo: refusing to restore: %s path %s is inside VCS metadata (manifest sanity check: brooom never trashes into such a path)", p.what, p.path)
		}
	}
	if covers(rec.StoredPath, rec.OriginalPath) || covers(rec.OriginalPath, rec.StoredPath) {
		return fmt.Errorf("undo: refusing to restore: stored path %s and destination %s overlap", rec.StoredPath, rec.OriginalPath)
	}
	return nil
}
