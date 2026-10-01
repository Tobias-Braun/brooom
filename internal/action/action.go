// Package action turns findings into changes on disk, safely.
//
// Every command that can modify anything goes through the Executor:
//
//  1. Plan: each actionable finding is re-validated at plan time (the path
//     is re-resolved through the scope guard, re-stat'ed, risk flags are
//     re-checked: a file opened since the scan, a branch that got new
//     commits, a worktree that became dirty). Findings that no longer
//     qualify are skipped with a reason.
//  2. Dry run (default): the plan is printed, nothing is modified.
//  3. Apply (--apply): the user confirms per group or per item unless
//     --yes; a session manifest is created before the first step and
//     updated after every step; each step's undo information is recorded.
//  4. Summary: reclaimed bytes, failures, the session ID and recovery hints
//     (e.g. the command that recreates a deleted branch at its old tip).
//
// Each action type (trash, delete-branch, remove-worktree, prune-worktrees,
// git-gc, git-prune, git-reflog-expire) implements Action and registers
// itself with Register.
package action

import (
	"context"
	"errors"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// ErrSkipped wraps reasons why a planned finding is no longer acted on.
var ErrSkipped = errors.New("skipped")

// Env is what actions need to do their work.
type Env struct {
	Config *config.Config
	Git    gitx.Runner
	Guard  *scope.Guard
	// Trasher returns the trasher for a detector (honouring per-detector
	// strategy overrides and the --trash-strategy flag).
	Trasher func(detector string) (trash.Trasher, error)
	// TrasherFor returns the trasher for an explicit strategy. Undo needs it
	// because it must restore with the strategy recorded in the manifest, not
	// with whatever the current config or --trash-strategy selects; Trasher
	// only takes a detector name. Unlike Trasher it is bound to the session
	// that is being undone where a strategy needs one.
	TrasherFor func(strategy config.TrashStrategy) (trash.Trasher, error)
	// BeforeDelete, when set, is called by the trash action right before an
	// item is removed with the delete strategy. It is the place for the
	// one-time permanence warning: Plan must stay free of side effects because
	// dry runs claim that nothing was changed.
	BeforeDelete func()
	// Force allows acting on findings with blocking risk flags and makes
	// delete-branch use -D.
	Force bool

	// plannedDeletes holds the branches (see plannedKey) that the running
	// session deletes. The executor fills it before the first step so that a
	// delete-branch recovery hint does not name a branch that is about to be
	// deleted too as the one keeping the commits reachable.
	plannedDeletes map[string]struct{}
}

// plannedKey identifies a local branch of a repository in Env.plannedDeletes.
func plannedKey(repoPath, fullRef string) string { return repoPath + "\x00" + fullRef }

// Step is one concrete, validated operation.
type Step struct {
	Finding findings.Finding
	// Description is a one-line human-readable description, e.g.
	// "move node_modules (1.2 GB) to trash".
	Description string
	// Command is the equivalent shell command for display, if any.
	Command string
	// Strategy is the trash strategy the step will use, set only by steps
	// that remove files through a trasher. Plans, prompts and headers use it
	// to say "delete permanently" instead of the generic action name; it is
	// display state and never reaches the manifest or JSON output.
	Strategy config.TrashStrategy
	// live is action-specific state the apply-pass re-plan hands to Apply
	// (delete-branch: its decision); nil in every other plan.
	live any
}

// Action executes one action type.
type Action interface {
	// Type is the action type this implementation handles.
	Type() findings.ActionType
	// Plan re-validates the finding and returns the concrete step, or an
	// error wrapping ErrSkipped with the reason it no longer qualifies.
	Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error)
	// Apply executes a planned step and returns the manifest entry
	// describing the outcome and how to undo it.
	Apply(ctx context.Context, env *Env, s Step) (session.Entry, error)
	// Undo reverses an applied entry where possible.
	Undo(ctx context.Context, env *Env, e session.Entry) error
}

var (
	mu       sync.RWMutex
	registry = map[findings.ActionType]Action{}
)

// Register adds an action implementation; duplicates panic.
func Register(a Action) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := registry[a.Type()]; dup {
		panic("action: duplicate action " + string(a.Type()))
	}
	registry[a.Type()] = a
}

// Get returns the action implementation for a type.
func Get(t findings.ActionType) (Action, bool) {
	mu.RLock()
	defer mu.RUnlock()
	a, ok := registry[t]
	return a, ok
}
