package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

func newEmptyTrashCmd(a *app) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "empty-trash",
		Short: "Permanently delete what brooom moved to the OS trash",
		Example: `  brooom empty-trash
  brooom empty-trash --dry-run`,
		Long: `List the items brooom moved to the OS trash that are still there (from the
session manifests), ask once and delete them permanently. Nothing else in the
trash is touched: an item is only deleted when its stored copy still lies
inside an OS trash directory (.Trash, the freedesktop Trash, $Recycle.Bin) and
is still of the recorded type (and, for a file, of the recorded size). Items
that changed are listed with the reason and kept.

The manifest entries of deleted items are marked as not restorable, so
'brooom undo' and 'brooom sessions' stay truthful.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runEmptyTrash(dryRun, yes)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "only list the items and delete nothing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation (for scripts)")
	return cmd
}

// trashedItem is one manifest entry whose copy is still in the OS trash.
type trashedItem struct {
	session string
	rec     trash.Record
	// refused is why the item is kept, "" when it may be deleted.
	refused string
}

func (a *app) runEmptyTrash(dryRun, yes bool) error {
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	store := session.NewStore(dirs.Sessions)
	items, err := trashedItems(store)
	if err != nil {
		return err
	}
	deletable := a.printTrashed(items)
	if len(deletable) == 0 {
		if len(items) == 0 {
			fmt.Fprintln(a.io.Out, "nothing in the trash from brooom")
		}
		return nil
	}
	if dryRun {
		fmt.Fprintln(a.io.Out, "dry run: nothing was deleted")
		return nil
	}
	if !yes {
		if !a.canPrompt() {
			return usageError{action.ErrConfirmationRequired}
		}
		prompt := fmt.Sprintf("Permanently delete %s (%s) from the trash? [y/N] ", countItems(len(deletable)), output.FormatSize(totalSize(deletable)))
		if !action.Confirm(a.io.In, a.io.Out, prompt) {
			fmt.Fprintln(a.io.Out, "nothing was deleted")
			return nil
		}
	}
	return a.deleteTrashed(store, deletable)
}

// trashedItems lists the applied, still restorable OS trash entries of every
// session whose stored copy still exists, each checked with VerifyStored.
// Entries whose copy is gone (the user emptied the trash) are left out: there
// is nothing to delete, and a listing changes no manifest.
func trashedItems(store *session.Store) ([]trashedItem, error) {
	list, _, err := store.List()
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []trashedItem
	for _, m := range list {
		for _, e := range m.Entries {
			if !stillInTrash(e) {
				continue
			}
			it := trashedItem{session: m.ID, rec: *e.Trash}
			if err := trash.VerifyStored(it.rec); err != nil {
				it.refused = err.Error()
			}
			out = append(out, it)
		}
	}
	return out, nil
}

// stillInTrash reports whether the entry moved something to the OS trash that
// was not restored and whose stored copy still exists.
func stillInTrash(e session.Entry) bool {
	if e.Status != session.StatusApplied || e.Trash == nil || e.Trash.Strategy != trash.StrategyTrash {
		return false
	}
	if !e.Trash.Restorable || e.Trash.StoredPath == "" {
		return false
	}
	_, err := os.Lstat(e.Trash.StoredPath)
	return err == nil
}

// printTrashed lists the items and returns those that may be deleted.
func (a *app) printTrashed(items []trashedItem) []trashedItem {
	var ok []trashedItem
	for _, it := range items {
		if it.refused != "" {
			continue
		}
		ok = append(ok, it)
		fmt.Fprintf(a.io.Out, "  %s  %s  (session %s)\n", output.Sanitize(it.rec.OriginalPath), output.FormatSize(it.rec.SizeBytes), output.Sanitize(it.session))
	}
	if len(ok) > 0 {
		fmt.Fprintf(a.io.Out, "total: %s, %s\n", countItems(len(ok)), output.FormatSize(totalSize(ok)))
	}
	var kept int
	for _, it := range items {
		if it.refused == "" {
			continue
		}
		if kept == 0 {
			fmt.Fprintln(a.io.Out, "kept:")
		}
		kept++
		fmt.Fprintf(a.io.Out, "  %s: %s\n", output.Sanitize(it.rec.OriginalPath), output.Sanitize(it.refused))
	}
	return ok
}

// deleteTrashed deletes the items, reports each failure and marks the
// manifests of what is gone.
func (a *app) deleteTrashed(store *session.Store, items []trashedItem) error {
	var gone []string
	var freed int64
	for _, it := range items {
		if err := trash.RemoveStored(it.rec); err != nil {
			fmt.Fprintf(a.io.Err, "brooom: %s\n", output.Sanitize(err.Error()))
			continue
		}
		gone = append(gone, it.rec.StoredPath)
		freed += it.rec.SizeBytes
	}
	fmt.Fprintf(a.io.Out, "deleted %s from the trash, freed %s\n", countItems(len(gone)), output.FormatSize(freed))
	marked, err := store.MarkTrashEmptied(gone, a.now())
	if marked > 0 {
		fmt.Fprintf(a.io.Out, "marked %d session entries as not restorable\n", marked)
	}
	if err != nil {
		return err
	}
	if failed := len(items) - len(gone); failed > 0 {
		return fmt.Errorf("%d item(s) could not be deleted", failed)
	}
	return nil
}

func totalSize(items []trashedItem) int64 {
	var n int64
	for _, it := range items {
		n += it.rec.SizeBytes
	}
	return n
}

func countItems(n int) string { return plural(n, "item") }
