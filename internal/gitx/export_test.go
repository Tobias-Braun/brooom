package gitx

// SamePathOS exposes samePathOS so tests can exercise the Windows and macOS
// comparison rules on any host OS.
var SamePathOS = samePathOS

// IsMissingObject exposes the partial-clone error classifier.
var IsMissingObject = isMissingObject

// ParseWorktrees exposes the porcelain parser for format tests.
var ParseWorktrees = parseWorktrees
