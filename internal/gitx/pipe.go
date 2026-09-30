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
	"sync/atomic"
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
	return PipeLimit(ctx, r, dir, first, second, 0, onLine)
}

// PipeLimit is Pipe with a cap on the bytes the first command may hand to the
// second. When limit is positive and the first command produces more, both
// processes are killed right away and ErrOutputLimit is returned, so a huge
// producer (a `log -p` of generated lockfiles) never runs to completion. Zero
// means unbounded.
func PipeLimit(ctx context.Context, r Runner, dir string, first, second []string, limit int64, onLine func(string)) error {
	return pipeLimitInput(ctx, r, dir, nil, first, second, limit, onLine)
}

// pipeLimitInput is PipeLimit with optional stdin for the first command (for
// `log --stdin`); nil leaves it on the null device.
func pipeLimitInput(ctx context.Context, r Runner, dir string, stdin io.Reader, first, second []string, limit int64, onLine func(string)) error {
	path, err := gitPath(r)
	if err != nil {
		return err
	}
	// GIT_NO_LAZY_FETCH needs git 2.44 or newer; older versions ignore it, which
	// is harmless because they only lazy-fetch when a promisor remote is set up.
	env := Env(os.Environ())
	parent := ctx
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	var stderr1, stderr2 bytes.Buffer
	c1 := exec.CommandContext(ctx, path, append([]string{"-C", dir}, first...)...)
	c2 := exec.CommandContext(ctx, path, append([]string{"-C", dir}, second...)...)
	c1.Env, c2.Env = env, env
	if stdin != nil {
		c1.Stdin = stdin
	}
	c1.Stderr, c2.Stderr = &stderr1, &stderr2
	pr, err := c1.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gitx: pipe in %s: %w", dir, err)
	}
	c2.Stdin = pr
	var lim *limitReader
	if limit > 0 {
		lim = &limitReader{r: pr, left: limit, cancel: cancel}
		c2.Stdin = lim
	}
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
	if lim == nil {
		_ = pr.Close()
	}
	scanErr := readLines(out, onLine)
	if scanErr != nil {
		// Stop producing so the waits below return promptly.
		_ = c1.Process.Kill()
		_ = c2.Process.Kill()
	}
	err2 := c2.Wait()
	if lim != nil {
		// c2 may have stopped reading; close the read end so c1 cannot block
		// on a full pipe.
		_ = pr.Close()
	}
	err1 := c1.Wait()
	if lim != nil && lim.exceeded.Load() {
		return ErrOutputLimit
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if scanErr != nil {
		return fmt.Errorf("gitx: reading git output in %s: %w", dir, scanErr)
	}
	if e := exitError(err1, first, dir, &stderr1); e != nil {
		return e
	}
	return exitError(err2, second, dir, &stderr2)
}

// limitReader passes at most left bytes through. Reading past that cancels the
// pipeline's context, which kills both processes, and fails the read.
type limitReader struct {
	r        io.Reader
	left     int64
	cancel   context.CancelFunc
	exceeded atomic.Bool
}

func (l *limitReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		l.exceeded.Store(true)
		l.cancel()
		return 0, ErrOutputLimit
	}
	return n, err
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
