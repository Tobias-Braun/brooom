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
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/cli/progressui"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// Exit codes.
const (
	ExitOK    = 0
	ExitError = 1
	ExitUsage = 2
	// ExitScanFailed means nothing was scanned because of scan errors (for
	// example every repository was skipped), so an empty report is not a
	// clean bill of health. Partial failures keep exit 0 and report the
	// errors on the format's error channel.
	ExitScanFailed = 3
	// ExitDetectorFailed means the scan ran and its report was written, but
	// at least one detector failed on a target (findings.ScanError.Fatal), so
	// the report may be incomplete. Notes (skipped paths, incomplete checks)
	// keep exit 0.
	ExitDetectorFailed = 4
)

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
	// path is the folder or repository to work on instead of the working
	// directory. It is the positional argument of the bare command, scan,
	// sweep and git purge, and --path of undo and clean.
	path       string
	detectors  []string
	format     string
	quiet      bool
	noColor    bool
	verbose    bool
	progress   string
	configPath string
}

// applyFlags are shared by every command that can modify something. Such a
// command shows its plan, asks once and then acts; --dry-run stops after the
// plan and --yes skips the question.
type applyFlags struct {
	dryRun        bool
	yes           bool
	force         bool
	trashStrategy string
}

// apply reports whether the run acts on its plan (after the confirmation).
func (f applyFlags) apply() bool { return !f.dryRun }

// app carries state shared by all commands of one invocation.
type app struct {
	io    IO
	flags globalFlags
	// deprecationNoted is set once the deprecated config keys were reported,
	// so a run that loads the config several times says so once.
	deprecationNoted bool
	// args are the command-line arguments of this invocation (without the
	// program name); the session manifest records them.
	args []string
	// goos overrides runtime.GOOS for the dialect of quoted commands; only
	// tests set it, so both dialects are covered on every OS.
	goos string

	// postRunHooks run in order after a successful command. Append to it;
	// never assign a command's PersistentPostRun (see postRunHook).
	postRunHooks []postRunHook

	// update is the state of the opt-in background update check.
	update updateState

	// stdinTTY reports whether prompting is possible; nil means "io.In is a
	// terminal". Tests inject it to script confirmations.
	stdinTTY func() bool
	// stderrTTY is stdinTTY's counterpart for the live progress display; nil
	// means "io.Err is a terminal". Tests inject it to fake a terminal.
	stderrTTY func() bool
	// progressDecided is set by the first useProgress call; display is the
	// live progress display, nil when none runs (see progress.go).
	progressDecided bool
	display         *progressui.Display
	// clock returns the current time; nil means time.Now. Tests inject it to
	// age quarantined sessions.
	clock func() time.Time
}

// usageError marks errors caused by invalid invocation (exit code 2).
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

// scanFailedError marks a scan that covered no target because of scan errors
// (exit code 3). The report has already been written when it is returned.
type scanFailedError struct{ err error }

func (e scanFailedError) Error() string { return e.err.Error() }
func (e scanFailedError) Unwrap() error { return e.err }

// detectorFailedError marks a scan during which a detector failed (exit code
// 4). The report has already been written when it is returned.
type detectorFailedError struct{ err error }

func (e detectorFailedError) Error() string { return e.err.Error() }
func (e detectorFailedError) Unwrap() error { return e.err }

// listError is an error whose message is deliberately several lines (a
// heading followed by one indented line per problem). Its constructor must
// already have sanitised every untrusted part; renderError only keeps the
// line breaks of such errors.
type listError struct{ msg string }

func (e listError) Error() string { return e.msg }

