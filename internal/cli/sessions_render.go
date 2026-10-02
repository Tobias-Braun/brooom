package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// sessionsFormat validates the --format value of `brooom sessions`. Only
// table (also the empty default), plain, json and ndjson exist for manifests;
// tree and summary describe findings and have no meaning here.
func sessionsFormat(v string) (string, error) {
	switch v {
	case "", "table":
		return "table", nil
	case "json", "plain", "ndjson":
		return v, nil
	}
	return "", usageError{fmt.Errorf("unsupported format %q for sessions (supported: table, plain, json, ndjson)", v)}
}

func (a *app) runSessions() error {
	format, err := sessionsFormat(a.flags.format)
	if err != nil {
		return err
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	return a.listSessions(session.NewStore(dirs.Sessions), format)
}

func (a *app) listSessions(store *session.Store, format string) error {
	list, problems, err := store.List()
	if err != nil {
		return err
	}
	switch format {
	case "plain":
		for _, m := range list {
			fmt.Fprintln(a.io.Out, output.Sanitize(m.ID))
		}
		return nil
	case "ndjson":
		return writeNDJSON(a.io.Out, list)
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
	return renderSessionTable(a.io.Out, list)
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeNDJSON writes one compact JSON document per element of items.
func writeNDJSON[T any](w io.Writer, items []T) error {
	enc := json.NewEncoder(w)
	for _, it := range items {
		if err := enc.Encode(it); err != nil {
			return err
		}
	}
	return nil
}

// renderSessionTable prints one row per session. Manifests of earlier
// releases have no root and show "-".
func renderSessionTable(w io.Writer, list []*session.Manifest) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tROOT\tITEMS\tRECLAIMED")
	for _, m := range list {
		root := m.Root
		if root == "" {
			root = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", output.Sanitize(m.ID), output.Sanitize(root), m.Counts().Applied, output.FormatSize(m.ReclaimedBytes))
	}
	return tw.Flush()
}
