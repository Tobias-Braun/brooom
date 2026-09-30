package gitx

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// maxPipeLine bounds one output line of the consumer; object paths can be
// long but never approach this.
const maxPipeLine = 1 << 20

// gitPath returns the git executable of r when it is an *ExecRunner, else the
// one on PATH.
func gitPath(r Runner) (string, error) {
	if er, ok := r.(*ExecRunner); ok && er.Path != "" {
		return er.Path, nil
	}
	p, err := exec.LookPath("git")
	if err != nil {
		return "", ErrGitNotFound
	}
	return p, nil
}

// Pipe runs `git -C dir first...` and feeds its stdout straight into
// `git -C dir second...`, calling onLine for every line the second command
// prints (CRLF-tolerant). Both processes use the sanitized Env and no shell is
// involved. Runner only returns captured stdout, which for a history scan
// (rev-list piped into cat-file --batch-check) would mean holding millions of
// lines in memory, hence this streaming variant. It always runs the real git
// binary (the runner's path when it is an *ExecRunner), never a fake Runner.
//
// Cancelling ctx kills both processes and returns ctx.Err(). Lazy fetching of
// missing objects in partial clones is disabled so a scan never touches the
// network. A non-zero exit of either command returns an *Error.
func Pipe(ctx context.Context, r Runner, dir string, first, second []string, onLine func(string)) error {
	path, err := gitPath(r)
	if err != nil {
		return err
	}
	// GIT_NO_LAZY_FETCH needs git 2.44 or newer; older versions ignore it, which
	// is harmless because they only lazy-fetch when a promisor remote is set up.
	env := Env(os.Environ())
	var stderr1, stderr2 bytes.Buffer
	c1 := exec.CommandContext(ctx, path, append([]string{"-C", dir}, first...)...)
	c2 := exec.CommandContext(ctx, path, append([]string{"-C", dir}, second...)...)
	c1.Env, c2.Env = env, env
	c1.Stderr, c2.Stderr = &stderr1, &stderr2
	pr, err := c1.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gitx: pipe in %s: %w", dir, err)
	}
	c2.Stdin = pr
	out, err := c2.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gitx: pipe in %s: %w", dir, err)
	}
	if err := c1.Start(); err != nil {
		return mapStartErr(err)
	}
	if err := c2.Start(); err != nil {
		_ = c1.Process.Kill()
		_ = c1.Wait()
		return mapStartErr(err)
	}
	// c2 now holds its own copy of the pipe's read end. Closing ours means an
	// early exit of c2 breaks c1's pipe (SIGPIPE) instead of leaving c1 blocked
	// on a full buffer until the deadline.
	_ = pr.Close()
	scanErr := readLines(out, onLine)
	if scanErr != nil {
		// Stop producing so the waits below return promptly.
		_ = c1.Process.Kill()
		_ = c2.Process.Kill()
	}
	err2 := c2.Wait()
	err1 := c1.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if scanErr != nil {
		return fmt.Errorf("gitx: reading git output in %s: %w", dir, scanErr)
	}
	if e := exitError(err1, first, dir, &stderr1); e != nil {
		return e
	}
	return exitError(err2, second, dir, &stderr2)
}

func readLines(rd io.Reader, onLine func(string)) error {
	sc := bufio.NewScanner(rd)
	sc.Buffer(make([]byte, 0, 64*1024), maxPipeLine)
	for sc.Scan() {
		onLine(strings.TrimRight(sc.Text(), "\r"))
	}
	return sc.Err()
}

// mapStartErr turns a missing git binary into ErrGitNotFound.
func mapStartErr(err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		return ErrGitNotFound
	}
	return err
}

// exitError converts a failed Wait into an *Error, passing other errors on.
func exitError(err error, args []string, dir string, stderr *bytes.Buffer) error {
	if err == nil {
		return nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &Error{Args: args, Dir: dir, ExitCode: exitErr.ExitCode(), Stderr: stderr.String()}
	}
	return err
}