// renderError returns the text printed after "brooom:". Every message is
// sanitised so a control character or newline in a quoted path cannot forge
// output lines. The one exception is the message of a listError or
// config.ValidationError, whose own newlines are kept and whose lines are
// sanitised one by one as a backstop; any wrapping prefix (for example the
// config file path) is still sanitised as a whole.
func renderError(err error) string {
	full := err.Error()
	inner := ""
	var le listError
	var ve *config.ValidationError
	switch {
	case errors.As(err, &le):
		inner = le.Error()
	case errors.As(err, &ve):
		inner = ve.Error()
	}
	prefix, ok := strings.CutSuffix(full, inner)
	if inner == "" || !ok {
		return output.Sanitize(full)
	}
	lines := strings.Split(inner, "\n")
	for i, l := range lines {
		lines[i] = output.Sanitize(l)
	}
	return output.Sanitize(prefix) + strings.Join(lines, "\n")
}

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
	// The live display must end before anything else is printed to stderr and
	// on every path, so the terminal is restored after errors and Ctrl-C too.
	a.stopProgress(err == nil)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(stdio.Err, "brooom:", renderError(err))
	var ue usageError
	var sf scanFailedError
	var df detectorFailedError
	switch {
	case errors.As(err, &ue):
		return ExitUsage
	case errors.As(err, &sf):
		return ExitScanFailed
	case errors.As(err, &df):
		return ExitDetectorFailed
	}
	return ExitError
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "brooom [path]",
		Short: "Sweep disk clutter from AI-assisted development",
		Example: `  brooom
  brooom ~/code
  brooom sweep
  brooom sweep after-agents
  brooom undo`,
		Long: `Brooom finds and safely cleans the clutter that heavy AI/agent-assisted
development leaves behind: agent run logs and runtime files, stale and merged
git branches, leftover worktrees, bloated git histories and build artifacts.

Safety first: every command that changes something shows its plan and asks
once before it acts (--dry-run only shows the plan, --yes skips the question),
removed files go to the trash by default, and every session can be undone.
Sweep never removes unmerged or uncommitted work.
Brooom never removes a directory that contains version control metadata (.git,
.hg, .jj, .svn) or a Windows junction, and the delete strategy is refused
outside a git repository and whenever git cannot confirm that a path holds no
untracked files.

Without a path Brooom only looks at the git repository you are in. Pass a
folder (brooom ~/code, brooom sweep tidy ~/code) to work on every repository
below it.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          rootPathArg,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if err := rejectIgnoredScanFlags(cmd); err != nil {
				return err
			}
			if _, err := parseProgressMode(a.flags.progress); err != nil {
				return err
			}
			a.startUpdateCheck(cmd)
			return nil
		},
		PersistentPostRun: a.runPostRunHooks,
		RunE: func(cmd *cobra.Command, args []string) error {
			a.setPath(args)
			return a.runScan(cmd, scanOptions{})
		},
	}
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return usageError{err}
	})
	pf := root.PersistentFlags()
	pf.StringSliceVarP(&a.flags.detectors, "detector", "d", nil, "run only these detectors (repeatable)")
	pf.StringVarP(&a.flags.format, "format", "f", "", "output format: table, tree, json, ndjson, plain, summary")
	pf.BoolVarP(&a.flags.quiet, "quiet", "q", false, "print only essential output")
	pf.BoolVar(&a.flags.noColor, "no-color", false, "disable colors (also honours NO_COLOR)")
	pf.BoolVarP(&a.flags.verbose, "verbose", "v", false, "print progress and diagnostics to stderr")
	pf.StringVar(&a.flags.progress, "progress", progressAuto, "live progress display on stderr: auto (terminals only), always, never")
	pf.StringVar(&a.flags.configPath, "config", "", "config file (default ~/.brooom/config.json)")

	a.postRunHooks = append(a.postRunHooks, a.finishUpdateCheck, a.retentionNotice)

	root.AddCommand(
		newScanCmd(a),
		newSweepCmd(a),
		newGitCmd(a),
		newCleanCmd(a),
		newUndoCmd(a),
		newSessionsCmd(a),
		newPurgeCmd(a),
		newConfigCmd(a),
		newVersionCmd(a),
		newUpdateCheckCmd(a),
	)
	root.SetHelpCommand(newHelpCmd())
	customizeCompletionCmd(root)
	markArgErrorsAsUsage(root)
	registerCompletions(root, a)
	return root
}

// markArgErrorsAsUsage wraps the positional-argument validator of every
// command so a wrong invocation (unknown subcommand, missing or extra
// argument) is a usage error with exit code 2, like a bad flag. Cobra reports
// these as plain errors, which would otherwise exit 1.
func markArgErrorsAsUsage(cmd *cobra.Command) {
	if validate := cmd.Args; validate != nil {
		cmd.Args = func(c *cobra.Command, args []string) error {
			if err := validate(c, args); err != nil {
				return usageError{err}
			}
			return nil
		}
	}
	for _, c := range cmd.Commands() {
		markArgErrorsAsUsage(c)
	}
}

// newHelpCmd replaces cobra's help command, which prints the root help and
// exits 0 for a topic that does not exist (`brooom help foo`). An unknown topic
// is a usage error like any other unknown command.
func newHelpCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "help [command]",
		Short:   "Help about any command",
		Example: "  brooom help sweep\n  brooom help config show",
		Long: `Print the help of any command.
