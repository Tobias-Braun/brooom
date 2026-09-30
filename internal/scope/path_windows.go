//go:build windows

package scope

import "path/filepath"

// maxPathLen is the extended-length path limit of Windows.
const maxPathLen = 32767

func checkInput(p string) error { return winCheckInput(p) }

func normalizeExtended(p string) string { return winNormalizeExtended(p) }

func rejectComponent(c string) error { return winRejectComponent(c) }

// normalizeExisting lets Windows report the canonical spelling of the
// existing prefix: long names instead of 8.3 short names (PROGRA~1), the real
// letter case, and junction targets. Failure keeps the input, which is
// already symlink free.
func normalizeExisting(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return p
}
