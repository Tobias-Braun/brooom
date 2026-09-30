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
	"time"
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
// Cancelling ctx kills both processes and returns ctx.Err(). When ctx has no
// deadline the pipeline is bounded by the runner's timeout (DefaultTimeout),
// like ExecRunner.Run, and a passed bound returns a *TimeoutError. Lazy
// fetching of missing objects in partial clones is disabled so a scan never
// touches the network. A non-zero exit of either command returns an *Error.
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
	def := DefaultTimeout
	if er, ok := r.(*ExecRunner); ok {
		def = er.timeout()
	}
	start := time.Now()
	parent, cancelBound := bound(ctx, def)
	defer cancelBound()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	p := &pipeline{dir: dir, first: first, second: second, limit: limit, cancel: cancel}
	// GIT_NO_LAZY_FETCH needs git 2.44 or newer; older versions ignore it, which
	// is harmless because they only lazy-fetch when a promisor remote is set up.
	env := Env(os.Environ())
	p.c1 = exec.CommandContext(ctx, path, append([]string{"-C", dir}, first...)...)
	p.c2 = exec.CommandContext(ctx, path, append([]string{"-C", dir}, second...)...)
	for _, c := range []*exec.Cmd{p.c1, p.c2} {
		c.Env = env
		// A killed git can leave a child holding a pipe; without a delay Wait
		// would block until that child exits too.
		c.WaitDelay = waitDelay
		ownProcessGroup(c)
	}
	// A nil interface leaves Stdin on the null device, as required.
	p.c1.Stdin = stdin
	p.c1.Stderr, p.c2.Stderr = &p.stderr1, &p.stderr2
	runErr := p.run(onLine)
	if err := p.outcome(parent, start); err != nil {
		return err
	}
	return runErr
}

// pipeline is one producer/consumer run.
type pipeline struct {
	c1, c2        *exec.Cmd
	stderr1       bytes.Buffer
	stderr2       bytes.Buffer
	dir           string
	first, second []string
	limit         int64
	cancel        context.CancelFunc
	// lim, rd, wr and copyDone exist only when a byte cap is set (see connect).
	lim      *limitReader
	rd, wr   *os.File
	copyDone chan struct{}
	// producerEnded is set when we killed the producer after the consumer left.
	producerEnded atomic.Bool
}

// outcome maps the end state of a run to the errors that take precedence over
// exit statuses: the byte cap first, then a passed deadline (as *TimeoutError)
// or a cancelled parent. It returns nil when the run ended for neither reason.
func (p *pipeline) outcome(parent context.Context, start time.Time) error {
	if p.lim != nil && p.lim.exceeded.Load() {
		return ErrOutputLimit
	}
	perr := parent.Err()
	switch {
	case perr == nil:
		return nil
	case errors.Is(perr, context.DeadlineExceeded):
		args := append(append(append([]string{}, p.first...), "|"), p.second...)
		return &TimeoutError{Args: args, Dir: p.dir, After: roundElapsed(time.Since(start))}
	}
	return perr
}

// run starts both commands, streams the consumer's output to onLine and waits
// for both. The caller maps deadline and cap outcomes.
func (p *pipeline) run(onLine func(string)) error {
	pr, err := p.c1.StdoutPipe()
	if err != nil {
		return fmt.Errorf("gitx: pipe in %s: %w", p.dir, err)
	}
	if err := p.connect(pr); err != nil {
		return err
	}
	out, err := p.c2.StdoutPipe()
	if err != nil {
		p.closeCopyPipe()
		return fmt.Errorf("gitx: pipe in %s: %w", p.dir, err)
	}
	if err := p.startBoth(pr); err != nil {
		return err
	}
	scanErr := readLines(out, onLine)
	if scanErr != nil {
		// Stop producing so the waits below return promptly.
		_ = p.c1.Process.Kill()
		_ = p.c2.Process.Kill()
	}
	err2 := p.c2.Wait()
	err1 := p.finishProducer(pr)
	if scanErr != nil {
		return fmt.Errorf("gitx: reading git output in %s: %w", p.dir, scanErr)
	}
	if e := exitError(err1, p.first, p.dir, &p.stderr1); e != nil {
		return e
	}
	return exitError(err2, p.second, p.dir, &p.stderr2)
}

// startBoth starts the producer, then the consumer, and hands the pipe over.
func (p *pipeline) startBoth(pr io.Closer) error {
	if err := p.c1.Start(); err != nil {
		p.closeCopyPipe()
		return mapStartErr(err)
	}
	if err := p.c2.Start(); err != nil {
		p.closeCopyPipe()
		_ = p.c1.Process.Kill()
		_ = p.c1.Wait()
		return mapStartErr(err)
	}
	if p.lim == nil {
		// c2 now holds its own copy of the pipe's read end. Closing ours means
		// an early exit of c2 breaks c1's pipe (SIGPIPE) instead of leaving c1
		// blocked on a full buffer until the deadline.
		_ = pr.Close()
		return nil
	}
	// The consumer holds its own copy of the read end now; ours must go so its
	// exit is visible to the copy goroutine as a broken pipe.
	_ = p.rd.Close()
	p.copyDone = make(chan struct{})
	go func() {
		defer close(p.copyDone)
		_, _ = io.Copy(p.wr, p.lim)
		_ = p.wr.Close()
	}()
	return nil
}

// finishProducer runs after the consumer exited. Closing the read end breaks a
// producer that keeps writing (SIGPIPE) and unblocks the copy goroutine, which
// would otherwise wait on a slow or silent producer until the deadline, and
// with it the consumer's Wait. A silent producer never sees SIGPIPE, so it is
// killed if it outlives the consumer by waitDelay; its status is then noise.
//
// The timer starts before the wait on the copy goroutine: on Windows closing
// the pipe may not interrupt a Read that is blocked on a silent producer, so
// waiting first would leave the kill unarmed until the deadline. Killing the
// producer closes its end of the pipe, which ends that Read.
func (p *pipeline) finishProducer(pr io.Closer) error {
	timer := time.AfterFunc(waitDelay, func() {
		p.producerEnded.Store(true)
		_ = p.c1.Process.Kill()
	})
	_ = pr.Close()
	if p.copyDone != nil {
		<-p.copyDone
	}
	err := p.c1.Wait()
	timer.Stop()
	if p.producerEnded.Load() {
		return nil
	}
	return err
}

// connect wires the producer's stdout pr to the consumer's stdin. Without a
// limit the consumer reads the pipe directly. With one, the bytes pass through
// a limitReader. Handing that reader to exec would make exec copy it in a
// goroutine that Wait blocks on, and when the consumer exits early while the
// producer is slow or silent that goroutine is stuck in Read until the
// deadline. So the copy runs here, into an os.Pipe whose read end is the
// consumer's stdin, and can be stopped by closing pr.
func (p *pipeline) connect(pr io.Reader) error {
	if p.limit <= 0 {
		p.c2.Stdin = pr
		return nil
	}
	rd, wr, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("gitx: pipe in %s: %w", p.dir, err)
	}
	p.rd, p.wr = rd, wr
	p.lim = &limitReader{r: pr, left: p.limit, cancel: p.cancel}
	p.c2.Stdin = rd
	return nil
}

// closeCopyPipe releases the os.Pipe of a limited run that never got going.
func (p *pipeline) closeCopyPipe() {
	if p.rd != nil {
		_ = p.rd.Close()
		_ = p.wr.Close()
	}
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
