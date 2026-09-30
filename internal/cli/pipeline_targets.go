package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// Messages of the usage errors of target selection. They say how to fix the
// invocation because these are the errors a new user meets first.
var (
	errNotInRepo  = fmt.Errorf("%w; run it inside a repo, use --workspaces, or configure roots with `brooom roots add <path>`", scope.ErrNotInRepo)
	errBareAnchor = errors.New("this folder is the bare repository of a bare plus linked worktrees layout and has no working tree; run brooom inside one of its worktrees or use --workspaces")
	errNoRoots    = errors.New("no workspace roots available for --workspaces; add one with `brooom roots add <path>`")
)

// targetSet is what target building produces: the targets to scan, the
// locations the guard must allow and the non-fatal problems met on the way.
type targetSet struct {
	targets []scope.Target
	// allowed are the guard locations. They are a superset of the target
	// paths' parents: the repo, the selected roots and the user-level targets.
	allowed []string
	// repoMeta are directories the guard accepts only as the repository a
	// branch or worktree operation runs git in (scope.Guard.WithRepoMeta): the
	// main worktree of a linked worktree. Nothing below them is in scope.
	repoMeta []string
	errs     []findings.ScanError
}

// newGuard builds the scan guard: the allowed locations plus the repository
// metadata locations, which only Guard.ResolveRepoMeta accepts.
func (ts *targetSet) newGuard() (*scope.Guard, error) {
	return guardWithMeta(ts.allowed, ts.repoMeta)
}

// guardWithMeta is scope.NewGuard(allowed...) plus repository metadata
// locations.
func guardWithMeta(allowed, meta []string) (*scope.Guard, error) {
	g, err := scope.NewGuard(allowed...)
	if err != nil || len(meta) == 0 {
		return g, err
	}
	return g.WithRepoMeta(meta...)
}

// allow adds an optional location to the guard after checking that the guard
// would accept it, so one stale location never makes NewGuard fail for the
// whole scan. Missing locations are skipped silently when quiet is true (a
// catalog lists many tools that are not installed); other failures are
// recorded as scan errors naming the path.
func (ts *targetSet) allow(path, what string, quiet bool) (string, bool) {
	g, err := scope.NewGuard(path)
	if err == nil {
		resolved := g.Allowed()[0]
		ts.allowed = append(ts.allowed, resolved)
		return resolved, true
	}
	missingButExpected := quiet && errors.Is(err, fs.ErrNotExist)
	if !missingButExpected {
		ts.errs = append(ts.errs, findings.ScanError{Path: path, Message: fmt.Sprintf("%s not allowed: %v", what, err)})
	}
	return "", false
}

// buildTargets selects the targets for the request: the repository around
// the working directory by default, the configured roots with --workspaces.
func (a *app) buildTargets(ctx context.Context, req *scanRequest, runner gitx.Runner) (*targetSet, error) {
	if a.flags.workspaces {
		return a.workspaceTargets(ctx, req.cfg)
	}
	return repoTargets(ctx, runner)
}

// repoTargets builds the single repo target of the working directory. Inside
// a linked worktree the repository's main worktree is additionally accepted
// as repository metadata location (never scanned, and nothing below it is in
// scope) so the branch and worktree detectors can resolve the repository
// that their git commands run in.
func repoTargets(ctx context.Context, runner gitx.Runner) (*targetSet, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	root, err := scope.FindRepoRoot(cwd)
	if errors.Is(err, scope.ErrNotInRepo) {
		return nil, usageError{errNotInRepo}
	}
	if err != nil {
		return nil, err
	}
	ts := &targetSet{
		targets: []scope.Target{{
			Kind:  scope.TargetRepo,
			Path:  root,
			Scope: findings.Scope{Type: findings.ScopeRepo, Path: root},
		}},
		allowed: []string{root},
	}
	if !isLinkedWorktree(root) {
		return ts, nil
	}
	if scope.IsBareAnchor(root) {
		// Nothing to sweep here, and saying so as "0 findings" would be
		// misleading: the files live in the linked worktrees below.
		return nil, usageError{errBareAnchor}
	}
	ts.allowMainWorktree(ctx, runner, root)
	return ts, nil
}

