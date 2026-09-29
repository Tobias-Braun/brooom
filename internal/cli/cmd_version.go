package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/buildinfo"
)

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			info := buildinfo.Get()
			switch a.flags.format {
			case "json":
				enc := json.NewEncoder(a.io.Out)
				enc.SetIndent("", "  ")
				return enc.Encode(info)
			case "", "table", "plain":
				if a.flags.quiet || a.flags.format == "plain" {
					_, err := fmt.Fprintln(a.io.Out, info.Version)
					return err
				}
				_, err := fmt.Fprintf(a.io.Out, "brooom %s (commit %s, built %s, %s, %s)\n",
					info.Version, info.Commit, info.Date, info.GoVersion, info.Platform)
				return err
			default:
				return usageError{fmt.Errorf("version supports --format table, plain or json, not %q", a.flags.format)}
			}
		},
	}
}

func newUpdateCheckCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "update-check",
		Short: "Check GitHub for a newer release (opt-in, contacts api.github.com)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return errNotImplemented
		},
	}
}
