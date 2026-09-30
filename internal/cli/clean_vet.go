package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Reasons that tests and users see verbatim.
const (
	reasonUserNotEnabled = "user location not enabled for this run (pass --user)"
	forceNoneHint        = "the file carries no suggested action; --force cannot add one. " +
		"Export again with `brooom scan --force --format json`"
)

// verdictEntry is a finding together with why it was not accepted.
type verdictEntry struct {
	finding findings.Finding
	reason  string
}

// verdict splits the selected findings of a file. Accepted findings go to the
// executor, which re-validates them against the live state. Refused findings
// contradict the scope or the rules of this run (they make the command fail);
// skipped findings are legitimate but have nothing to execute.
type verdict struct {
	accepted []findings.Finding
	refused  []verdictEntry
	skipped  []verdictEntry
}

// vet checks every finding against this run's scope and the static rules. It
// only reads. What it accepts is still no promise: Plan re-validates paths,
// open files, dirty state and new commits at apply time, and risk flags from
// the file are ignored by design.
func (sc *cleanScope) vet(fs []findings.Finding) verdict {
	var v verdict
	for _, f := range fs {
		if !f.Actionable() {
			v.skipped = append(v.skipped, verdictEntry{f, forceNoneHint})
			continue
		}
		if reason := sc.refusal(f); reason != "" {
			v.refused = append(v.refused, verdictEntry{f, reason})
			continue
		}
		if reason := unavailableAction(f); reason != "" {
			v.skipped = append(v.skipped, verdictEntry{f, reason})
			continue
		}
		v.accepted = append(v.accepted, stripUntrusted(f))
	}
	return v
}

// stripUntrusted removes the action arguments that would steer a destructive
// git maintenance operation. The expiry of git gc, prune and reflog expire
// comes from the configuration of the repository (the actions fall back to
// it when the arguments are absent), never from a file that can be edited:
// {"expire": "now"} would otherwise empty the reflog without --force.
func stripUntrusted(f findings.Finding) findings.Finding {
	switch f.SuggestedAction.Type {
	case findings.ActionGitGC, findings.ActionGitPrune, findings.ActionGitReflogExpire:
		f.SuggestedAction.Args = nil
	}
	return f
}

// unavailableAction skips findings whose action is a known type that this
// build does not implement yet.
func unavailableAction(f findings.Finding) string {
	if _, ok := action.Get(f.SuggestedAction.Type); !ok {
		return fmt.Sprintf("action %q is not available in this build", f.SuggestedAction.Type)
	}
	return ""
}

// knownActions are the action types of the findings schema. A type outside
// this set cannot come from brooom and is refused, never skipped quietly.
var knownActions = map[findings.ActionType]bool{
	findings.ActionNone: true, findings.ActionTrash: true, findings.ActionDeleteBranch: true,
	findings.ActionRemoveWorktree: true, findings.ActionPruneWorktrees: true, findings.ActionGitGC: true,
	findings.ActionGitPrune: true, findings.ActionGitReflogExpire: true,
}

// refusal returns why the finding must not be acted on in this run, or "".
func (sc *cleanScope) refusal(f findings.Finding) string {
	if !knownActions[f.SuggestedAction.Type] {
		return fmt.Sprintf("unknown action type %q", f.SuggestedAction.Type)
	}
	resolved, reason := sc.checkPath(f)
	if reason != "" {
		return reason
	}
	if reason := checkSymlinkSwap(f, resolved); reason != "" {
		return reason
	}
	if reason := checkAlias(f, resolved); reason != "" {
		return reason
	}
	if reason := sc.checkConfig(f, resolved); reason != "" {
		return reason
	}
	if !isGitFinding(f) {
		return ""
	}
	return sc.checkGit(f)
}

// checkAlias refuses a trash finding whose path is another spelling (an
// 8.3 short name on Windows) of .git or of Brooom's own state. The trash
// action repeats the check at apply time; refusing here reports it as a
// tampered file instead of a quiet skip.
func checkAlias(f findings.Finding, resolved string) string {
	if f.SuggestedAction.Type != findings.ActionTrash {
		return ""
	}
	if err := action.RefuseByIdentity(resolved); err != nil {
		return "protected path: " + strings.TrimPrefix(err.Error(), action.ErrSkipped.Error()+": ")
	}
	return ""
}

