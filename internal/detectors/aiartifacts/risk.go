package aiartifacts

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/procs"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// ignoreChunk is the number of paths per `git check-ignore` call. gitx.Runner
// has no stdin, so the paths travel as arguments and are chunked to stay well
// below command line limits.
const ignoreChunk = 64

// openFiles is procs.OpenFiles, replaceable so tests can simulate an
// incomplete or unavailable check that no real machine reproduces on demand.
var openFiles = procs.OpenFiles

// Evidence codes beyond matches_pattern and age.
const (
	evOpenUnavailable = "open_check_unavailable"
	evTrackedFailed   = "tracked_check_failed"
)

// flag sets the risk flags of all items. The order is fixed so findings are
// stable between runs.
func (r *run) flag(ctx context.Context, items []*item) error {
	if err := r.markOpen(ctx, items); err != nil {
		return err
	}
	if r.target.Kind == scope.TargetRepo {
		if err := r.markGit(ctx, items); err != nil {
			return err
		}
	}
	for _, it := range items {
		if r.cfg.Thresholds.RecentDays > it.age {
			it.flags = append(it.flags, findings.RiskRecentlyModified)
		}
		if r.target.Kind == scope.TargetUser {
			it.flags = append(it.flags, findings.RiskOutsideRepo)
		}
		if it.cand.symlink {
			it.flags = append(it.flags, findings.RiskSymlink)
		}
	}
	return nil
}

// markOpen runs one batched open-file check. A process seen on a path always
// flags it, even when the check as a whole was incomplete; when the check
// could not vouch for a path (unavailable or incomplete) that is noted in the
// evidence and never treated as "safe". Symlinks are skipped: a link is not
// held open, only its target could be, and the target is never the subject.
func (r *run) markOpen(ctx context.Context, items []*item) error {
	var paths []string
	for _, it := range items {
		if !it.cand.symlink {
			paths = append(paths, it.path)
		}
	}
	if len(paths) == 0 {
		return nil
	}
	open, err := r.env.OpenFiles(ctx, paths, openFiles)
	if cerr := ctx.Err(); cerr != nil {
		return cerr
	}
	for _, it := range items {
		if it.cand.symlink {
			continue
		}
		switch {
		case open[filepath.Clean(it.path)]:
			it.flags = append(it.flags, findings.RiskFileOpen)
		case err != nil:
			it.evidence = append(it.evidence, findings.Evidence{
				Code:    evOpenUnavailable,
				Message: "could not check whether a process has it open",
				Value:   openReason(err),
			})
		}
	}
	return nil
}

// openReason gives the stable reason string for the evidence value.
func openReason(err error) string {
	switch {
	case errors.Is(err, procs.ErrUnavailable):
		return "unavailable"
	case errors.Is(err, procs.ErrIncomplete):
		return "incomplete"
	default:
		return "error"
	}
}

// markGit sets tracked_files (blocking) and gitignored (informational) for a
// repository target.
func (r *run) markGit(ctx context.Context, items []*item) error {
	rels := make([]string, len(items))
	for i, it := range items {
		rel, err := filepath.Rel(r.target.Path, it.path)
		if err != nil {
			rel = ""
		}
		rels[i] = filepath.ToSlash(rel)
	}
	if err := r.markTrackedAll(ctx, items, rels); err != nil {
		return err
	}
	ignored, err := r.ignoredSet(ctx, items, rels)
	if err != nil {
		return err
	}
	for i, it := range items {
		if ignored[ignoreArg(it, rels[i])] {
			it.flags = append(it.flags, findings.RiskGitignored)
		}
	}
	return nil
}

// markTrackedAll asks git once (in a few chunked calls, see
// gitx.TrackedUnder) whether anything at or below each candidate is tracked,
// instead of running one `git ls-files` per candidate. A failing check (or a
// path that cannot be made relative) flags the item as tracked: the trash
// action refuses when it cannot tell either, and a report that hides the
// doubt would be worse than a blocked finding.
func (r *run) markTrackedAll(ctx context.Context, items []*item, rels []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var asked []string
	for _, rel := range rels {
		if insideRepo(rel) {
			asked = append(asked, rel)
		}
	}
	tracked, err := gitx.TrackedUnder(ctx, r.env.Git, r.target.Path, asked)
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	for i, it := range items {
		switch {
		case !insideRepo(rels[i]):
			markTrackedUnknown(it, "path is not inside the repository, tracked files could not be ruled out")
		case err != nil:
			markTrackedUnknown(it, "git ls-files failed, tracked files could not be ruled out")
		case tracked[rels[i]]:
			it.flags = append(it.flags, findings.RiskTrackedFiles)
		}
	}
	return nil
}

// markTrackedUnknown blocks an item whose tracked state could not be decided.
func markTrackedUnknown(it *item, msg string) {
	it.flags = append(it.flags, findings.RiskTrackedFiles)
	it.evidence = append(it.evidence, findings.Evidence{Code: evTrackedFailed, Message: msg})
}

// ignoreArg is the path form handed to check-ignore. Directories get a
// trailing slash so directory-only ignore patterns ("cache/") apply.
func ignoreArg(it *item, rel string) string {
	if it.cand.isDir {
		return rel + "/"
	}
	return rel
}

// ignoredSet returns the set of ignoreArg values git reports as ignored.
// Exit code 1 means none of the chunk is ignored; a higher code is a
// non-fatal failure of the informational check and the chunk is skipped.
func (r *run) ignoredSet(ctx context.Context, items []*item, rels []string) (map[string]bool, error) {
	var args []string
	for i, it := range items {
		if insideRepo(rels[i]) {
			args = append(args, ignoreArg(it, rels[i]))
		}
	}
	ignored := map[string]bool{}
	for start := 0; start < len(args); start += ignoreChunk {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, p := range r.checkIgnore(ctx, args[start:min(start+ignoreChunk, len(args))]) {
			ignored[p] = true
		}
	}
	return ignored, nil
}

// checkIgnore runs one `git check-ignore -z` call and returns the paths it
// reports. Git exits with 1 when none of the paths is ignored, which surfaces
// as an error with empty output and correctly yields no paths; any other
// failure likewise only loses the informational gitignored flag for the chunk.
func (r *run) checkIgnore(ctx context.Context, chunk []string) []string {
	// -z is only valid together with --stdin, so the paths go through stdin
	// when the runner supports it (NUL separated, safe for any file name).
	// Otherwise they are passed as arguments and the output is line based;
	// names git would quote then simply stay unflagged.
	in := strings.Join(chunk, "\x00") + "\x00"
	sep := "\x00"
	out, err := gitx.RunInput(ctx, r.env.Git, r.target.Path, strings.NewReader(in), "check-ignore", "-z", "--stdin")
	if errors.Is(err, gitx.ErrInputUnsupported) {
		sep = "\n"
		out, err = r.env.Git.Run(ctx, r.target.Path, append([]string{"check-ignore", "--"}, chunk...)...)
	}
	if err != nil {
		return nil
	}
	var paths []string
	for _, p := range strings.Split(out, sep) {
		if p != "" {
			paths = append(paths, p)
		}
	}
	return paths
}

// insideRepo reports whether a slash-separated path relative to the
// repository root stays inside it.
func insideRepo(rel string) bool {
	return rel != "" && rel != ".." && !strings.HasPrefix(rel, "../")
}

// ageEvidence is the age evidence of a finding; its value is the int days.
func ageEvidence(days int) findings.Evidence {
	return findings.Evidence{
		Code:    "age",
		Message: fmt.Sprintf("last modified %d days ago", days),
		Value:   days,
	}
}
