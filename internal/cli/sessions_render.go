package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// sessionsFormat validates the --format value of `brooom sessions`. Only
// table (also the empty default) and json exist for manifests.
func sessionsFormat(v string) (string, error) {
	switch v {
	case "", "table":
		return "table", nil
	case "json":
		return "json", nil
	}
	return "", usageError{fmt.Errorf("unsupported format %q for sessions (supported: table, json)", v)}
}

func (a *app) runSessions(args []string) error {
	format, err := sessionsFormat(a.flags.format)
	if err != nil {
		return err
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	store := session.NewStore(dirs.Sessions)
	if len(args) == 1 {
		return a.showSession(store, args[0], format)
	}
	return a.listSessions(store, format)
}

func (a *app) listSessions(store *session.Store, format string) error {
	list, problems, err := store.List()
	if err != nil {
		return err
	}
	if format == "json" {
		if list == nil {
			list = []*session.Manifest{}
		}
		if problems == nil {
			problems = []session.Problem{}
		}
		return writeJSON(a.io.Out, map[string]any{"sessions": list, "problems": problems})
	}
	for _, p := range problems {
		fmt.Fprintf(a.io.Err, "warning: skipping %s: %v\n", output.Sanitize(p.File), output.Sanitize(p.Err.Error()))
	}
	if len(list) == 0 {
		_, err := fmt.Fprintln(a.io.Out, "No sessions yet.")
		return err
	}
	return renderSessionTable(a.io.Out, list, time.Now())
}

func (a *app) showSession(store *session.Store, id, format string) error {
	m, err := store.Load(id)
	if err != nil {
		return err
	}
	if format == "json" {
		return writeJSON(a.io.Out, m)
	}
	return renderSessionDetail(a.io.Out, m, time.Now())
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func renderSessionTable(w io.Writer, list []*session.Manifest, now time.Time) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTARTED\tCOMMAND\tAPPLIED\tFAILED\tRECLAIMED\tRESTORABLE")
	for _, m := range list {
		c := m.Counts()
		cmdText := m.Command
		if m.FinishedAt.IsZero() {
			cmdText += " (unfinished)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%s\t%d\n", output.Sanitize(m.ID), formatStarted(m.StartedAt, now),
			output.Sanitize(cmdText), c.Applied, c.Failed, output.FormatSize(m.ReclaimedBytes), c.Restorable)
	}
	return tw.Flush()
}

func renderSessionDetail(w io.Writer, m *session.Manifest, now time.Time) error {
	fmt.Fprintf(w, "Session:   %s\n", output.Sanitize(m.ID))
	fmt.Fprintf(w, "Started:   %s\n", formatStarted(m.StartedAt, now))
	if m.FinishedAt.IsZero() {
		fmt.Fprintln(w, "Finished:  (unfinished)")
	} else {
		fmt.Fprintf(w, "Finished:  %s\n", m.FinishedAt.Local().Format("2006-01-02 15:04:05"))
	}
	fmt.Fprintf(w, "Command:   %s\n", output.Sanitize(m.Command))
	fmt.Fprintf(w, "Reclaimed: %s\n", output.FormatSize(m.ReclaimedBytes))
	if len(m.Entries) == 0 {
		_, err := fmt.Fprintln(w, "\nNo entries.")
		return err
	}
	fmt.Fprintf(w, "\nEntries (%d):\n", len(m.Entries))
	for i, e := range m.Entries {
		writeEntry(w, i+1, e)
	}
	return nil
}

func writeEntry(w io.Writer, n int, e session.Entry) {
	fmt.Fprintf(w, "\n%d. [%s] %s  %s\n", n, output.Sanitize(string(e.Status)), output.Sanitize(string(e.Action)), output.Sanitize(e.Path))
	fmt.Fprintf(w, "   size:       %s\n", output.FormatSize(e.SizeBytes))
	fmt.Fprintf(w, "   restorable: %t\n", e.Restorable)
	if e.Error != "" {
		fmt.Fprintf(w, "   error:      %s\n", output.Sanitize(e.Error))
	}
	if e.Trash != nil {
		fmt.Fprintf(w, "   trash:      strategy=%s stored=%s\n", output.Sanitize(string(e.Trash.Strategy)), output.Sanitize(e.Trash.StoredPath))
	}
	if e.RecoveryHint != "" {
		fmt.Fprintf(w, "   recovery:   %s\n", output.Sanitize(e.RecoveryHint))
	}
}

// formatStarted renders local time plus a relative age, e.g.
// "2026-09-29 22:45 (3h ago)".
func formatStarted(t, now time.Time) string {
	return fmt.Sprintf("%s (%s)", t.Local().Format("2006-01-02 15:04"), relativeAge(t, now))
}

func relativeAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "in the future"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
