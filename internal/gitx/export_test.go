package gitx

import "io"

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