// allowMainWorktree registers the main worktree of the linked worktree at root
// as a repository metadata location.
// A failed listing is a scan error, not a failure: scanning continues with
// the linked worktree only.
func (ts *targetSet) allowMainWorktree(ctx context.Context, runner gitx.Runner, root string) {
	main, err := linkedWorktreeMain(ctx, runner, root)
	if err != nil {
		ts.errs = append(ts.errs, findings.ScanError{Path: root, Message: "cannot locate the main worktree, scanning the linked worktree only: " + err.Error()})
		return
	}
	g, err := scope.NewGuard(main)
	if err != nil {
		ts.errs = append(ts.errs, findings.ScanError{Path: main, Message: fmt.Sprintf("main worktree not allowed: %v", err)})
		return
	}
	ts.repoMeta = append(ts.repoMeta, g.Allowed()[0])
}

// isLinkedWorktree reports whether root's .git is a regular file, which is
// how git marks linked worktrees (and submodules). A normal repository has a
// .git directory and never triggers a git call.
func isLinkedWorktree(root string) bool {
	fi, err := os.Lstat(filepath.Join(root, ".git"))
	return err == nil && fi.Mode().IsRegular()
}

// linkedWorktreeMain returns the main worktree of the repository that root
// is a linked worktree of: the first entry of `git worktree list
// --porcelain`, run through the sanitized gitx runner.
func linkedWorktreeMain(ctx context.Context, runner gitx.Runner, root string) (string, error) {
	if runner == nil {
		return "", gitx.ErrGitNotFound
	}
	out, err := runner.Run(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return "", err
	}
	for _, line := range gitx.Lines(out) {
		if p, ok := strings.CutPrefix(line, "worktree "); ok {
			// git prints forward slashes on Windows as well.
			return filepath.FromSlash(p), nil
		}
	}
	return "", errors.New("git worktree list returned no worktree")
}

// configuredRoot is a configured root with its expanded path.
type configuredRoot struct {
	root config.Root
	path string
}

// workspaceTargets discovers repositories and project folders below the
// selected configured roots. Roots that are missing or unusable become scan
// errors and are skipped; only existing roots reach the guard.
func (a *app) workspaceTargets(ctx context.Context, cfg *config.Config) (*targetSet, error) {
	if len(cfg.Roots) == 0 {
		return nil, usageError{errNoRoots}
	}
	selected, err := selectRoots(cfg.Roots, a.flags.roots)
	if err != nil {
		return nil, err
	}
	ts := &targetSet{}
	var found []scope.Target
	for _, r := range selected {
		resolved, err := resolveExistingDir(r.path)
		if err != nil {
			ts.errs = append(ts.errs, findings.ScanError{Path: r.path, Message: "root skipped: " + err.Error()})
			continue
		}
		targets, errs := discoverRoot(ctx, cfg, r, resolved)
		if ctx.Err() != nil {
			return nil, errScanInterrupted
		}
		found = append(found, targets...)
		ts.errs = append(ts.errs, errs...)
		ts.allowed = append(ts.allowed, resolved)
	}
	if len(ts.allowed) == 0 {
		return nil, usageError{noUsableRootsError(ts.errs)}
	}
	ts.targets = mergeTargets(found)
	return ts, nil
}

// noUsableRootsError is errNoRoots plus the reasons the configured roots
// were skipped, so a stale path in the config is easy to spot.
func noUsableRootsError(errs []findings.ScanError) error {
	if len(errs) == 0 {
		return errNoRoots
	}
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = e.Path + ": " + e.Message
	}
	return fmt.Errorf("%w (%s)", errNoRoots, strings.Join(parts, "; "))
}

