package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
)

func newUndoCmd(a *app) *cobra.Command {
	var af applyFlags
	cmd := &cobra.Command{
		Use:   "undo [session-id]",
		Short: "Restore what a session removed (default: the latest session)",
		Long: `Restore the items a session removed, last applied first. Pass a full session
id or a unique prefix; without one the latest session is used. Without --apply
this only prints what would be restored and what cannot be (with the reason
and a manual recovery hint).

Nothing is ever overwritten: an entry whose original location exists again is
reported as a conflict and stays as it was. Entries are only restored inside
the current scope, because manifests are files that can be edited. The scope
is the repository you are in; for a session that was applied with --workspaces
it is the configured roots (--root narrows them). Entries outside the scope
are reported as skipped, not as lost: run undo from the repository they belong
to or with --workspaces.

Exit status: 0 when every restorable entry was restored, 1 when one conflicted
or failed, 2 when confirmation is needed but stdin is not a terminal (pass
--yes).`,
		Example: `  brooom undo
  brooom undo 20260929-224501-3f9a
  brooom undo --apply`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runUndo(cmd, args, af)
		},
	}
	addApplyFlags(cmd, &af)
	return cmd
}

// runUndo loads the session, builds the scope guard and the action
// environment, and hands both to action.RunUndo. It only maps errors to exit
// codes; the restore logic lives in the action package.
func (a *app) runUndo(cmd *cobra.Command, args []string, af applyFlags) error {
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	strategy, err := parseTrashStrategy(af.trashStrategy)
	if err != nil {
		return err
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return err
	}
	store := session.NewStore(dirs.Sessions)
	m, err := loadUndoSession(store, args)
	if errors.Is(err, session.ErrNotFound) && len(args) == 0 {
		fmt.Fprintln(a.io.Out, "nothing to undo")
		return nil
	}
	if err != nil {
		return err
	}
	a.adoptSessionScope(m)
	req, err := a.newScanRequest(scanOptions{})
	if err != nil {
		return err
	}
	env, err := a.undoEnv(ctx, req, m, af, newTrasherResolver(req.cfg, strategy, dirs, m.ID, a.io.Err))
	if err != nil {
		return err
	}
	res, err := action.RunUndo(ctx, env, m, action.UndoOptions{
		Apply:      af.apply,
		Yes:        af.yes,
		IO:         action.IO{In: a.io.In, Out: a.io.Out, Err: a.io.Err},
		Store:      store,
		StdinIsTTY: a.canPrompt,
		RerunHint:  "re-run '" + cmd.CommandPath() + " " + m.ID + " --apply'",
	})
	return mapUndoError(res, err, af.apply)
}

// adoptSessionScope makes undo of a session recorded with --workspaces resolve
// the workspace scope without the flag, so the printed `brooom undo <id>` works
// from any directory. Only the fact is taken from the manifest: the guard is
// still built from the configured roots (all of them, since a recorded --root
// may have been removed since), never from paths the manifest names, and every
// entry is checked against it. An explicit --root on this invocation is kept.
func (a *app) adoptSessionScope(m *session.Manifest) {
	if m.Workspaces {
		a.flags.workspaces = true
	}
}

// mapUndoError maps the outcome to exit codes: a missing confirmation is a
// usage error (2); an applied run in which a restorable entry conflicted or
// failed is 1, after the summary was printed.
func mapUndoError(res *action.UndoResult, err error, applied bool) error {
	switch {
	case errors.Is(err, action.ErrConfirmationRequired):
		return usageError{err}
	case err != nil:
		return err
	case applied && res.Incomplete():
		return fmt.Errorf("%d restorable entries could not be restored; see the summary above", res.Conflicts+res.Failed)
	}
	return nil
}

// loadUndoSession selects the session: the latest one, or the one named by a
// full id or unique prefix. An unknown id lists the known sessions.
func loadUndoSession(store *session.Store, args []string) (*session.Manifest, error) {
	if len(args) == 0 {
		return store.Latest()
	}
	m, err := store.Load(args[0])
	if errors.Is(err, session.ErrNotFound) {
		return nil, fmt.Errorf("%w; known sessions: %s", err, knownSessions(store))
	}
	return m, err
}

// knownSessions lists up to five of the newest session ids for error text.
func knownSessions(store *session.Store) string {
	list, _, err := store.List()
	if err != nil || len(list) == 0 {
		return "none"
	}
	var ids []string
	for i, m := range list {
		if i == 5 {
			ids = append(ids, "...")
			break
		}
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}

// undoEnv builds the action environment for undo. The guard is the scope of
// the invocation (the repository around the working directory, or the
// workspace roots), the same resolution scan uses, including its usage error
// outside a repository. When entries fall outside it, the user-level
// locations of the detectors are allowed too, so sessions of `brooom ai
// --user` can be undone from any repository.
func (a *app) undoEnv(ctx context.Context, req *scanRequest, m *session.Manifest, af applyFlags, r *trasherResolver) (*action.Env, error) {
	runner, _ := newGitRunner()
	ts, err := a.buildTargets(ctx, req, runner)
	if err != nil {
		return nil, err
	}
	env, err := a.envForAllowed(req.cfg, runner, ts, af, r)
	if err != nil {
		return nil, err
	}
	if !anyOutsideScope(action.PlanUndo(m, env)) {
		return env, nil
	}
	withUser := *req.cfg
	withUser.Detectors.AIArtifacts.UserLocations = true
	ts.addExtraTargets(ctx, &withUser, detect.All())
	return a.envForAllowed(req.cfg, runner, ts, af, r)
}

func (a *app) envForAllowed(cfg *config.Config, runner gitx.Runner, ts *targetSet, af applyFlags, r *trasherResolver) (*action.Env, error) {
	guard, err := ts.newGuard()
	if err != nil {
		return nil, fmt.Errorf("build scope: %w", err)
	}
	return newActionEnv(cfg, runner, guard, af, r), nil
}

func anyOutsideScope(steps []action.UndoStep) bool {
	for _, s := range steps {
		if s.OutsideScope {
			return true
		}
	}
	return false
}
