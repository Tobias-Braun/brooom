package gitx

import (
	"context"
	"errors"
	"io"
)

// ErrInputUnsupported is returned by RunInput when the Runner cannot feed
// standard input to git (for example a test fake). Callers treat it as
// "cannot answer" and fall back to the conservative result.
var ErrInputUnsupported = errors.New("gitx: runner does not support stdin input")

// InputRunner is an optional Runner extension that pipes stdin into git. It
// is a separate interface so existing Runner implementations stay source
// compatible. Merge detection needs it to feed diffs into `git patch-id`.
type InputRunner interface {
	// RunInput behaves like Runner.Run but connects stdin to the process.
	RunInput(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error)
}

// RunInput runs git with stdin through r, or returns ErrInputUnsupported when
// r does not implement InputRunner.
func RunInput(ctx context.Context, r Runner, dir string, stdin io.Reader, args ...string) (string, error) {
	ir, ok := r.(InputRunner)
	if !ok {
		return "", ErrInputUnsupported
	}
	return ir.RunInput(ctx, dir, stdin, args...)
}

// RunInput implements InputRunner.
func (r *ExecRunner) RunInput(ctx context.Context, dir string, stdin io.Reader, args ...string) (string, error) {
	return r.run(ctx, dir, stdin, args)
}