An unknown command is a usage error (exit status 2).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			target, rest, err := cmd.Root().Find(args)
			if err != nil || target == nil || len(rest) > 0 {
				return usageError{fmt.Errorf("unknown help topic %q", strings.Join(args, " "))}
			}
			return target.Help()
		},
	}
}

// groupRunE is the RunE of commands that only group subcommands. Cobra treats
// a command without RunE as help-only and never validates its arguments, so
// `brooom config bogus` would print help and exit 0. With a RunE the NoArgs
// validator rejects the typo; without arguments the help is printed.
func groupRunE(cmd *cobra.Command, _ []string) error { return cmd.Help() }

// NewRootCommand returns a fresh command tree without any collaborators wired
// in. It exists for tooling that only inspects the tree (the CLI reference
// generator, tests); construction performs no I/O, so it is safe to call from
// a `go run` tool. Use Main to run the CLI.
func NewRootCommand() *cobra.Command {
	return newRootCmd(&app{})
}

// setPath takes the optional path argument of a command.
func (a *app) setPath(args []string) {
	if len(args) == 1 {
		a.flags.path = args[0]
	}
}

// addPathFlag registers --path for the commands whose positional argument is
// something else (undo takes a session id, clean reads --from).
func addPathFlag(cmd *cobra.Command, a *app) {
	cmd.Flags().StringVar(&a.flags.path, "path", "", "work on this folder or repository instead of the current one")
}

// rootPathArg accepts at most one argument for the bare command. A word that
// is neither an existing directory nor spelled like a path is a mistyped or
// removed subcommand (`brooom sweeep`, `brooom branches`), and saying so beats
// "no such directory".
func rootPathArg(cmd *cobra.Command, args []string) error {
	if err := cobra.MaximumNArgs(1)(cmd, args); err != nil {
		return err
	}
	if len(args) == 0 || looksLikePath(args[0]) {
		return nil
	}
	if fi, err := os.Stat(args[0]); err == nil && fi.IsDir() {
		return nil
	}
	return fmt.Errorf("unknown command %q for %q", args[0], cmd.CommandPath())
}

// looksLikePath reports whether s is spelled like a path rather than a
// command name: it has a separator or starts with "." or "~".
func looksLikePath(s string) bool {
	return strings.ContainsAny(s, `/\`) || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~")
}

// addApplyFlags registers the flags of commands that can modify things.
// sweep leaves out --force (see addForceFlag): it never acts on dirty or
// unmerged work.
func addApplyFlags(cmd *cobra.Command, f *applyFlags) {
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "only show what would be done and change nothing")
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation (for scripts)")
	cmd.Flags().StringVar(&f.trashStrategy, "trash-strategy", "", "override the trash strategy: trash, quarantine, delete (delete needs a git repository that shows no untracked files)")
}

// addForceFlag registers --force for the commands that may act on findings
// with overridable blocking risk flags.
func addForceFlag(cmd *cobra.Command, f *applyFlags) {
	cmd.Flags().BoolVar(&f.force, "force", false, "also act on findings with blocking risk flags (e.g. git branch -D)")
}