// discoverRoot runs scope.Discover for one root so its own excludes apply.
// Unreadable directories and root-level failures come back as scan errors.
func discoverRoot(ctx context.Context, cfg *config.Config, r configuredRoot, resolved string) ([]scope.Target, []findings.ScanError) {
	var (
		mu   sync.Mutex
		errs []findings.ScanError
	)
	add := func(path, msg string) {
		mu.Lock()
		defer mu.Unlock()
		errs = append(errs, findings.ScanError{Path: path, Message: msg})
	}
	targets, err := scope.Discover(ctx, []string{resolved}, scope.DiscoverOptions{
		MaxDepth:    cfg.Scan.MaxDepth,
		Exclude:     r.root.Exclude,
		SkipDirs:    cfg.Scan.SkipDirs,
		Concurrency: cfg.Scan.Concurrency,
		OnError:     func(path string, err error) { add(path, err.Error()) },
	})
	if err != nil && ctx.Err() == nil {
		add(resolved, err.Error())
	}
	return targets, errs
}

// mergeTargets deduplicates targets found under several roots by path and
// kind, preferring the innermost root as scope, and sorts them so the order
// is stable across runs.
func mergeTargets(in []scope.Target) []scope.Target {
	type key struct {
		path string
		kind scope.TargetKind
	}
	best := map[key]scope.Target{}
	for _, t := range in {
		k := key{t.Path, t.Kind}
		if cur, ok := best[k]; !ok || len(t.Scope.Path) > len(cur.Scope.Path) {
			best[k] = t
		}
	}
	out := make([]scope.Target, 0, len(best))
	for _, t := range best {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// resolveExistingDir makes path absolute and symlink-resolved and checks
// that it is a directory.
func resolveExistingDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	// resolved is already symlink-free, so classify it the way walk and trash
	// do (reparse-aware) instead of by a following Stat.
	if _, err := os.Lstat(resolved); err != nil {
		return "", err
	}
	if !walk.IsDirNoFollow(resolved) {
		return "", errors.New("not a directory")
	}
	return resolved, nil
}

// selectRoots returns the configured roots to scan: all of them, or with
// --root only those the given values name. A value must match a configured
// root after expansion and symlink resolution; ad-hoc scanning of
// unconfigured directories is deliberately impossible.
func selectRoots(roots []config.Root, wanted []string) ([]configuredRoot, error) {
	all := make([]configuredRoot, 0, len(roots))
	for i, r := range roots {
		p, err := r.ResolvedPath()
		if err != nil {
			return nil, fmt.Errorf("roots[%d].path: %w", i, err)
		}
		all = append(all, configuredRoot{root: r, path: p})
	}
	if len(wanted) == 0 {
		return all, nil
	}
	var out []configuredRoot
	picked := map[int]bool{}
	for _, w := range wanted {
		idx, err := matchRoot(all, w)
		if err != nil {
			return nil, err
		}
		if !picked[idx] {
			picked[idx] = true
			out = append(out, all[idx])
		}
	}
	return out, nil
}

// matchRoot finds the configured root that value names.
func matchRoot(all []configuredRoot, value string) (int, error) {
	expanded, err := config.ExpandPath(value)
	if err != nil {
		return 0, usageError{fmt.Errorf("--root %q: %w", value, err)}
	}
	for i, r := range all {
		if sameDir(expanded, r.path) {
			return i, nil
		}
	}
	paths := make([]string, len(all))
	for i, r := range all {
		paths[i] = r.path
	}
	return 0, usageError{fmt.Errorf("--root %q is not a configured root (configured: %s); add it with `brooom roots add <path>`", value, strings.Join(paths, ", "))}
}

// sameDir reports whether a and b name the same directory: by os.SameFile
// when both exist, otherwise by their cleaned absolute spelling.
func sameDir(a, b string) bool {
	ra, rb := canonical(a), canonical(b)
	if fa, err := os.Stat(ra); err == nil {
		if fb, err := os.Stat(rb); err == nil {
			return os.SameFile(fa, fb)
		}
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.EqualFold(ra, rb)
	}
	return ra == rb
}

// canonical returns the absolute, symlink-resolved (where possible) and
// cleaned form of p.
func canonical(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

// userKey identifies an extra target for deduplication. The path alone is not
// enough: two catalog tools may share a base directory (an extra tool matching
// `~/.claude/projects/*/*.log` next to claude-code) and each detector only
// enumerates the locations of its own tool, so dropping the second target hid
// its files and made results depend on catalog order. Targets already in the
// set (repositories, projects) carry an empty detector and tool.
type userKey struct{ detector, tool, path string }

// userTargetKey builds the deduplication key of t as declared by detector.
func userTargetKey(detector string, t scope.Target) userKey {
	return userKey{detector: detector, tool: t.Tool, path: t.Path}
}

// addExtraTargets asks every selected, globally enabled detector that
// implements detect.TargetSource for its user-level targets, appends them
// (deduplicated by detector, tool and path), and allows their paths in the guard. Locations
// that do not exist are dropped silently; every other problem is a scan
// error and never aborts the scan.
func (ts *targetSet) addExtraTargets(ctx context.Context, cfg *config.Config, detectors []detect.Detector) {
	seen := map[userKey]bool{}
	for _, t := range ts.targets {
		seen[userTargetKey("", t)] = true
	}
	for _, d := range detectors {
		src, ok := d.(detect.TargetSource)
		if !ok || !detectorEnabled(cfg, d.Name()) {
			continue
		}
		extra, err := src.ExtraTargets(ctx, cfg)
		if err != nil {
			ts.errs = append(ts.errs, findings.ScanError{Detector: d.Name(), Message: "extra targets: " + err.Error()})
		}
		for _, t := range extra {
			ts.addUserTarget(d.Name(), t, seen)
		}
	}
}

// addUserTarget validates one extra target and appends it. Only user-level
// targets are accepted: a detector must not widen the scan to repositories.
func (ts *targetSet) addUserTarget(detector string, t scope.Target, seen map[userKey]bool) {
	if t.Kind != scope.TargetUser {
		ts.errs = append(ts.errs, findings.ScanError{Detector: detector, Path: t.Path, Message: fmt.Sprintf("extra target of kind %q ignored: only user targets are allowed", t.Kind)})
		return
	}
	resolved, ok := ts.allow(t.Path, "user location", true)
	if !ok {
		return
	}
	t.Path = resolved
	key := userTargetKey(detector, t)
	if seen[key] {
		return
	}
	seen[key] = true
	t.Scope = findings.Scope{Type: findings.ScopeUser, Path: resolved}
	ts.targets = append(ts.targets, t)
}

// effectiveConfigs computes the effective configuration of every target once
// so Applies stays cheap. A ForTarget error (for example a .brooom.json that
// tries to loosen a rule) becomes a scan error and drops that target; the
// others still run. User-level targets have no overlay and use the global
// configuration.
func effectiveConfigs(cfg *config.Config, targets []scope.Target) (map[string]*config.Config, []scope.Target, []findings.ScanError) {
	eff := make(map[string]*config.Config, len(targets))
	kept := make([]scope.Target, 0, len(targets))
	var errs []findings.ScanError
	for _, t := range targets {
		if t.Kind == scope.TargetUser {
			eff[t.Path] = cfg
			kept = append(kept, t)
			continue
		}
		hint := ""
		if t.Scope.Type == findings.ScopeRoot {
			hint = t.Scope.Path
		}
		c, err := cfg.ForTarget(hint, t.Path)
		if err != nil {
			errs = append(errs, findings.ScanError{Path: t.Path, Message: "target skipped: " + err.Error()})
			continue
		}
		eff[t.Path] = c
		kept = append(kept, t)
	}
	return eff, kept, errs
}

// detectorEnabled reports whether a detector is enabled in cfg. Detectors
// unknown to the configuration have no toggle and therefore stay enabled;
// the known ones follow their config key.
func detectorEnabled(cfg *config.Config, name string) bool {
	for _, known := range config.DetectorNames() {
		if known == name {
			return cfg.DetectorEnabled(name)
		}
	}
	return true
}

// appliesFunc builds RunOptions.Applies: a detector runs on a target only
// if the target's effective config enables it, and git detectors only run
// on repo targets.
func appliesFunc(effective map[string]*config.Config) func(detect.Detector, scope.Target) bool {
	return func(d detect.Detector, t scope.Target) bool {
		if d.Category() == detect.CategoryGit && t.Kind != scope.TargetRepo {
			return false
		}
		cfg, ok := effective[t.Path]
		return ok && detectorEnabled(cfg, d.Name())
	}
}
