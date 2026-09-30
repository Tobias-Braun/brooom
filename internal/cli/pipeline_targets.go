package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	errNotInRepo  = fmt.Errorf("%w; run it inside a repo, or pass a folder to scan every repository below it (for example `brooom sweep ~/code`)", scope.ErrNotInRepo)
	errBareAnchor = errors.New("this folder is the bare repository of a bare plus linked worktrees layout and has no working tree; run brooom inside one of its worktrees or pass the folder above it as the path")
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
	// discovered is set when the targets were found by walking a folder
	// (the path argument outside a repository). The allowed location is then
	// that folder, not a repository.
	discovered bool
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
// the working directory by default, or what the path argument names.
func (a *app) buildTargets(ctx context.Context, req *scanRequest, runner gitx.Runner) (*targetSet, error) {
	if a.flags.path != "" {
		return a.pathTargets(ctx, req.cfg, runner, a.flags.path)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("determine working directory: %w", err)
	}
	return repoTargets(ctx, runner, cwd)
}

// repoTargets builds the repo target of the repository containing dir.
//
// Inside a linked worktree the repository's main worktree becomes a second
// repo target: the scope is the whole repository, so a sweep run from one of
// the worktrees an agent left behind also sees its siblings (typically below
// .claude/worktrees of the main checkout). Findings of the same branch reached
// through both targets carry one ID and are deduplicated by the engine, and
// the worktree the command runs in is never removed (it is in use). The main
// worktree is also registered as repository metadata location, which the git
// detectors resolve before they run git in it.
func repoTargets(ctx context.Context, runner gitx.Runner, dir string) (*targetSet, error) {
	root, err := scope.FindRepoRoot(dir)
	if errors.Is(err, scope.ErrNotInRepo) {
		return nil, usageError{errNotInRepo}
	}
	if err != nil {
		return nil, err
	}
	ts := &targetSet{
		targets: []scope.Target{repoTarget(root)},
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

// repoTarget is the repo target of a repository top-level directory, scoped
// to itself.
func repoTarget(root string) scope.Target {
	return scope.Target{
		Kind:  scope.TargetRepo,
		Path:  root,
		Scope: findings.Scope{Type: findings.ScopeRepo, Path: root},
	}
}

// pathTargets resolves the path argument. A path inside a repository scans
// that repository, exactly like running from there; any other folder is
// walked and every repository and project folder below it is scanned, with
// the folder as the only location the guard allows. Filesystem roots are
// refused: a scan of a whole disk is never what a typo meant.
func (a *app) pathTargets(ctx context.Context, cfg *config.Config, runner gitx.Runner, raw string) (*targetSet, error) {
	expanded, err := config.ExpandPath(raw)
	if err != nil {
		return nil, usageError{fmt.Errorf("path %q: %w", raw, err)}
	}
	resolved, err := resolveExistingDir(expanded)
	if err != nil {
		return nil, usageError{fmt.Errorf("path %q: %w", raw, err)}
	}
	if config.IsFilesystemRoot(resolved) {
		return nil, usageError{fmt.Errorf("path %q is a filesystem root; pass a workspace folder instead", raw)}
	}
	if _, err := scope.FindRepoRoot(resolved); err == nil {
		return repoTargets(ctx, runner, resolved)
	}
	targets, errs := discoverPath(ctx, cfg, resolved)
	if ctx.Err() != nil {
		return nil, errScanInterrupted
	}
	return &targetSet{targets: mergeTargets(targets), allowed: []string{resolved}, errs: errs, discovered: true}, nil
}

// allowMainWorktree adds the main worktree of the linked worktree at root as
// a second repo target and as repository metadata location (see repoTargets).
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
	main = g.Allowed()[0]
	ts.repoMeta = append(ts.repoMeta, main)
	// A bare repository (the .bare of a bare plus linked worktrees layout)
	// has no working tree to scan; it stays a metadata location only.
	if canonical(main) == canonical(root) || !hasGitEntry(main) {
		return
	}
	ts.allowed = append(ts.allowed, main)
	ts.targets = append(ts.targets, repoTarget(main))
}

// hasGitEntry reports whether dir holds a .git entry (directory or file), which
// a working tree has and a bare repository does not.
func hasGitEntry(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
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

// discoverPath runs scope.Discover below the path argument. Unreadable
// directories and failures of the walk come back as scan errors.
func discoverPath(ctx context.Context, cfg *config.Config, resolved string) ([]scope.Target, []findings.ScanError) {
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
		SkipDirs:    cfg.Scan.SkipDirs,
		Concurrency: cfg.Scan.Concurrency,
		OnError:     func(path string, err error) { add(path, err.Error()) },
	})
	if err != nil && ctx.Err() == nil {
		add(resolved, err.Error())
	}
	return targets, errs
}

// mergeTargets deduplicates targets by path and kind, preferring the
// innermost scope, and sorts them so the order is stable across runs.
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
		c, err := cfg.ForTarget(t.Path)
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
