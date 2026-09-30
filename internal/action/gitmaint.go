package action

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/session"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// The three git maintenance actions are the only Brooom operations that
// destroy data which is otherwise recoverable: unreachable objects and reflog
// recovery points. They are therefore never part of a default run, every step
// is validated with git's own dry-run mode at plan time (which is also how
// dates are validated: Brooom has no date parser of its own), and every
// manifest entry says that it is not restorable.
//
// Ordering and serialization: the executor sorts steps by action priority
// (reflog-expire, prune, gc; see actionPriority) so prune and gc see the
// expired reflog, and it runs steps strictly one after another. Two
// maintenance steps therefore never run on the same repository concurrently;
// any future parallel executor must keep per-repository serialization,
// because git gc, prune and reflog expire race on the same object store.

// ErrInvalidDate is wrapped by date validation failures that are the
// caller's mistake (empty, option-like or rejected by git), so commands can
// report them as usage errors before anything is planned.
var ErrInvalidDate = errors.New("invalid git date")

// Finding args that carry the dates. They are the keys the git-bloat detector
// writes into SuggestedAction.Args.
const (
	argExpire = "expire"
	argPrune  = "prune"
)

// maxSizedObjects bounds how many unreachable objects are sized with
// cat-file: a repository with millions of dangling objects must not make
// planning take minutes. The count is always exact, only the size is then a
// lower bound.
const maxSizedObjects = 2000

func init() {
	Register(gitGC{})
	Register(gitPrune{})
	Register(gitReflogExpire{})
}

// CheckDateSyntax is the static part of ValidateDate, for callers that must
// reject a value before any repository is known.
func CheckDateSyntax(date string) error { return checkDateSyntax(date) }

// ValidateDate checks a git date for the expiry of prune and reflog expire.
// Static rejections come first (empty, no letters or digits, a leading dash
// that could be parsed as an option, control characters), then git itself is
// asked with a dry run of `git prune -n --expire=<date>` in dir, so every
// date git understands is accepted and everything else is refused with git's
// own message. The date is always passed as the value of an `--expire=` style
// argument, never as a separate argument.
func ValidateDate(ctx context.Context, r gitx.Runner, dir, date string) error {
	if err := checkDateSyntax(date); err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "prune", "-n", "--expire="+date); err != nil {
		return dateRejected(err, date)
	}
	return nil
}

