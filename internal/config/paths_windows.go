//go:build windows

package config

import "path/filepath"

// isRootPath reports whether the cleaned path is a volume root ("C:\", "C:"),
// a UNC share root (`\\server\share`) or the root of the current drive ("\").
// filepath.VolumeName covers drive letters, UNC shares and `\\?\` prefixes.
func isRootPath(cleaned string) bool {
	vol := filepath.VolumeName(cleaned)
	rest := cleaned[len(vol):]
	// filepath.Clean turns a bare drive ("C:") into "C:.", so a remainder of
	// "." next to a volume name also denotes the volume itself.
	return rest == "" || rest == `\` || (vol != "" && rest == ".")
}