// checkPath resolves the finding path through the guard that fits its scope.
// User findings are only accepted with --user and only inside a user location;
// everything else must lie inside the repository or the selected roots. The
// action decides how symlinks are treated: trash removes a link without
// following it (ResolveParent), all other actions resolve fully.
func (sc *cleanScope) checkPath(f findings.Finding) (string, string) {
	if !filepath.IsAbs(f.Path) {
		return "", "path is not absolute"
	}
	g := sc.project
	if f.Scope.Type == findings.ScopeUser {
		if !sc.userEnabled {
			return "", reasonUserNotEnabled
		}
		if sc.user == nil {
			return "", "no user location exists for this run"
		}
		g = sc.user
	}
	resolve := g.Resolve
	if f.SuggestedAction.Type == findings.ActionTrash {
		resolve = g.ResolveParent
	}
	resolved, err := resolve(f.Path)
	if err != nil {
		return "", pathRefusal(err)
	}
	return resolved, ""
}

// pathRefusal words a guard error: outside the scope is the common, expected
// case and gets a short reason.
func pathRefusal(err error) string {
	if errors.Is(err, scope.ErrOutsideScope) {
		return "outside the scope of this run"
	}
	return "path cannot be validated: " + err.Error()
}

// checkSymlinkSwap refuses a trash finding whose path is a symlink now
// although the finding does not describe one. That is what a directory
// swapped for a link after the scan looks like; the trash action would only
// move the link, but such a finding no longer describes what was reviewed.
func checkSymlinkSwap(f findings.Finding, resolved string) string {
	if f.SuggestedAction.Type != findings.ActionTrash || f.HasRisk(findings.RiskSymlink) {
		return ""
	}
	fi, err := os.Lstat(resolved)
	if err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return "path is a symbolic link now, but the finding describes a regular " + string(f.Kind)
	}
	return ""
}

// gitKinds are the finding kinds that refer to a repository.
var gitKinds = map[findings.Kind]bool{
	findings.KindBranch: true, findings.KindWorktree: true, findings.KindWorktreeMissing: true,
	findings.KindGitObjects: true, findings.KindGitPacks: true, findings.KindGitReflog: true,
	findings.KindGitLargeBlob: true,
}

// isGitAction reports whether the action type works through git.
func isGitAction(t findings.ActionType) bool {
	switch t {
	case findings.ActionDeleteBranch, findings.ActionRemoveWorktree, findings.ActionPruneWorktrees,
		findings.ActionGitGC, findings.ActionGitPrune, findings.ActionGitReflogExpire:
		return true
	}
	return false
}

// isGitFinding decides by kind and by action, so a tampered kind cannot
// dodge the repository checks of a git action.
func isGitFinding(f findings.Finding) bool {
	return gitKinds[f.Kind] || isGitAction(f.SuggestedAction.Type)
}

// isWorktreeFinding reports findings whose Path is a linked worktree and
// whose repository is named in Meta["repo"].
func isWorktreeFinding(f findings.Finding) bool {
	switch f.SuggestedAction.Type {
	case findings.ActionRemoveWorktree, findings.ActionPruneWorktrees:
		return true
	}
	return f.Kind == findings.KindWorktree || f.Kind == findings.KindWorktreeMissing
}

// checkGit applies the extra rules of git findings: the repository they refer
// to must be a repository of this scope, and the branch name must be safe to
// hand to git. The name is additionally passed only through the action's
// argument-safe API.
func (sc *cleanScope) checkGit(f findings.Finding) string {
	repo := f.Path
	if isWorktreeFinding(f) {
		repo = f.Meta["repo"]
		if repo == "" {
			return `finding has no repository (meta "repo")`
		}
	}
	if !filepath.IsAbs(repo) {
		return "repository path is not absolute"
	}
	// The main worktree of a linked worktree is accepted as a repository only.
	resolved, err := sc.project.ResolveRepoMeta(repo)
	if err != nil {
		return "repository " + pathRefusal(err)
	}
	if !sc.isRepo(resolved) {
		return "repository " + resolved + " is not a repository of this run's scope"
	}
	return unsafeRefName(f.Ref)
}

// unsafeRefName refuses names git could read as an option and names with
// control characters, which can forge terminal output.
func unsafeRefName(ref string) string {
	if strings.HasPrefix(ref, "-") {
		return "branch name starts with '-'"
	}
	if strings.ContainsFunc(ref, unicode.IsControl) {
		return "branch name contains control characters"
	}
	return ""
}
