package gitx

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	// ErrNotRepo is returned when a directory is not inside a git repository.
	ErrNotRepo = errors.New("gitx: not a git repository")
	// ErrBareRepo is returned for bare repositories, which have no working
	// tree and therefore nothing Brooom sweeps.
	ErrBareRepo = errors.New("gitx: bare repository")
	// ErrUnsafeRepo matches (errors.Is) the *UnsafeRepoError returned for a
	// repository git refuses to touch because another user owns it ("dubious
	// ownership", typical on WSL, exFAT or network drives and in containers).
	ErrUnsafeRepo = errors.New("gitx: repository has dubious ownership")
)

// UnsafeRepoError is a repository that git refuses because of dubious
// ownership. It keeps git's own stderr and names the command that would make
// git trust the directory; Brooom never runs it, changing git's trust
// settings is the user's decision.
type UnsafeRepoError struct {
	// Dir is the directory git was asked about.
	Dir string
	// Stderr is git's message, trimmed.
	Stderr string
}

func (e *UnsafeRepoError) Error() string {
	return fmt.Sprintf("dubious ownership in %s (git: %s); to trust it run: %s",
		e.Dir, oneLine(e.Stderr), safeDirectoryCommand(e.Dir, runtime.GOOS))
}

// safeDirectoryCommand is the pasteable command that trusts dir. The path is
// quoted for the shell of goos, otherwise a directory with spaces or shell
// metacharacters would be split into several arguments.
func safeDirectoryCommand(dir, goos string) string {
	return "git config --global --add safe.directory " + quoteArg(dir, goos)
}

