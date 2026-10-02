package testutil

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// DirTrasher stands in for the OS trash in tests: it moves every item into a
// fresh directory below Dir and back, so no test touches the real trash.
// Records name the OS trash strategy, so undo treats them like real ones, and
// any DirTrasher restores what another one stored.
type DirTrasher struct{ Dir string }

// Remove implements trash.Trasher. The size is the planned one when the
// context carries it, like the real trashers report it.
func (d DirTrasher) Remove(ctx context.Context, path string) (trash.Record, error) {
	size, ok := trash.SizeHintFrom(ctx, path)
	if !ok {
		s, err := walk.DirSize(ctx, path, walk.Options{Fresh: true})
		if err != nil {
			return trash.Record{}, err
		}
		size = s.SizeBytes
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return trash.Record{}, err
	}
	if err := os.MkdirAll(d.Dir, 0o700); err != nil {
		return trash.Record{}, err
	}
	slot, err := os.MkdirTemp(d.Dir, "item-")
	if err != nil {
		return trash.Record{}, err
	}
	stored := filepath.Join(slot, filepath.Base(path))
	if err := os.Rename(path, stored); err != nil {
		return trash.Record{}, err
	}
	return trash.Record{
		Strategy: trash.StrategyTrash, OriginalPath: path, StoredPath: stored,
		SizeBytes: size, IsDir: fi.IsDir(), RemovedAt: time.Now().UTC(), Restorable: true,
	}, nil
}

// Restore implements trash.Trasher.
func (DirTrasher) Restore(_ context.Context, r trash.Record) error {
	if _, err := os.Lstat(r.OriginalPath); err == nil {
		return trash.ErrRestoreConflict
	}
	if _, err := os.Lstat(r.StoredPath); errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s is gone", trash.ErrNotRestorable, r.StoredPath)
	}
	if err := os.MkdirAll(filepath.Dir(r.OriginalPath), 0o755); err != nil {
		return err
	}
	return os.Rename(r.StoredPath, r.OriginalPath)
}
