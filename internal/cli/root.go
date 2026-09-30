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
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	// args are the command-line arguments of this invocation (without the
	// program name); the session manifest records them.
	args []string

	// postRunHooks run in order after a successful command. Append to it;
	// never assign a command's PersistentPostRun (see postRunHook).
	postRunHooks []postRunHook

	// update is the state of the opt-in background update check.
	update updateState

	// stdinTTY reports whether prompting is possible; nil means "io.In is a
	// terminal". Tests inject it to script confirmations.
	stdinTTY func() bool
	// clock returns the current time; nil means time.Now. Tests inject it to
	// age quarantined sessions.
	clock func() time.Time
}

// usageError marks errors caused by invalid invocation (exit code 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// Main runs the CLI with args (without the program name) and returns the
// process exit code.
//
// The command runs with a context that is cancelled by Ctrl-C or SIGTERM, so
// a scan can stop early and still print its partial report, and an apply run
// stops after the current step with its manifest saved.
func Main(args []string, stdio IO) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return executeContext(ctx, &app{io: stdio}, args)
}

// execute runs the command tree of a. Tests build the app themselves to
// inject collaborators before calling it.
func execute(a *app, args []string) int {
	return executeContext(context.Background(), a, args)
}

// executeContext is execute with a caller-supplied context, which lets tests
// cancel a run without sending real signals.
func executeContext(ctx context.Context, a *app, args []string) int {
	stdio := a.io
	a.args = args
	root := newRootCmd(a)
	root.SetArgs(args)
	root.SetIn(stdio.In)
	root.SetOut(stdio.Out)
	root.SetErr(stdio.Err)
	err := root.ExecuteContext(ctx)
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
		Example: `  brooom
  brooom --workspaces --format json
  brooom sweep --apply
  brooom undo`,
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
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			a.startUpdateCheck(cmd)
		},
		PersistentPostRun: a.runPostRunHooks,
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

	a.postRunHooks = append(a.postRunHooks, a.finishUpdateCheck, a.retentionNotice)

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
	customizeCompletionCmd(root)
	registerCompletions(root, a)
	return root
}

// NewRootCommand returns a fresh command tree without any collaborators wired
// in. It exists for tooling that only inspects the tree (the CLI reference
// generator, tests); construction performs no I/O, so it is safe to call from
// a `go run` tool. Use Main to run the CLI.
func NewRootCommand() *cobra.Command {
	return newRootCmd(&app{})
}

// addApplyFlags registers the flags of commands that can modify things.
func addApplyFlags(cmd *cobra.Command, f *applyFlags) {
	cmd.Flags().BoolVar(&f.apply, "apply", false, "execute the plan (default is a dry run)")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation (for scripts)")
	cmd.Flags().BoolVar(&f.force, "force", false, "also act on findings with blocking risk flags (e.g. git branch -D)")
	cmd.Flags().StringVar(&f.trashStrategy, "trash-strategy", "", "override the trash strategy: trash, quarantine, delete")
}
