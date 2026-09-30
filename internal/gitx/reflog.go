package gitx

import (
	"context"
	"strings"
)

// StashRef is the ref whose reflog IS the list of stashes: every entry of
// `git stash list` is one line of the reflog of refs/stash, and the stash
// commits are reachable only through it. Expiring that reflog therefore
// deletes uncommitted user work, not just a recovery point.
const StashRef = "refs/stash"

// stashProtection are the `-c` settings that make git keep every stash entry
// during `reflog expire` and `gc`. git honours the per-ref pattern
// gc.<pattern>.reflogExpire (and ...Unreachable, which is the one that hits
// older stash entries because they are not reachable from the stash tip) only
// when no --expire option is given, which is why the expiry is passed as
// gc.reflogExpire instead of --expire.
var stashProtection = []string{
	"-c", "gc." + StashRef + ".reflogExpire=never",
	"-c", "gc." + StashRef + ".reflogExpireUnreachable=never",
}

// StashProtection returns the leading git arguments that protect the stash
// reflog from expiry. The result is a fresh slice the caller may extend.
func StashProtection() []string { return append([]string(nil), stashProtection...) }

// ReflogExpireArgs builds `git reflog expire --all` for every reflog except
// the stash reflog. The date reaches git as the value of a config setting,
// never as a separate argument, so it cannot be taken for an option.
// dryRun adds --dry-run --verbose, which lists what would be pruned.
func ReflogExpireArgs(date string, dryRun bool) []string {
	args := append(StashProtection(), "-c", "gc.reflogExpire="+date, "reflog", "expire")
	if dryRun {
		args = append(args, "--dry-run", "--verbose")
	}
	return append(args, "--all")
}

// ReflogExpireCommand renders ReflogExpireArgs for display.
func ReflogExpireCommand(date string) string {
	return "git " + strings.Join(ReflogExpireArgs(date, false), " ")
}

// StashExpiring counts the stash entries that git would expire: with an empty
// date those older than git's own gc settings (gc.reflogExpire and
// gc.reflogExpireUnreachable, 90 / 30 days by default), else those older than
// date. It never modifies the repository. A repository without stashes
// yields 0.
func StashExpiring(ctx context.Context, r Runner, dir, date string) (int, error) {
	// for-each-ref succeeds with no output for a missing ref, unlike the
	// dry run below, which fails with "points nowhere".
	exists, err := r.Run(ctx, dir, "for-each-ref", "--format=%(refname)", StashRef)
	if err != nil {
		return 0, err
	}
	if exists == "" {
		return 0, nil
	}
	args := []string{"reflog", "expire", "--dry-run", "--verbose"}
	if date != "" {
		args = append(args, "--expire="+date, "--expire-unreachable="+date)
	}
	out, err := r.Run(ctx, dir, append(args, StashRef)...)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range Lines(out) {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "prune") || strings.HasPrefix(l, "would prune") {
			n++
		}
	}
	return n, nil
}
