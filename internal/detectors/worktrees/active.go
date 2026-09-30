package worktrees

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/procs"
)

// openFiles is procs.OpenFiles, replaceable so tests can simulate a process
// in a worktree or an unavailable check on any OS.
var openFiles = procs.OpenFiles

// Evidence codes for the "is somebody working here" signals.
const (
	evRecent          = "recently_modified"
	evInUse           = "worktree_in_use"
	evOpenUnavailable = "open_check_unavailable"
)

// markActivity adds the signals that a worktree is being worked in right now
// to a candidate: an in-use worktree is blocked (never overridable, like a
// locked one) and a recently modified one is flagged informationally. Worktrees without a
// removal to suggest need neither, and missing directories have no activity.
func (s *scan) markActivity(ctx context.Context, e *entry, v *verdict) error {
	if e.missing || v.action != findings.ActionRemoveWorktree {
		return nil
	}
	if err := s.markInUse(ctx, e, v); err != nil {
		return err
	}
	s.markRecent(ctx, e, v)
	return nil
}

// markInUse blocks worktrees that contain the working directory of this
// process (known on every OS) or that a process holds open or stands in
// (procs; the Linux scan reads cwd, macOS lsof and Windows Restart Manager
// report what they can). An unavailable or incomplete check is unknown, never
// safe: it stays visible as evidence but keeps the suggestion, like the trash
// action does, because the action re-checks at apply time.
func (s *scan) markInUse(ctx context.Context, e *entry, v *verdict) error {
	inUse := gitx.CwdWithin(e.path)
	msg := "the current directory is inside the worktree"
	if !inUse {
		res, err := s.openState(ctx, e.path)
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		inUse = res[e.path]
		msg = "a process has a file open in the worktree or its working directory there"
		if !inUse && err != nil {
			v.evidence = append(v.evidence, unknownOpenEvidence(err))
		}
	}
	if !inUse {
		return nil
	}
	v.risks = append(v.risks, findings.RiskFileOpen)
	v.evidence = append(v.evidence, findings.Evidence{Code: evInUse, Message: msg, Value: true})
	v.action = findings.ActionNone
	v.reason = "worktree is in use (" + msg + "); finish or close that first"
	return nil
}

// openBatch is the outcome of the one open-file check of a scan. Every
// candidate used to pay for its own call, which on macOS meant one lsof run
// (and a fresh time budget) per worktree; procs now answers all directories
// of a call from one lsof listing.
type openBatch struct {
	paths map[string]bool
	res   map[string]bool
	// err applies to every path of the batch, as it would to a single call.
	err error
}

// prefetchOpen checks all existing, in-scope, non-main worktrees with a
// single openFiles call. Worktrees that a later rule discards are checked
// needlessly, but an extra directory only adds a prefix comparison to the one
// shared lsof listing (or one directory walk on Linux and Windows), not an
// lsof run.
func (s *scan) prefetchOpen(ctx context.Context, wts []gitx.Worktree) {
	var paths []string
	set := map[string]bool{}
	for _, wt := range wts {
		if wt.Main || wt.Bare {
			continue
		}
		e, ok := s.entry(wt)
		if !ok || e.missing || gitx.CwdWithin(e.path) {
			continue
		}
		paths = append(paths, e.path)
		set[e.path] = true
	}
	if len(paths) == 0 {
		return
	}
	res, err := openFiles(ctx, paths)
	s.open = &openBatch{paths: set, res: res, err: err}
}

// openState answers the open-file question for one worktree path from the
// batch, falling back to a single call for paths the batch did not cover.
func (s *scan) openState(ctx context.Context, path string) (map[string]bool, error) {
	if b := s.open; b != nil && b.paths[path] {
		return b.res, b.err
	}
	return openFiles(ctx, []string{path})
}

func unknownOpenEvidence(err error) findings.Evidence {
	what := "failed"
	switch {
	case errors.Is(err, procs.ErrUnavailable):
		what = "unavailable"
	case errors.Is(err, procs.ErrIncomplete):
		what = "incomplete"
	}
	return findings.Evidence{
		Code:    evOpenUnavailable,
		Message: "open-file check " + what + ", so processes in the worktree are unknown",
		Value:   what,
	}
}

// markRecent flags a worktree whose newest file (or, without a readable
// directory, HEAD commit) changed within thresholds.recent_days. The flag is
// purely informational: agent runs leave hundreds of fresh worktrees that must
// be removable right away, so it never withholds the action and never lowers
// the confidence. Real protection comes from the dirty, locked, in-use and
// unstarted checks. recent_days 0 disables the flag, and an unknown time is
// not recent.
func (s *scan) markRecent(ctx context.Context, e *entry, v *verdict) {
	days := s.cfg.Thresholds.RecentDays
	last := s.lastModified(ctx, e)
	if days <= 0 || last.IsZero() || s.env.AgeDays(last) >= days {
		return
	}
	v.risks = append(v.risks, findings.RiskRecentlyModified)
	v.evidence = append(v.evidence, findings.Evidence{
		Code:    evRecent,
		Message: fmt.Sprintf("modified %d days ago, within the last %d days (informational, does not block removal)", s.env.AgeDays(last), days),
		Value:   s.env.AgeDays(last),
	})
}