// quoteArg quotes s as one shell word: double quotes on Windows (cmd and
// PowerShell agree on them, and paths cannot contain a double quote there),
// POSIX single quotes elsewhere. Words made of safe characters stay bare.
func quoteArg(s, goos string) string {
	bare := s != "" && strings.IndexFunc(s, func(r rune) bool { return !bareArgRune(r, goos) }) < 0
	switch {
	case bare:
		return s
	case goos == "windows":
		return `"` + s + `"`
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// bareArgRune reports whether r needs no quoting in a shell word.
func bareArgRune(r rune, goos string) bool {
	alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
	return alnum || strings.ContainsRune("/._-:+@%", r) || goos == "windows" && r == '\\'
}

// Is makes errors.Is(err, ErrUnsafeRepo) true.
func (e *UnsafeRepoError) Is(target error) bool { return target == ErrUnsafeRepo }

// oneLine folds git's multi-line message into one line for a report.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// memo caches the result of one computation per key. Concurrent callers of
// the same key block on a single execution (per-key sync.Once), so expensive
// git queries run once per scan even when several detectors ask at once.
type memo[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]*memoEntry[V]
}

type memoEntry[V any] struct {
	once sync.Once
	val  V
	err  error
}

// do returns the cached result for key or computes it with f.
func (m *memo[K, V]) do(key K, f func() (V, error)) (V, error) {
	m.mu.Lock()
	if m.m == nil {
		m.m = make(map[K]*memoEntry[V])
	}
	e, ok := m.m[key]
	if !ok {
		e = &memoEntry[V]{}
		m.m[key] = e
	}
	m.mu.Unlock()
	e.once.Do(func() { e.val, e.err = f() })
	return e.val, e.err
}

// forget drops the memoized result for key, so the next caller recomputes it.
// Used when a result only reflects a cancelled context and must not stick.
func (m *memo[K, V]) forget(key K) {
	m.mu.Lock()
	delete(m.m, key)
	m.mu.Unlock()
}

// Repo is a handle on one git repository. Handles from Open are uncached:
// every query hits git, which is what actions need because they must
// re-validate against the live repository. Handles from Cache.Repo memoize
// expensive queries and are safe for concurrent use.
type Repo struct {
	// Runner runs the underlying git commands.
	Runner Runner
	// Dir is the top level of the worktree this handle was opened from. Any
	// worktree of the repository answers repository-wide queries alike.
	Dir string
	// Common is the resolved common git directory; it identifies the
	// repository across all of its worktrees.
	Common string

	memoize bool
	// known is the git version a Cache resolved once for all its handles;
	// nil means the handle asks git itself.
	known *Version
	// verdicts is the on-disk squash verdict cache of a scan's Cache; nil for
	// uncached handles, which must always recompute.
	verdicts *verdictStore
	// gh is the scan-wide breaker shared by all handles of one Cache; nil
	// for uncached handles, which always call gh.
	gh *ghBreaker

	// diffLimit overrides maxDiffBytes when positive (tests only).
	diffLimit int64

	version        memo[struct{}, Version]
	branches       memo[struct{}, []Branch]
	remoteBranches memo[struct{}, []RemoteBranch]
	worktrees      memo[struct{}, []Worktree]
	bases          memo[string, Base]
	remoteHolder   memo[string, string]
	unpushed       memo[string, int]
	patches        patchCache
	commits        memo[string, string]
	refTips        memo[struct{}, map[string]string]
	mergedRefs     memo[string, map[string]struct{}]
	behind         memo[string, map[string]int]
	merged         memo[mergeKey, MergeResult]
	prs            memo[struct{}, PRInfo]
	objectStats    memo[struct{}, ObjectStats]
	extra          memo[string, any]
}

// cached runs f through m when the handle memoizes, otherwise directly.
func cached[K comparable, V any](r *Repo, m *memo[K, V], key K, f func() (V, error)) (V, error) {
	if !r.memoize {
		return f()
	}
	return m.do(key, f)
}

// Open opens the repository containing dir without memoization.
func Open(ctx context.Context, r Runner, dir string) (*Repo, error) {
	return open(ctx, r, dir, false)
}

func open(ctx context.Context, r Runner, dir string, memoize bool) (*Repo, error) {
	top, common, err := resolveRepo(ctx, r, dir)
	if err != nil {
		return nil, err
	}
	repo := &Repo{Runner: r, Dir: top, Common: common, memoize: memoize}
	if err := repo.requireGit(ctx); err != nil {
		return nil, err
	}
	return repo, nil
}

// resolveRepo returns the top level and common dir of the repository around
// dir with one `git rev-parse` instead of three. Any failure (not a
// repository, or a bare one, where --show-toplevel is fatal) falls back to the
// individual queries, which produce the precise ErrNotRepo / ErrBareRepo.
func resolveRepo(ctx context.Context, r Runner, dir string) (top, common string, err error) {
	out, cerr := r.Run(ctx, dir, "rev-parse", "--is-bare-repository", "--show-toplevel", "--git-common-dir")
	if lines := Lines(out); cerr == nil && len(lines) == 3 && lines[0] == "false" {
		return NormalizePath(lines[1]), absCommonDir(dir, lines[2]), nil
	}
	if top, err = TopLevel(ctx, r, dir); err != nil {
		return "", "", err
	}
	common, err = CommonDir(ctx, r, dir)
	return top, common, err
}

// absCommonDir makes the --git-common-dir output absolute relative to dir.
func absCommonDir(dir, out string) string {
	p := filepath.FromSlash(out)
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return NormalizePath(p)
}

// run executes git in the handle's worktree.
func (r *Repo) run(ctx context.Context, args ...string) (string, error) {
	return r.Runner.Run(ctx, r.Dir, args...)
}

// TopLevel returns the clean absolute top-level directory of the worktree
// containing dir. Bare repositories yield ErrBareRepo and directories outside
// a repository ErrNotRepo.
func TopLevel(ctx context.Context, r Runner, dir string) (string, error) {
	bare, err := r.Run(ctx, dir, "rev-parse", "--is-bare-repository")
	if err != nil {
		return "", mapNotRepo(dir, err)
	}
	if bare == "true" {
		return "", ErrBareRepo
	}
	out, err := r.Run(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", mapNotRepo(dir, err)
	}
	return NormalizePath(out), nil
}

// CommonDir returns the clean absolute common git directory of the
// repository containing dir; all worktrees of one repository share it.
func CommonDir(ctx context.Context, r Runner, dir string) (string, error) {
	out, err := r.Run(ctx, dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", mapNotRepo(dir, err)
	}
	return absCommonDir(dir, out), nil
}

// MainWorktree returns the path of the main worktree of the repository dir
// belongs to, so a linked worktree and its main checkout yield the same path.
func MainWorktree(ctx context.Context, r Runner, dir string) (string, error) {
	repo, err := Open(ctx, r, dir)
	if err != nil {
		return "", err
	}
	return repo.MainWorktree(ctx)
}

// MainWorktree returns the path of the repository's main worktree, the first
// entry of `git worktree list`.
func (r *Repo) MainWorktree(ctx context.Context) (string, error) {
	wts, err := r.ListWorktrees(ctx)
	if err != nil {
		return "", err
	}
	if len(wts) == 0 {
		return "", ErrNotRepo
	}
	if wts[0].Bare {
		return "", ErrBareRepo
	}
	return wts[0].Path, nil
}

// NormalizePath converts a path printed by git (forward slashes on Windows)
// into a clean native path with symlinks resolved. When resolution fails, for
// example because the directory is gone, the cleaned path is returned.
func NormalizePath(p string) string {
	p = filepath.Clean(filepath.FromSlash(p))
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}

// mapNotRepo classifies a failed repository probe by git's stderr, because the
// exit status is 128 for every fatal error. Only "not a git repository" is
// ErrNotRepo; dubious ownership becomes an *UnsafeRepoError so callers can
// report it instead of silently treating the directory as no repository.
// Everything else (permissions, corrupt repository, cancelled context,
// missing binary) passes through with git's stderr intact.
func mapNotRepo(dir string, err error) error {
	var gerr *Error
	if !errors.As(err, &gerr) {
		return err
	}
	switch {
	case strings.Contains(gerr.Stderr, "dubious ownership"):
		return &UnsafeRepoError{Dir: dir, Stderr: strings.TrimSpace(gerr.Stderr)}
	case strings.Contains(gerr.Stderr, "not a git repository"):
		return ErrNotRepo
	}
	return err
}

// Cache hands out one shared, memoizing Repo per repository (keyed by
// CommonDir). One Cache lives for one scan.
type Cache struct {
	runner Runner
	mu     sync.Mutex
	repos  map[string]*Repo
	// byDir remembers the handle per directory asked for, so the many
	// env.Repo calls of one scan cost no git process after the first.
	byDir map[string]*Repo
	// gh is the breaker all handles of this scan share.
	gh *ghBreaker

	// verMu guards ver and verOK. It is separate from mu so resolving the
	// version never stalls lookups of already known repositories.
	verMu sync.Mutex
	ver   Version
	verOK bool

	// verdicts is the optional on-disk squash verdict cache (nil: off).
	verdicts *verdictStore
}

// NewCache returns an empty cache using runner for all repositories.
func NewCache(runner Runner) *Cache {
	return &Cache{runner: runner, repos: make(map[string]*Repo), byDir: make(map[string]*Repo), gh: &ghBreaker{}}
}

// Repo returns the shared handle for the repository containing dir. Linked
// worktrees of one repository share the handle of the first one seen. Git is
// only asked outside the cache lock, so a slow repository never stalls the
// lookups of the others.
func (c *Cache) Repo(ctx context.Context, dir string) (*Repo, error) {
	c.mu.Lock()
	r, ok := c.byDir[dir]
	c.mu.Unlock()
	if ok {
		return r, nil
	}
	top, common, err := resolveRepo(ctx, c.runner, dir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	r, ok = c.repos[common]
	c.mu.Unlock()
	if !ok {
		if r, err = c.newRepo(ctx, top, common); err != nil {
			return nil, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// Another goroutine may have built the handle meanwhile; keep the first
	// so every caller shares one set of memos.
	if existing, ok := c.repos[common]; ok {
		r = existing
	} else {
		c.repos[common] = r
	}
	c.byDir[dir] = r
	return r, nil
}

// newRepo builds a memoizing handle whose version is pre-seeded from the
// cache-wide result.
func (c *Cache) newRepo(ctx context.Context, top, common string) (*Repo, error) {
	v, err := c.gitVersion(ctx)
	if err != nil {
		return nil, err
	}
	r := &Repo{Runner: c.runner, Dir: top, Common: common, memoize: true, gh: c.gh, known: &v, verdicts: c.verdicts}
	if err := r.requireGit(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// gitVersion runs `git version` once per Cache. Only a success is kept, so a
// cancelled context fails that one lookup instead of poisoning the scan;
// concurrent callers wait for the first attempt rather than each spawning git.
// A waiter blocks on verMu without watching its own ctx: the holder runs one
// short `git version` bounded by its own ctx, so the wait is brief and making
// it interruptible would need a channel-based lock for no real gain.
func (c *Cache) gitVersion(ctx context.Context) (Version, error) {
	c.verMu.Lock()
	defer c.verMu.Unlock()
	if c.verOK {
		return c.ver, nil
	}
	v, err := GitVersion(ctx, c.runner)
	if err != nil {
		return Version{}, fmt.Errorf("gitx: cannot determine the git version (Brooom needs git %s or newer): %w", MinGitVersion, err)
	}
	c.ver, c.verOK = v, true
	return v, nil
}

// gitVersion returns the (memoized) git version, or the zero Version when it
// cannot be determined, which makes every feature check take the fallback.
// Opening a repository already refuses unusable versions (requireGit).
func (r *Repo) gitVersion(ctx context.Context) Version {
	v, _ := r.gitVersionErr(ctx)
	return v
}

func (r *Repo) gitVersionErr(ctx context.Context) (Version, error) {
	if r.known != nil {
		return *r.known, nil
	}
	return cached(r, &r.version, struct{}{}, func() (Version, error) {
		return GitVersion(ctx, r.Runner)
	})
}

// requireGit fails when the git binary is older than MinGitVersion or its
// version cannot be determined or parsed. It runs when a repository handle is
// opened, so every detector and action is covered once instead of each git
// feature guessing at a zero version.
func (r *Repo) requireGit(ctx context.Context) error {
	v, err := r.gitVersionErr(ctx)
	if err != nil {
		return fmt.Errorf("gitx: cannot determine the git version (Brooom needs git %s or newer): %w", MinGitVersion, err)
	}
	if !v.AtLeast(MinGitVersion.Major, MinGitVersion.Minor) {
		return fmt.Errorf("%w: found git %s, Brooom needs %s or newer", ErrGitTooOld, v, MinGitVersion)
	}
	return nil
}

// resolveCommit resolves ref to a full commit SHA without side effects.
// Memoized on cached handles, where refs are stable for the length of a scan.
func (r *Repo) resolveCommit(ctx context.Context, ref string) (string, error) {
	return cached(r, &r.commits, ref, func() (string, error) {
		out, err := r.run(ctx, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(out), nil
	})
}

// refExists reports whether the fully qualified ref resolves to a commit.
func (r *Repo) refExists(ctx context.Context, ref string) bool {
	_, err := r.resolveCommit(ctx, ref)
	return err == nil
}
