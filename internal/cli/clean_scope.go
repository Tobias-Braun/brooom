package cli

import (
	"context"
	"fmt"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// cleanTargetSources lists the detectors asked for user-level locations when
// `clean --user` is given. It is a variable only so tests can inject fake
// detect.TargetSource implementations without touching the real home.
var cleanTargetSources = detect.All

// cleanScope is the scope of one `clean --from` invocation. It is built from
// the current working directory or the configured roots, exactly like a scan,
// and never from the findings file.
//
// Three guards exist on purpose. project allows only the repository (or the
// selected roots); it decides the fate of every finding that claims a
// repository or root scope. user allows only the user-level tool locations
// and is used for findings claiming the user scope, so a finding cannot pick
// the more permissive location by lying about its scope. guard is the union
// and is what the actions get.
type cleanScope struct {
	cfg     *config.Config
	git     gitx.Runner
	gitErr  error
	guard   *scope.Guard
	project *scope.Guard
	// user is nil unless --user was given and a user location exists.
	user *scope.Guard
	// userEnabled records --user, also when no user location exists.
	userEnabled bool
	// repos are the repository directories of this scope: git findings must
	// refer to one of them.
	repos []string
	// locations are the resolved project and user locations, for notes.
	locations []string
	// targetCfgs memoizes the effective configuration per scan target.
	targetCfgs map[string]*config.Config
}

// newCleanScope resolves the scope like the scan command: the repository
// around the working directory, or what --path names. Outside a repository
// without --path it fails with scope.ErrNotInRepo and the same explanation
// the scan gives.
func (a *app) newCleanScope(ctx context.Context, cfg *config.Config, user bool) (*cleanScope, error) {
	if user {
		cfg.Detectors.AIArtifacts.UserLocations = true
	}
	runner, gitErr := newGitRunner()
	ts, err := a.buildTargets(ctx, &scanRequest{cfg: cfg}, runner)
	if err != nil {
		return nil, err
	}
	sc := &cleanScope{cfg: cfg, git: runner, gitErr: gitErr, userEnabled: user}
	if sc.project, err = ts.newGuard(); err != nil {
		return nil, fmt.Errorf("build scope: %w", err)
	}
	sc.repos = repoDirs(ts)
	userAllowed := sc.userLocations(ctx, cfg)
	if err := sc.buildGuards(ts, userAllowed); err != nil {
		return nil, err
	}
	sc.locations = append(append([]string{}, ts.allowed...), userAllowed...)
	return sc, nil
}

// userLocations asks the target-source detectors for their user locations,
// only with --user. Missing locations are dropped and other problems are
// ignored: a location that cannot be allowed simply stays out of the guard,
// which makes findings there refused.
func (sc *cleanScope) userLocations(ctx context.Context, cfg *config.Config) []string {
	if !sc.userEnabled {
		return nil
	}
	us := &targetSet{}
	us.addExtraTargets(ctx, cfg, cleanTargetSources())
	return us.allowed
}

// buildGuards creates the user-only and the combined guard.
func (sc *cleanScope) buildGuards(ts *targetSet, user []string) error {
	project := ts.allowed
	var err error
	if len(user) > 0 {
		if sc.user, err = scope.NewGuard(user...); err != nil {
			return fmt.Errorf("build user scope: %w", err)
		}
	}
	if sc.guard, err = guardWithMeta(append(append([]string{}, project...), user...), ts.repoMeta); err != nil {
		return fmt.Errorf("build scope: %w", err)
	}
	return nil
}

// repoDirs returns the repositories of the scope. In repository mode every
// allowed location is one (the repository and, for a linked worktree, its
// main worktree); below a walked folder the discovered repository targets
// are.
func repoDirs(ts *targetSet) []string {
	var out []string
	if !ts.discovered {
		out = append(out, ts.allowed...)
		out = append(out, ts.repoMeta...)
	}
	for _, t := range ts.targets {
		if t.Kind == scope.TargetRepo {
			out = append(out, t.Path)
		}
	}
	return out
}

// isRepo reports whether resolved is one of the scope's repositories.
func (sc *cleanScope) isRepo(resolved string) bool {
	for _, r := range sc.repos {
		if gitx.SamePath(r, resolved) {
			return true
		}
	}
	return false
}

// knowsScope reports whether a scope path recorded in a file lies within the
// locations of this run.
func (sc *cleanScope) knowsScope(path string) bool {
	_, err := sc.guard.Resolve(path)
	return err == nil
}

// requireGit fails early and clearly when accepted findings need git but it
// is not installed, instead of failing every step.
func (sc *cleanScope) requireGit(accepted []findings.Finding) error {
	if sc.gitErr == nil {
		return nil
	}
	for _, f := range accepted {
		if isGitAction(f.SuggestedAction.Type) {
			return fmt.Errorf("the %s action needs git: %w", f.SuggestedAction.Type, sc.gitErr)
		}
	}
	return nil
}
