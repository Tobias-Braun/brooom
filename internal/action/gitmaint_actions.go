package action

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// gitGC runs `git gc --quiet --prune=<prune_expire>`. The prune date is
// always passed explicitly so gc never falls back to its own default
// implicitly. Never `--force`, `--aggressive` or `--cruft`: those are the
// user's git configuration to decide. gc also expires reflog entries per
// gc.reflogExpire / gc.reflogExpireUnreachable and runs `git worktree prune`
// and `git rerere gc`, which is why the hint and the plan say so.
type gitGC struct{}

var gcBase = maintBase{
	typ: findings.ActionGitGC, dateArg: argPrune,
	defaultDate: func(c config.GitBloat) string { return c.PruneExpire },
}

// Type implements Action.
func (gitGC) Type() findings.ActionType { return findings.ActionGitGC }

// Plan implements Action. The date is validated with the prune dry run; gc is
// not skipped for "nothing to do" because the finding itself (loose objects,
// many packs) is the reason to run it.
func (gitGC) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	m, err := gcBase.prepare(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	stats, err := gitx.CountObjects(ctx, env.Git, m.repo.Dir)
	if err != nil {
		return Step{}, fmt.Errorf("git count-objects in %s: %w", m.repo.Dir, err)
	}
	return Step{
		Finding: f,
		Description: fmt.Sprintf("git gc in %s: repack %d loose objects and %d packs, delete unreachable objects older than %s, "+
			"expire old reflog entries per gc.reflogExpire / gc.reflogExpireUnreachable (NOT restorable)",
			filepath.Base(m.repo.Dir), stats.Count, stats.Packs, m.date),
		Command: gcCommand(m),
	}, nil
}

func gcCommand(m *maintCtx) string { return "git -C " + m.repo.Dir + " gc --quiet --prune=" + m.date }

// Apply implements Action.
func (gitGC) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	return applyMaint(ctx, env, gcBase, s, true, gcHint, func(date string) []string {
		return []string{"gc", "--quiet", "--prune=" + date}
	})
}

// Undo implements Action.
func (gitGC) Undo(context.Context, *Env, session.Entry) error {
	return notRestorable(gcHint("the configured prune_expire"))
}

// gitPrune runs `git prune --expire=<date>`.
type gitPrune struct{}

var pruneBase = maintBase{
	typ: findings.ActionGitPrune, dateArg: argExpire,
	defaultDate: func(c config.GitBloat) string { return c.PruneExpire },
}

// Type implements Action.
func (gitPrune) Type() findings.ActionType { return findings.ActionGitPrune }

// Plan implements Action. It lists what would be deleted with git's own dry
// run and skips when that is nothing.
func (gitPrune) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	m, err := pruneBase.prepare(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	u, err := dryRunPrune(ctx, env, m.repo, m.date)
	if err != nil {
		return Step{}, skipf("%v", err)
	}
	if u.count == 0 {
		return Step{}, skipf("nothing to do: no unreachable objects older than %s", m.date)
	}
	return Step{
		Finding: f,
		Description: fmt.Sprintf("git prune in %s: delete %s older than %s for good (NOT restorable)",
			filepath.Base(m.repo.Dir), describeUnreachable(u), m.date),
		Command: "git -C " + m.repo.Dir + " prune --expire=" + m.date,
	}, nil
}

// describeUnreachable renders the object count and size; a sampled size is
// marked as a lower bound.
func describeUnreachable(u unreachable) string {
	noun := plural(u.count, "unreachable object")
	if u.sampled == 0 {
		return noun
	}
	prefix := ""
	if u.sampled < u.count {
		prefix = "at least "
	}
	return fmt.Sprintf("%s (%s%s)", noun, prefix, output.FormatSize(u.bytes))
}

// Apply implements Action.
func (gitPrune) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	return applyMaint(ctx, env, pruneBase, s, false, gitPruneHint, func(date string) []string {
		return []string{"prune", "--expire=" + date}
	})
}

// Undo implements Action.
func (gitPrune) Undo(context.Context, *Env, session.Entry) error {
	return notRestorable(gitPruneHint("the recorded date"))
}

// gitReflogExpire runs `git reflog expire --expire=<date> --all`. Entries of
// unreachable commits additionally follow gc.reflogExpireUnreachable, which
// Brooom does not override.
type gitReflogExpire struct{}

var reflogBase = maintBase{
	typ: findings.ActionGitReflogExpire, dateArg: argExpire,
	defaultDate: func(c config.GitBloat) string { return c.ReflogExpire },
}

// Type implements Action.
func (gitReflogExpire) Type() findings.ActionType { return findings.ActionGitReflogExpire }

// Plan implements Action. It counts the entries git reports as "would prune"
// and skips when there are none.
func (gitReflogExpire) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	m, err := reflogBase.prepare(ctx, env, f)
	if err != nil {
		return Step{}, err
	}
	n, err := dryRunReflog(ctx, env, m.repo, m.date)
	if err != nil {
		return Step{}, skipf("%v", err)
	}
	if n == 0 {
		return Step{}, skipf("nothing to do: no reflog entries older than %s", m.date)
	}
	return Step{
		Finding: f,
		Description: fmt.Sprintf("git reflog expire in %s: remove %s older than %s (NOT restorable)",
			filepath.Base(m.repo.Dir), reflogCount(n), m.date),
		Command: "git -C " + m.repo.Dir + " reflog expire --expire=" + m.date + " --all",
	}, nil
}

// reflogCount pluralizes "entry" correctly, which plural does not.
func reflogCount(n int) string {
	if n == 1 {
		return "1 reflog entry"
	}
	return fmt.Sprintf("%d reflog entries", n)
}

// Apply implements Action.
func (gitReflogExpire) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	return applyMaint(ctx, env, reflogBase, s, true, reflogHint, func(date string) []string {
		return []string{"reflog", "expire", "--expire=" + date, "--all"}
	})
}

// Undo implements Action.
func (gitReflogExpire) Undo(context.Context, *Env, session.Entry) error {
	return notRestorable(reflogHint("the recorded date"))
}
