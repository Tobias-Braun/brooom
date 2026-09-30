package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

func newPurgeCmd(a *app) *cobra.Command {
	var apply, yes bool
	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Permanently delete quarantined sessions past their retention",
		Long: `List the quarantined sessions (~/.brooom/quarantine/<session-id>) that are
older than trash.quarantine_retention_days and, with --apply, delete them
permanently. A retention of 0 means quarantined files never expire, so
nothing is listed. Only session directories are touched, never anything else
in the quarantine directory, the OS trash or the session manifests; the
manifests of purged sessions are marked as not restorable.`,
		Example: `  brooom purge
  brooom purge --apply`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runPurge(cmd, apply, yes)
		},
	}
	cmd.Flags().BoolVar(&apply, "apply", false, "delete the listed sessions (default is a dry run)")
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
	a.printPurgeListing(listing, days, now)
	if len(listing.Expired) == 0 {
		return nil
	}
	if !apply {
		fmt.Fprintf(a.io.Out, "dry run: nothing was deleted; re-run '%s' to delete them permanently\n", a.applyCommand(cmd))
		return nil
	}
	if !yes {
		if !a.canPrompt() {
			return usageError{action.ErrConfirmationRequired}
		}
		prompt := fmt.Sprintf("Permanently delete %d quarantined session(s)? [y/N] ", len(listing.Expired))
		if !action.Confirm(a.io.In, a.io.Out, prompt) {
			fmt.Fprintln(a.io.Out, "aborted: nothing was deleted")
			return nil
		}
	}
	return a.applyPurge(dirs, listing, now)
}

// printPurgeListing prints the expired sessions with age and size and the
// total, or why there is nothing to list.
func (a *app) printPurgeListing(l *trash.QuarantineListing, days int, now time.Time) {
	for _, p := range l.Skipped {
		fmt.Fprintf(a.io.Err, "brooom: skipping %s: a symlink is never followed or deleted\n", p)
	}
	switch {
	case days == 0:
		fmt.Fprintln(a.io.Out, "quarantine retention is 0 (never expire); nothing to purge")
		return
	case len(l.Expired) == 0:
		fmt.Fprintf(a.io.Out, "nothing to purge: no quarantined session is older than %d days\n", days)
		return
	}
	fmt.Fprintf(a.io.Out, "quarantined sessions older than %d days:\n", days)
	for _, s := range l.Expired {
		fmt.Fprintf(a.io.Out, "  %s  %d days old  %s\n", s.ID, int(s.Age(now).Hours()/24), output.FormatSize(s.SizeBytes))
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
			fmt.Fprintf(a.io.Err, "brooom: %v\n", r.Err)
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
