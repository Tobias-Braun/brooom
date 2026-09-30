// Package gitx runs git commands for detectors and actions.
//
// Brooom shells out to the git binary instead of using a Go git library: it
// guarantees identical behaviour to the user's git (worktrees, config,
// alternates, partial clones) and keeps the binary small.
//
// Commands run with a sanitized environment: no pager, no terminal prompts,
// C locale for parseable output and GIT_OPTIONAL_LOCKS=0 so read-only
// commands such as `git status` never take the index lock or rewrite the
// index in the user's repositories.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// ErrGitNotFound is returned when no git binary is on PATH.
var ErrGitNotFound = errors.New("git executable not found in PATH")

// Runner runs git commands. Implementations must be safe for concurrent use.
type Runner interface {
	// Run executes `git -C dir args...` and returns trimmed stdout. A
	// non-zero exit status returns an *Error carrying stderr.
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

// Error is returned when git exits with a non-zero status.
type Error struct {
	Args     []string
	Dir      string
	ExitCode int
	Stderr   string
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = fmt.Sprintf("exit status %d", e.ExitCode)
	}
	return fmt.Sprintf("git %s (in %s): %s", strings.Join(e.Args, " "), e.Dir, msg)
}

// ExecRunner runs the git binary found on PATH.
type ExecRunner struct {
	// Path is the git executable; empty means look up "git" on PATH.
	Path string
	// Timeout bounds every call whose context has no deadline of its own, so
	// a hung git (slow remote, stuck hook) cannot stall a scan forever. Zero
	// means DefaultTimeout, negative disables the bound.
	Timeout time.Duration
	// MaxOutput bounds the stdout of every call in bytes. A command that
	// prints more is killed at once and the call fails with ErrOutputLimit,
	// so unbounded output (`log -p` of a huge history) cannot exhaust memory.
	// Zero means unbounded.
	MaxOutput int64
}

// ErrOutputLimit is returned when a command's output exceeded the configured
// cap and the command was killed.
var ErrOutputLimit = errors.New("gitx: git output exceeds the size limit")

// DefaultTimeout is generous enough for gc on very large repositories while
// still ending a truly hung command.
const DefaultTimeout = 10 * time.Minute

func (r *ExecRunner) timeout() time.Duration {
	if r.Timeout == 0 {
		return DefaultTimeout
	}
	return r.Timeout
}

// NewExecRunner returns a runner for the git binary on PATH.
func NewExecRunner() (*ExecRunner, error) {
	p, err := exec.LookPath("git")
	if err != nil {
		return nil, ErrGitNotFound
	}
	return &ExecRunner{Path: p}, nil
}

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	return r.run(ctx, dir, nil, args)
}

// run is the single code path behind Run and RunInput so both use the same
// environment, error type and output trimming.
func (r *ExecRunner) run(ctx context.Context, dir string, stdin io.Reader, args []string) (string, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if _, ok := ctx.Deadline(); !ok && r.timeout() > 0 {
		var cancelTimeout context.CancelFunc
		ctx, cancelTimeout = context.WithTimeout(ctx, r.timeout())
		defer cancelTimeout()
	}
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, r.Path, full...)
	cmd.Env = Env(os.Environ())
	// A killed git can leave a child (hook, alias) holding the output pipes;
	// without a delay Wait would block until that child exits too.
	cmd.WaitDelay = waitDelay
	// Only set Stdin when input was given: an unset Stdin reads the null
	// device, so git can never block waiting on the terminal.
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	stdout := &limitBuffer{max: r.MaxOutput, cancel: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stdout.exceeded {
			return "", fmt.Errorf("git %s (in %s): %w", strings.Join(args, " "), dir, ErrOutputLimit)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &Error{Args: args, Dir: dir, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
		}
		return "", mapStartErr(err)
	}
	return strings.TrimRight(stdout.buf.String(), "\r\n"), nil
}

// limitBuffer is a bytes.Buffer with an optional cap. Going over it cancels
// the command's context (killing git) and fails the write, so at most max
// bytes are ever held.
type limitBuffer struct {
	buf      bytes.Buffer
	max      int64
	cancel   context.CancelFunc
	exceeded bool
}

func (b *limitBuffer) Write(p []byte) (int, error) {
	if b.max > 0 && int64(b.buf.Len()+len(p)) > b.max {
		b.exceeded = true
		b.cancel()
		return 0, ErrOutputLimit
	}
	return b.buf.Write(p)
}

// strippedEnvKeys are the exact variables that select or reconfigure the
// repository git operates on. They are dropped because tools that run inside
// hooks or direnv/dotfile setups export them (git itself sets GIT_DIR and
// GIT_INDEX_FILE for hooks), and with one pointing at another repository every
// command would silently read that repository's index and refs.
var strippedEnvKeys = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true,
	"GIT_COMMON_DIR": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_NAMESPACE": true,
	"GIT_PREFIX": true, "GIT_CEILING_DIRECTORIES": true,
	"GIT_DISCOVERY_ACROSS_FILESYSTEM": true,
	"GIT_GLOB_PATHSPECS":              true, "GIT_NOGLOB_PATHSPECS": true,
	"GIT_LITERAL_PATHSPECS": true, "GIT_ICASE_PATHSPECS": true,
	"GIT_CONFIG_PARAMETERS": true, "GIT_CONFIG_COUNT": true,
}

// stripped reports whether the environment entry must not reach git.
func stripped(entry string) bool {
	key, _, _ := strings.Cut(entry, "=")
	if runtime.GOOS == "windows" {
		key = strings.ToUpper(key)
	}
	return strippedEnvKeys[key] ||
		strings.HasPrefix(key, "GIT_CONFIG_KEY_") || strings.HasPrefix(key, "GIT_CONFIG_VALUE_")
}

// Env returns base without the repository-selecting and config-injecting GIT_*
// variables, followed by the variables Brooom forces for every git call (later
// entries win in os/exec).
//
// GIT_NO_LAZY_FETCH=1 keeps read-only commands from fetching missing objects
// from a promisor remote (git 2.44 or newer; older versions ignore it and a
// partial clone may still fetch). core.fsmonitor=false, passed through
// GIT_CONFIG_COUNT/KEY/VALUE, stops git from running a repository-configured
// fsmonitor hook during status. Inherited GIT_CONFIG_* entries are stripped
// first, so the forced pair is the only one and needs no merging.
func Env(base []string) []string {
	env := make([]string, 0, len(base)+10)
	for _, e := range base {
		if !stripped(e) {
			env = append(env, e)
		}
	}
	return append(env,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.fsmonitor",
		"GIT_CONFIG_VALUE_0=false",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PAGER=cat",
		"PAGER=cat",
		"LC_ALL=C",
		"LANGUAGE=C",
	)
}

// Lines splits git output into non-empty lines, handling CRLF.
func Lines(out string) []string {
	if out == "" {
		return nil
	}
	raw := strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n")
	lines := raw[:0]
	for _, l := range raw {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
