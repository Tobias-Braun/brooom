package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// skipsRetentionNotice lists the commands that never print the notice: purge
// and undo deal with the quarantine themselves, and version, completion and
// help must stay instant and clean.
func skipsRetentionNotice(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "purge", "undo", "version", "completion", "__complete", "__completeNoDesc", "help":
			return true
		}
	}
	return false
}

// retentionNotice is a post-run hook that tells the user, on stderr, that
// quarantined sessions are past their retention. It is silent for machine
// formats and --quiet (scripts), never touches stdout so piped output stays
// intact, costs a directory listing plus one small manifest read per session,
// and swallows every error: a notice must never fail or slow a command.
func (a *app) retentionNotice(cmd *cobra.Command, _ []string) {
	if skipsRetentionNotice(cmd) || a.flags.quiet {
		return
	}
	cfg, _, err := a.loadConfig()
	if err != nil || cfg.Trash.QuarantineRetentionDays <= 0 {
		return
	}
	if format, err := a.renderedFormat(cmd, cfg.Output.Format); err != nil || machineFormats[format] {
		return
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return
	}
	l, err := trash.ListQuarantine(dirs.Quarantine, a.now(), cfg.Trash.QuarantineRetentionDays)
	if err != nil || len(l.Expired) == 0 {
		return
	}
	fmt.Fprintln(a.io.Err, retentionMessage(len(l.Expired), l.TotalBytes(), cfg.Trash.QuarantineRetentionDays))
}

// renderedFormat is the format cmd will actually print in, given the config's
// output.format. Deciding it from the flag and the config for every command
// is wrong both ways: `sessions` or `version` never read the config format
// (their table was printed, yet the notice was suppressed), and an acting run
// (--apply, git purge operations) prints its plan as text even when the config
// asks for a machine format. Only the commands that render findings honour
// the config, dry runs with resolveFormat and acting runs with
// resolveActingFormat; `clean` never renders in a format. The result may be
// empty, which means the default table.
func (a *app) renderedFormat(cmd *cobra.Command, cfgFormat string) (string, error) {
	path := cmd.CommandPath()
	if !scopeCommands[path] || noFormatCommands[path] {
		return a.flags.format, nil
	}
	if actsThisRun(cmd) {
		return resolveActingFormat(a.flags.format, cfgFormat)
	}
	return resolveFormat(a.flags.format, cfgFormat)
}

// actsThisRun reports whether cmd executes changes or a purge operation, whose
// plan is human text.
func actsThisRun(cmd *cobra.Command) bool {
	if f := cmd.Flags().Lookup("apply"); f != nil && f.Value.String() == "true" {
		return true
	}
	if cmd.CommandPath() != "brooom git purge" {
		return false
	}
	for _, name := range []string{"gc", "reflog-expire", "prune"} {
		if f := cmd.Flags().Lookup(name); f != nil && f.Changed {
			return true
		}
	}
	return false
}

// retentionMessage is the one-line notice.
func retentionMessage(n int, bytes int64, days int) string {
	noun, verb := "sessions", "are"
	if n == 1 {
		noun, verb = "session", "is"
	}
	return fmt.Sprintf("brooom: %d quarantined %s (%s) %s past the %d-day retention, run 'brooom purge' to free the space",
		n, noun, output.FormatSize(bytes), verb, days)
}
