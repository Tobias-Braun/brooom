// Package gitbloat implements the git-bloat detector: loose objects, many
// packs, oversized reflogs and huge blobs in history.
//
// The detector only reads. It runs read-only git commands through env.Git
// (GIT_OPTIONAL_LOCKS=0) and never runs gc, prune or reflog expire; the
// actions that do are separate. Savings are documented estimates, because an
// exact number would require actually running gc: the action measures the real
// value with count-objects before and after.
//
// All findings of a repository are attached to its main worktree, and the
// expensive measurements are memoized per repository (gitx.Repo.Memo), so every
// linked-worktree target of one repository yields identical finding IDs (the
// engine deduplicates them) and triggers no second scan.
package gitbloat

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// Name is the detector name used in findings and config.
const Name = config.DetectorGitBloat

// gcReflogNote is appended to the reason of every git-gc suggestion: gc does
// more than repacking, and users must know that before opting in.
const gcReflogNote = "git gc also expires reflog entries per gc.reflogExpire and gc.reflogExpireUnreachable " +
	"(defaults 90 and 30 days), so entries older than that can no longer be used to recover deleted branches or reset commits"

func init() { detect.Register(New()) }

// Detector is the git-bloat detector.
type Detector struct {
	// blobTimeout bounds the history scan per repository.
	blobTimeout time.Duration
}

// New returns the detector with the default blob scan timeout.
func New() *Detector { return &Detector{blobTimeout: blobScanTimeout} }

// Name implements detect.Detector.
func (*Detector) Name() string { return Name }

// Description implements detect.Detector.
func (*Detector) Description() string {
	return "Loose objects, many packs, oversized reflogs and large blobs in git history"
}

// Category implements detect.Detector.
func (*Detector) Category() detect.Category { return detect.CategoryGit }

// repoInfo is what one target resolves to before any check runs.
type repoInfo struct {
	repo *gitx.Repo
	// path is the guard-resolved main worktree all findings are attached to.
	path string
	// scope is the scope the finding is reported under.
	scope findings.Scope
	cfg   config.GitBloat
}

// Detect implements detect.Detector.
func (d *Detector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	err := d.detect(ctx, env, target, emit)
	if err != nil && ctx.Err() != nil {
		// A cancelled scan is reported as the context error, however deep
		// in the checks it was noticed.
		return ctx.Err()
	}
	return err
}

func (d *Detector) detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	info, err := d.resolve(ctx, env, target)
	if err != nil || info == nil {
		return err
	}
	fs, err := d.objectFindings(ctx, env, info)
	if err != nil {
		return err
	}
	rf, err := d.reflogFinding(ctx, env, info)
	if err != nil {
		return err
	}
	if rf != nil {
		fs = append(fs, *rf)
	}
	for _, f := range fs {
		emit(f)
	}
	blobs, err := d.blobFindings(ctx, env, info)
	if err != nil {
		return err
	}
	for _, f := range blobs {
		emit(f)
	}
	return nil
}

// resolve returns nil (and no error) for targets the detector silently
// skips: non-repo targets, bare or vanished repositories, disabled detector
// and repositories whose main worktree lies outside the allowed scope.
func (d *Detector) resolve(ctx context.Context, env *detect.Env, target scope.Target) (*repoInfo, error) {
	if target.Kind != scope.TargetRepo {
		return nil, nil
	}
	cfg, err := env.Config.ForTarget(target.Scope.Path, target.Path)
	if err != nil {
		return nil, fmt.Errorf("git-bloat: config for %s: %w", target.Path, err)
	}
	if !cfg.Detectors.GitBloat.Enabled {
		return nil, nil
	}
	repo, err := env.Repo(ctx, target.Path)
	if err != nil {
		return nil, skipNotRepo(err)
	}
	main, err := repo.MainWorktree(ctx)
	if err != nil {
		return nil, skipNotRepo(err)
	}
	// The guard decides whether a linked worktree whose main checkout lives
	// elsewhere may report on it; when not, stay silent instead of leaking
	// paths outside the scope.
	path, err := env.Guard.Resolve(main)
	if err != nil {
		return nil, nil //nolint:nilerr // out-of-scope repositories are skipped silently by design
	}
	return &repoInfo{repo: repo, path: path, scope: target.Scope, cfg: cfg.Detectors.GitBloat}, nil
}

// skipNotRepo maps "nothing to scan" errors to nil and passes real failures
// (cancelled context, missing git binary) on.
func skipNotRepo(err error) error {
	if errors.Is(err, gitx.ErrNotRepo) || errors.Is(err, gitx.ErrBareRepo) {
		return nil
	}
	return err
}

// base returns a finding with the fields every git-bloat finding shares.
func (i *repoInfo) base(kind findings.Kind, ref string) findings.Finding {
	return findings.Finding{
		ID:         findings.NewID(Name, kind, i.path, ref),
		Detector:   Name,
		Scope:      i.scope,
		Path:       i.path,
		Kind:       kind,
		Ref:        ref,
		Confidence: findings.ConfidenceMedium,
		Evidence:   []findings.Evidence{},
		RiskFlags:  []findings.RiskFlag{},
	}
}

// memoized runs f once per repository (per key) on cached handles.
func memoized[T any](r *gitx.Repo, key string, f func() (T, error)) (T, error) {
	v, err := r.Memo(key, func() (any, error) { return f() })
	if err != nil {
		var zero T
		return zero, err
	}
	return v.(T), nil
}

// humanBytes formats n for evidence messages.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTP"[exp])
}
