package gitx

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

// PathWithinOS exposes pathWithinOS for the same reason.
var PathWithinOS = pathWithinOS
