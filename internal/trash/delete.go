package trash

import (
	"context"
	"fmt"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// deleter removes items permanently. It is the explicit opt-in strategy:
// nothing it removes can be restored, and its records say so.
type deleter struct {
	now func() time.Time
}

// newDeleter returns the trasher that deletes items permanently.
func newDeleter(Options) (Trasher, error) {
	return &deleter{now: time.Now}, nil
}

// Strategy implements Trasher.
func (d *deleter) Strategy() config.TrashStrategy { return config.StrategyDelete }

// Remove implements Trasher. Symlinks are removed as links and directories
// without following links inside them.
func (d *deleter) Remove(ctx context.Context, path string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	fi, err := checkRemovable(path)
	if err != nil {
		return Record{}, err
	}
	size, err := treeSize(path)
	if err != nil {
		return Record{}, fmt.Errorf("cannot measure %q: %w", path, err)
	}
	if err := removeTree(path); err != nil {
		return Record{}, fmt.Errorf("cannot delete %q: %w", path, err)
	}
	return Record{
		Strategy:     config.StrategyDelete,
		OriginalPath: path,
		SizeBytes:    size,
		IsDir:        fi.IsDir() && !isSymlink(fi),
		RemovedAt:    d.now().UTC(),
	}, nil
}

// Restore implements Trasher: permanent deletion cannot be undone.
func (d *deleter) Restore(_ context.Context, r Record) error {
	return fmt.Errorf("%q was deleted permanently: %w", r.OriginalPath, ErrNotRestorable)
}
