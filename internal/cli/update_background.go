package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/buildinfo"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/updatecheck"
)

// NoUpdateCheckEnv disables the background check regardless of the config.
const NoUpdateCheckEnv = "BROOOM_NO_UPDATE_CHECK"

// defaultUpdateGrace is how long the post-run step waits for an unfinished
// background check. The check must never make a command slower than it is.
const defaultUpdateGrace = 200 * time.Millisecond

// postRunHook is one step run after a command succeeded. Hooks are kept in
// app.postRunHooks and run in registration order by the single root
// PersistentPostRun. Cobra runs only one persistent hook per command chain,
// so features must append here and never assign PersistentPostRun.
type postRunHook func(cmd *cobra.Command, args []string)

// updateState holds the injectable collaborators and the per-invocation state
// of the background update check. The zero value uses the real environment.
type updateState struct {
	loadConfig func(path string) (*config.Config, error)
	stdoutTTY  func() bool
	version    func() string
	now        func() time.Time
	executable func() (string, error)
	grace      time.Duration

	pending *pendingCheck
}

// pendingCheck is a started background check.
type pendingCheck struct {
	done    chan checkOutcome
	cancel  context.CancelFunc
	current string
}

type checkOutcome struct {
	entry updatecheck.Cache
	err   error
}

// runPostRunHooks is the root's PersistentPostRun: it only iterates the list.
func (a *app) runPostRunHooks(cmd *cobra.Command, args []string) {
	for _, h := range a.postRunHooks {
		h(cmd, args)
	}
}

// currentVersion is the running build's version.
func (a *app) currentVersion() string {
	if a.update.version != nil {
		return a.update.version()
	}
	return buildinfo.Get().Version
}

// skipsUpdateCheck lists the commands the background check never runs for:
// completion machinery must stay instant and silent, and update-check is the
// explicit form of the check.
func skipsUpdateCheck(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		switch c.Name() {
		case "completion", "__complete", "__completeNoDesc", "update-check", "help":
			return true
		}
	}
	return false
}

// startUpdateCheck is called from the root PersistentPreRun. It starts the
// cached check in a goroutine when every opt-in and quietness condition
// holds, and otherwise does nothing. It never returns an error: the update
// check must not be able to fail another command.
func (a *app) startUpdateCheck(cmd *cobra.Command) {
	a.update.pending = nil
	if !a.updateCheckAllowed(cmd) {
		return
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return
	}
	checker := updatecheck.Checker{
		CachePath: filepath.Join(dirs.Cache, "update.json"),
		Now:       a.update.now,
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &pendingCheck{done: make(chan checkOutcome, 1), cancel: cancel, current: a.currentVersion()}
	a.update.pending = p
	go func() {
		// A panic in a best-effort goroutine must not crash the command.
		// The channel is buffered, so the error outcome unblocks the post-run
		// step immediately instead of after the full grace period.
		defer func() {
			if r := recover(); r != nil {
				p.done <- checkOutcome{err: fmt.Errorf("update check panicked: %v", r)}
			}
		}()
		entry, err := checker.Cached(ctx)
		p.done <- checkOutcome{entry: entry, err: err}
	}()
}

// updateCheckAllowed evaluates every condition under which the background
// check may run, cheapest first. The config is read last and a broken config
// silently means "no check".
func (a *app) updateCheckAllowed(cmd *cobra.Command) bool {
	if os.Getenv(NoUpdateCheckEnv) != "" || skipsUpdateCheck(cmd) || a.flags.quiet {
		return false
	}
	// Only the default table output gets a notice; json, ndjson, plain, ...
	// are for machines and stay untouched (this also covers version --format json).
	if a.flags.format != "" && a.flags.format != "table" {
		return false
	}
	if !a.stdoutIsTerminal() || updatecheck.IsDevVersion(a.currentVersion()) {
		return false
	}
	return a.updateCheckEnabledInConfig()
}

func (a *app) updateCheckEnabledInConfig() bool {
	path := a.flags.configPath
	if path == "" {
		dirs, err := config.ResolveDirs()
		if err != nil {
			return false
		}
		path = dirs.ConfigFile
	}
	load := a.update.loadConfig
	if load == nil {
		load = config.Load
	}
	cfg, err := load(path)
	return err == nil && cfg != nil && cfg.UpdateCheck
}

// stdoutIsTerminal reports whether the command's stdout is a character
// device (a terminal), the only case in which a notice is appropriate.
func (a *app) stdoutIsTerminal() bool {
	if a.update.stdoutTTY != nil {
		return a.update.stdoutTTY()
	}
	f, ok := a.io.Out.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// finishUpdateCheck is the post-run hook of the background check. It waits at
// most the grace period for the goroutine, prints a one-line notice to stderr
// when a newer version exists, and cancels whatever is still running.
func (a *app) finishUpdateCheck(cmd *cobra.Command, args []string) {
	p := a.update.pending
	if p == nil {
		return
	}
	a.update.pending = nil
	defer p.cancel()
	grace := a.update.grace
	if grace == 0 {
		grace = defaultUpdateGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case out := <-p.done:
		if out.err == nil && compareVersions(p.current, out.entry.Latest) == statusUpdate {
			latest := out.entry.Latest
			if len(latest) > 0 && latest[0] == 'v' {
				latest = latest[1:]
			}
			fmt.Fprintf(a.io.Err, "brooom %s is available, run 'brooom update-check'\n", latest)
		}
	case <-timer.C:
	}
}
