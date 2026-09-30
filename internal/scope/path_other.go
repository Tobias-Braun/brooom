//go:build !windows

package scope

// maxPathLen is PATH_MAX on Linux; macOS is stricter and reports its own
// error for longer paths.
const maxPathLen = 4096

// The Windows specific path syntax hooks are no-ops here: backslashes and
// colons are ordinary characters in unix file names and the resolver already
// returns fully resolved prefixes.

func checkInput(string) error { return nil }

func normalizeExtended(p string) string { return p }

func rejectComponent(string) error { return nil }

func normalizeExisting(p string) string { return p }

// canonicalLast has nothing to do here: only Windows has 8.3 aliases.
func canonicalLast(p string) string { return p }
