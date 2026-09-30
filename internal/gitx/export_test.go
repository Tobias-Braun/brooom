package gitx

import (
	"io"
	"os/exec"
)

// SamePathOS exposes samePathOS so tests can exercise the Windows and macOS
// comparison rules on any host OS.
var SamePathOS = samePathOS

// IsMissingObject exposes the partial-clone error classifier.
var IsMissingObject = isMissingObject

// ParseWorktrees exposes the porcelain parser for format tests.
var ParseWorktrees = parseWorktrees

// SetDiffLimit lowers the squash-detection diff cap of r so tests can hit it
// without producing tens of megabytes.
func SetDiffLimit(r *Repo, n int64) { r.diffLimit = n }

// SafeDirectoryCommand exposes the per-OS quoted safe.directory hint.
var SafeDirectoryCommand = safeDirectoryCommand

// FinishProducerStuckRead runs the producer wait-up of a pipeline whose copy
// goroutine sits in a Read that closing the pipe cannot interrupt (what a
// Windows pipe may do): pr is read here and the no-op closer stands in for
// the close that would not help. c1 must be started with pr as its stdout.
func FinishProducerStuckRead(c1 *exec.Cmd, pr io.Reader) error {
	p := &pipeline{c1: c1, copyDone: make(chan struct{})}
	go func() {
		defer close(p.copyDone)
		_, _ = io.Copy(io.Discard, pr)
	}()
	return p.finishProducer(noopCloser{})
}

type noopCloser struct{}

func (noopCloser) Close() error { return nil }

// StrippedOS exposes the env filter with the OS as a parameter so the Windows
// case-insensitive rule is testable on any host.
var StrippedOS = strippedOS

// NewLimitReader exposes the byte-capped reader of PipeLimit.
func NewLimitReader(r io.Reader, limit int64, cancel func()) io.Reader {
	return &limitReader{r: r, left: limit, cancel: cancel}
}

// ParseBehind exposes the ahead-behind record parser.
var ParseBehind = parseBehind

// PathWithinOS exposes pathWithinOS for the same reason.
var PathWithinOS = pathWithinOS

// TrackedChunks exposes the pathspec chunking of TrackedUnder.
var TrackedChunks = trackedChunks
