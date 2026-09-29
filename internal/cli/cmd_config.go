package cli

import "github.com/spf13/cobra"

func newRootsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roots",
		Short: "Manage workspace roots used by --workspaces",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "add <path>...",
			Short: "Add workspace roots",
			Args:  cobra.MinimumNArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
		&cobra.Command{
			Use:   "remove <path>...",
			Short: "Remove workspace roots",
			Args:  cobra.MinimumNArgs(1),
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
		&cobra.Command{
			Use:   "list",
			Short: "List workspace roots",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
	)
	return cmd
}

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create, show, edit and validate the configuration",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "init",
			Short: "Write a config file with the defaults",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective configuration",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
		&cobra.Command{
			Use:   "edit",
			Short: "Open the config file in $VISUAL / $EDITOR",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Validate the config file",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return errNotImplemented },
		},
	)
	return cmd
}