// checkDateSyntax is the static part of ValidateDate.
func checkDateSyntax(date string) error {
	if strings.TrimSpace(date) == "" {
		return fmt.Errorf("%w: the date is empty", ErrInvalidDate)
	}
	if strings.HasPrefix(strings.TrimSpace(date), "-") {
		return fmt.Errorf("%w: %q starts with '-' and could be taken for an option", ErrInvalidDate, date)
	}
	hasAlnum := false
	for _, r := range date {
		if unicode.IsControl(r) {
			return fmt.Errorf("%w: %q contains control characters", ErrInvalidDate, date)
		}
		hasAlnum = hasAlnum || unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	if !hasAlnum {
		return fmt.Errorf("%w: %q contains no letters or digits", ErrInvalidDate, date)
	}
	return nil
}

// dateRejected turns a failing dry run into an ErrInvalidDate quoting git.
func dateRejected(err error, date string) error {
	var ge *gitx.Error
	if errors.As(err, &ge) {
		return fmt.Errorf("%w: git rejected %q: %s", ErrInvalidDate, date, firstLine(ge.Stderr))
	}
	return err
}

// firstLine returns the first non-empty line of s, or "no message".
func firstLine(s string) string {
	if l := gitx.Lines(s); len(l) > 0 {
		return strings.TrimSpace(l[0])
	}
	return "no message"
}

// maintCtx is a validated maintenance target.
type maintCtx struct {
	repo *gitx.Repo
	date string
}

// maintBase is the shared behaviour of the three actions: it resolves and
// validates the target the same way at plan and at apply time.
type maintBase struct {
	typ findings.ActionType
	// dateArg names the finding arg holding the date and defaultDate returns
	// the configured fallback used when the arg is absent.
	dateArg     string
	defaultDate func(config.GitBloat) string
}

// gitBloatConfig returns the effective git-bloat settings; a missing config
// means the defaults, so tests and callers without one still get safe values.
func gitBloatConfig(env *Env) config.GitBloat {
	if env.Config != nil {
		return env.Config.Detectors.GitBloat
	}
	return config.Default().Detectors.GitBloat
}

// targetGitBloat is gitBloatConfig for the repository of a finding: the
// per-root overrides and the tighten-only .brooom.json apply, so the
// fallback date is the one a scan of that repository would have used. A
// configuration that cannot be loaded falls back to the base one rather than
// to anything the finding says.
func targetGitBloat(env *Env, f findings.Finding) config.GitBloat {
	if env.Config == nil {
		return gitBloatConfig(env)
	}
	if cfg, err := env.Config.ForTarget("", f.Path); err == nil {
		return cfg.Detectors.GitBloat
	}
	return gitBloatConfig(env)
}

// date returns the date of the operation: the arg when present (an empty arg
// is an error, not a request for the default), else the configured value.
//
// The arg is only trustworthy from the callers that build the finding
// themselves (`brooom git purge` with an explicit date, the detector with
// the configured value). `brooom clean --from` strips it from findings read
// from a file, so a forged {"expire": "now"} never reaches this point and the
// configured expiry applies.
func (b maintBase) date(env *Env, f findings.Finding) string {
	if v, ok := f.SuggestedAction.Args[b.dateArg]; ok {
		return v
	}
	return b.defaultDate(targetGitBloat(env, f))
}

// prepare re-validates a finding: type, risk flags, scope, repository root,
// date and operations in progress. It runs git only through env.Git.
func (b maintBase) prepare(ctx context.Context, env *Env, f findings.Finding) (*maintCtx, error) {
	if f.SuggestedAction.Type != b.typ {
		return nil, skipf("finding suggests %q, not %s", f.SuggestedAction.Type, b.typ)
	}
	if !findings.Actionable(f.RiskFlags, env.Force) {
		return nil, skipf("%s", blockedReason(f.RiskFlags, env.Force))
	}
	repo, err := openMaintRepo(ctx, env, f.Path)
	if err != nil {
		return nil, err
	}
	date := b.date(env, f)
	if err := ValidateDate(ctx, env.Git, repo.Dir, date); err != nil {
		return nil, skipf("%v", err)
	}
	if err := checkIdle(ctx, env, repo); err != nil {
		return nil, err
	}
	return &maintCtx{repo: repo, date: date}, nil
}

// openMaintRepo resolves path through the guard and requires it to be the
// root of a working tree: maintenance acts on the whole repository, so a
// subdirectory or a bare repository is refused.
func openMaintRepo(ctx context.Context, env *Env, path string) (*gitx.Repo, error) {
	if env.Guard == nil {
		return nil, errors.New("git maintenance: no scope guard configured")
	}
	if env.Git == nil {
		return nil, errors.New("git maintenance: no git runner configured")
	}
	repo, err := openRepoDir(ctx, env, path)
	if err != nil {
		return nil, err
	}
	resolved, err := env.Guard.Resolve(path)
	if err != nil {
		return nil, skipf("%s", errOutsideScope)
	}
	if !gitx.SamePath(repo.Dir, resolved) {
		return nil, skipf("%s is not the root of a git repository (root: %s)", resolved, repo.Dir)
	}
	return repo, nil
}

// checkIdle refuses to run while a rebase, merge, cherry-pick, revert or
// bisect is in progress in the repository or any of its worktrees. Pruning
// or expiring then can delete objects the operation still needs. It applies
// to gc as well, because gc prunes and expires too.
func checkIdle(ctx context.Context, env *Env, repo *gitx.Repo) error {
	dir, err := gitx.GitDir(ctx, env.Git, repo.Dir)
	if err != nil {
		return fmt.Errorf("git maintenance: locate git directory of %s: %w", repo.Dir, err)
	}
	dirs := append([]string{dir}, gitx.WorktreeGitDirs(repo.Common)...)
	if op, where, ok := gitx.OperationInProgress(dirs...); ok {
		return skipf("a %s is in progress (%s); finish or abort it first", op, filepath.Base(where))
	}
	return nil
}

// runMaint runs one maintenance command and maps errors: a running or stale
// gc is a skip quoting git, everything else names the repository (on Windows
// a running git gc holding files ends up here).
func runMaint(ctx context.Context, env *Env, repo *gitx.Repo, args ...string) error {
	// gc on a large repository outlasts the scan timeout, so maintenance gets
	// its own, much longer bound (a caller's deadline still wins).
	_, err := env.Git.Run(gitx.WithTimeout(ctx, gitx.MaintenanceTimeout), repo.Dir, args...)
	if err == nil {
		return nil
	}
	var ge *gitx.Error
	if errors.As(err, &ge) {
		msg := strings.TrimSpace(ge.Stderr)
		low := strings.ToLower(msg)
		if strings.Contains(low, "already running") || strings.Contains(low, "gc.pid") {
			return skipf("git refused because another gc is running: %s", firstLine(msg))
		}
		return fmt.Errorf("git %s in %s: %s (is another git process, for example a running git gc, using the repository?)",
			strings.Join(args, " "), repo.Dir, firstLine(msg))
	}
	return fmt.Errorf("git %s in %s: %w", strings.Join(args, " "), repo.Dir, err)
}

// baseEntry is the manifest entry every outcome starts from.
func baseEntry(typ findings.ActionType, f findings.Finding) session.Entry {
	return session.Entry{
		FindingID: f.ID, Detector: f.Detector, Action: typ,
		Path: f.Path, Ref: f.Ref, At: trashNow().UTC(),
	}
}

// applyMaint is the shared Apply: re-validate, measure, run, measure. A skip
// is reported as a skipped entry without error, a failure as a failed entry.
func applyMaint(ctx context.Context, env *Env, b maintBase, s Step, measureLogs bool, hint func(date string) string, args func(date string) []string) (session.Entry, error) {
	f := s.Finding
	en := baseEntry(b.typ, f)
	m, err := b.prepare(ctx, env, f)
	if errors.Is(err, ErrSkipped) {
		en.Status, en.Error = session.StatusSkipped, skipReason(err)
		return en, nil
	}
	if err != nil {
		return failedTrash(en, err)
	}
	before, err := measureStore(ctx, env, m.repo, measureLogs)
	if err != nil {
		return failedTrash(en, err)
	}
	if err := runMaint(ctx, env, m.repo, args(m.date)...); err != nil {
		if errors.Is(err, ErrSkipped) {
			en.Status, en.Error = session.StatusSkipped, skipReason(err)
			return en, nil
		}
		return failedTrash(en, err)
	}
	// Measuring after the fact must not turn a completed, irreversible
	// operation into a failure: a failing measurement only loses the size.
	after, merr := measureStore(ctx, env, m.repo, measureLogs)
	en.Status = session.StatusApplied
	en.Restorable = false
	en.RecoveryHint = hint(m.date)
	if merr == nil {
		en.SizeBytes = before.reclaimedBy(after)
	}
	return en, nil
}

// notRestorable is the error of all three Undo methods. It wraps
// trash.ErrNotRestorable so `brooom undo` treats it like every other
// entry that cannot be restored and surfaces the explanation.
func notRestorable(explanation string) error {
	return fmt.Errorf("%w: %s", trash.ErrNotRestorable, explanation)
}

// gcHint, gitPruneHint and reflogHint are the recovery texts of the manifest
// entries. The same wording explains the undo error and the dry run.
func gcHint(prune string) string {
	return "not restorable; unreachable objects older than " + prune + " are deleted, and reflog entries older than " +
		"gc.reflogExpire / gc.reflogExpireUnreachable (git defaults 90 / 30 days) are expired, so resets and deleted " +
		"branches older than that cannot be recovered via the reflog. Stash entries (refs/stash) are kept: Brooom " +
		"protects them for the run."
}

func gitPruneHint(date string) string {
	return "unreachable objects older than " + date + " were deleted for good; commits only reachable through them cannot be recovered"
}

func reflogHint(date string) string {
	return "reflog entries older than " + date + " were removed; deleted branches and reset commits older than this can no longer be recovered via the reflog; stash entries (refs/stash) were kept"
}

// Compile-time checks that the actions satisfy the interface.
var (
	_ Action = gitGC{}
	_ Action = gitPrune{}
	_ Action = gitReflogExpire{}
)
