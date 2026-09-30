package cli

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

func newPurgeCmd(a *app) *cobra.Command {
	var apply, yes bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Permanently delete quarantined sessions past their retention and stale scan caches",
		Long: `List the quarantined sessions (~/.brooom/quarantine/<session-id>) that are
older than trash.quarantine_retention_days and, with --apply, delete them
permanently. A retention of 0 means quarantined files never expire, so
nothing is listed. Only session directories are touched, never anything else
in the quarantine directory, the OS trash or the session manifests; the
manifests of purged sessions are marked as not restorable.

The same run also lists and removes stale directory size caches
(~/.brooom/cache/dirsize-v1-*.json): files unused for 30 days, files of
folders that no longer exist and leftovers of interrupted writes. The caches
are rebuilt by the next scan, so this frees disk space only.`,
		Example: `  brooom purge
  brooom purge --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPurge(cmd, apply, yes)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "delete the listed sessions and cache files (default is a dry run)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation (for scripts)")
	return cmd
}

// runPurge lists expired quarantine sessions and, with apply, deletes them
// after confirmation. A missing confirmation on a non-terminal stdin is a
// usage error before anything is deleted.
func (a *app) runPurge(cmd *cobra.Command, apply, yes bool) error {
	cfg, _, err := a.loadConfig()
	if err != nil {
		return err
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	days := cfg.Trash.QuarantineRetentionDays
	now := a.now()
	listing, err := trash.ListQuarantine(dirs.Quarantine, now, days)
	if err != nil {
		return err
	}
	// The cache listing is gathered first (printed after the sessions) so the
	// session header can say "nothing to purge" only when that is true for
	// both kinds of files.
	staleCache := a.findStaleCache(dirs, now)
	a.printPurgeListing(listing, days, now, len(staleCache) > 0)
	a.printStaleCache(staleCache)
	if len(listing.Expired) == 0 && len(staleCache) == 0 {
		return nil
	}
	if !apply {
		what := "them"
		if len(listing.Expired) == 0 {
			// Only caches are listed; do not talk about sessions.
			what = "the stale cache files"
		}
		fmt.Fprintf(a.io.Out, "dry run: nothing was deleted; re-run '%s' to delete %s permanently\n", a.applyCommand(cmd), what)
		return nil
	}
	if !yes {
		if !a.canPrompt() {
			return usageError{action.ErrConfirmationRequired}
		}
		prompt := fmt.Sprintf("Permanently delete %d quarantined session(s) and %d stale cache file(s)? [y/N] ",
			len(listing.Expired), len(staleCache))
		if !action.Confirm(a.io.In, a.io.Out, prompt) {
			fmt.Fprintln(a.io.Out, "aborted: nothing was deleted")
			return nil
		}
	}
	return errors.Join(a.applyPurge(dirs, listing, now), a.applyCachePrune(staleCache))
}

// findStaleCache lists the stale directory size caches. A cache that cannot be
// listed is a note, never an error: it is only a performance artefact.
func (a *app) findStaleCache(dirs config.Dirs, now time.Time) []walk.StaleCacheFile {
	stale, err := walk.ListStaleCache(dirs.Cache, walk.PruneOptions{Now: now, CheckRoots: true})
	if err != nil {
		fmt.Fprintf(a.io.Err, "brooom: cannot list the scan cache: %v\n", err)
		return nil
	}
	return stale
}

// printStaleCache prints the listing of stale caches.
func (a *app) printStaleCache(stale []walk.StaleCacheFile) {
	if len(stale) == 0 {
		return
	}
	var total int64
	fmt.Fprintln(a.io.Out, "stale scan cache files:")
	for _, f := range stale {
		total += f.SizeBytes
		fmt.Fprintf(a.io.Out, "  %s  %s  (%s)\n", filepath.Base(f.Path), output.FormatSize(f.SizeBytes), f.Reason)
	}
	fmt.Fprintf(a.io.Out, "total: %d cache file(s), %s\n", len(stale), output.FormatSize(total))
}

// applyCachePrune deletes the listed cache files and reports the result.
func (a *app) applyCachePrune(stale []walk.StaleCacheFile) error {
	if len(stale) == 0 {
		return nil
	}
	removed, err := walk.RemoveStaleCache(stale)
	var freed int64
	for _, f := range removed {
		freed += f.SizeBytes
	}
	fmt.Fprintf(a.io.Out, "removed %d cache file(s), freed %s\n", len(removed), output.FormatSize(freed))
	if err != nil {
		return fmt.Errorf("some cache files could not be deleted: %w", err)
	}
	return nil
}

// printPurgeListing prints the expired sessions with age and size and the
// total, or why there is nothing to list.
func (a *app) printPurgeListing(l *trash.QuarantineListing, days int, now time.Time, cacheStale bool) {
	for _, p := range l.Skipped {
		fmt.Fprintf(a.io.Err, "brooom: skipping %s: a symlink is never followed or deleted\n", output.Sanitize(p))
	}
	// With stale cache files listed below, "nothing to purge" would be false.
	nothing := "nothing to purge"
	if cacheStale {
		nothing = "no session to purge"
	}
	switch {
	case days == 0:
		fmt.Fprintf(a.io.Out, "quarantine retention is 0 (never expire); %s\n", nothing)
		return
	case len(l.Expired) == 0:
		fmt.Fprintf(a.io.Out, "%s: no quarantined session is older than %d days\n", nothing, days)
		return
	}
	fmt.Fprintf(a.io.Out, "quarantined sessions older than %d days:\n", days)
	for _, s := range l.Expired {
		fmt.Fprintf(a.io.Out, "  %s  %d days old  %s\n", output.Sanitize(s.ID), int(s.Age(now).Hours()/24), output.FormatSize(s.SizeBytes))
	}
	fmt.Fprintf(a.io.Out, "total: %d session(s), %s\n", len(l.Expired), output.FormatSize(l.TotalBytes()))
}

// applyPurge deletes the sessions, then marks the manifests of the purged ones
// so undo and sessions stay truthful. Failures are listed and make the exit
// code 1, after the summary was printed.
func (a *app) applyPurge(dirs config.Dirs, l *trash.QuarantineListing, now time.Time) error {
	var purged []string
	var freed int64
	failed := 0
	for _, r := range trash.Purge(dirs.Quarantine, l.Expired) {
		if r.Err != nil {
			failed++
			fmt.Fprintf(a.io.Err, "brooom: %s\n", output.Sanitize(r.Err.Error()))
			continue
		}
		purged = append(purged, r.Session.Dir)
		freed += r.Session.SizeBytes
	}
	fmt.Fprintf(a.io.Out, "purged %d session(s), freed %s\n", len(purged), output.FormatSize(freed))
	marked, err := session.NewStore(dirs.Sessions).MarkPurged(purged, now)
	if marked > 0 {
		fmt.Fprintf(a.io.Out, "marked %d session entries as not restorable\n", marked)
	}
	if err != nil {
		return err
	}
	if failed > 0 {
		return fmt.Errorf("%d session(s) could not be deleted", failed)
	}
	return nil
}
