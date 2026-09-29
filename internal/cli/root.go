// Package cli implements the brooom command-line interface on top of the
// core packages. It contains no detection or cleanup logic itself: commands
// parse flags, build the scan/action environment, call into detect, action,
// session and output, and map errors to exit codes.
//
// Every subcommand lives in its own file (cmd_<name>.go) and is added to the
// tree in newRootCmd, so feature work on one command does not touch the
// others.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
)

// errNotImplemented is returned by commands whose milestone is not done yet.
var errNotImplemented = errors.New("not implemented yet")

// IO bundles the standard streams so commands are testable.
type IO struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// StdIO returns the process's standard streams.
func StdIO() IO {
	return IO{In: os.Stdin, Out: os.Stdout, Err: os.Stderr}
}

// globalFlags are the flags shared by every command.
type globalFlags struct {
	workspaces bool
	roots      []string
	detectors  []string
	format     string
	quiet      bool
	noColor    bool
	verbose    bool
	configPath string
}

// applyFlags are shared by every command that can modify something.
type applyFlags struct {
	apply         bool
	yes           bool
	force         bool
	trashStrategy string
}

// app carries state shared by all commands of one invocation.
type app struct {
	io    IO
	flags globalFlags
}

// usageError marks errors caused by invalid invocation (exit code 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Main runs the CLI with args (without the program name) and returns the
// process exit code.
func Main(args []string, stdio IO) int {
	a := &app{io: stdio}
	root := newRootCmd(a)
	root.SetArgs(args)
	root.SetIn(stdio.In)
	root.SetOut(stdio.Out)
	root.SetErr(stdio.Err)
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(stdio.Err, "brooom:", err)
	var ue usageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	return ExitError
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "brooom",
		Short: "Sweep disk clutter from AI-assisted development",
		Long: `Brooom finds and safely cleans the clutter that heavy AI/agent-assisted
development leaves behind: agent run logs and runtime files, stale and merged
git branches, leftover worktrees, bloated git histories and build artifacts.

Safety first: every command is a dry run unless you pass --apply, removed
files go to the trash by default, and every applied session can be undone.

Without flags Brooom only looks at the git repository you are in. Use
--workspaces to scan every repository below your configured roots.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runScan(cmd, scanOptions{})
		},
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError{err}
	})
	pf := root.PersistentFlags()
	pf.BoolVarP(&a.flags.workspaces, "workspaces", "w", false, "scan all configured workspace roots instead of the current repo")
	pf.StringSliceVar(&a.flags.roots, "root", nil, "limit --workspaces to these roots (repeatable)")
	pf.StringSliceVarP(&a.flags.detectors, "detector", "d", nil, "run only these detectors (repeatable)")
	pf.StringVarP(&a.flags.format, "format", "f", "", "output format: table, tree, json, ndjson, plain, summary")
	pf.BoolVarP(&a.flags.quiet, "quiet", "q", false, "print only essential output")
	pf.BoolVar(&a.flags.noColor, "no-color", false, "disable colors (also honours NO_COLOR)")
	pf.BoolVarP(&a.flags.verbose, "verbose", "v", false, "print progress and diagnostics to stderr")
	pf.StringVar(&a.flags.configPath, "config", "", "config file (default ~/.brooom/config.json)")

	root.AddCommand(
		newScanCmd(a),
		newSweepCmd(a),
		newBranchesCmd(a),
		newWorktreesCmd(a),
		newGitCmd(a),
		newLogsCmd(a),
		newArtifactsCmd(a),
		newAICmd(a),
		newCleanCmd(a),
		newUndoCmd(a),
		newSessionsCmd(a),
		newPurgeCmd(a),
		newRootsCmd(a),
		newConfigCmd(a),
		newVersionCmd(a),
		newUpdateCheckCmd(a),
	)
	return root
}

// addApplyFlags registers the flags of commands that can modify things.
func addApplyFlags(cmd *cobra.Command, f *applyFlags) {
	cmd.Flags().BoolVar(&f.apply, "apply", false, "execute the plan (default is a dry run)")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation (for scripts)")
	cmd.Flags().BoolVar(&f.force, "force", false, "also act on findings with blocking risk flags (e.g. git branch -D)")
	cmd.Flags().StringVar(&f.trashStrategy, "trash-strategy", "", "override the trash strategy: trash, quarantine, delete")
}
