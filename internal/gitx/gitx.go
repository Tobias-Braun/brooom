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
	"os"
	"os/exec"
	"strings"
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
	full := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, r.Path, full...)
	cmd.Env = Env(os.Environ())
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", &Error{Args: args, Dir: dir, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
		}
		return "", err
	}
	return strings.TrimRight(stdout.String(), "\r\n"), nil
}

// Env returns base with the variables Brooom forces for every git call
// appended (later entries win in os/exec).
func Env(base []string) []string {
	return append(append([]string(nil), base...),
		"GIT_OPTIONAL_LOCKS=0",
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
