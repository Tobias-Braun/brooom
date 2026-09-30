package gitx

// SamePathOS exposes samePathOS so tests can exercise the Windows and macOS
// comparison rules on any host OS.
var SamePathOS = samePathOS

// ParseWorktrees exposes the porcelain parser for format tests.
var ParseWorktrees = parseWorktrees
