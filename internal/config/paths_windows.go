//go:build windows

package config

import "path/filepath"

// isRootPath reports whether the cleaned path is a volume root ("C:\", "C:"),
// a UNC share root (`\\server\share`) or the root of the current drive ("\").
// filepath.VolumeName covers drive letters, UNC shares and `\\?\` prefixes.
func isRootPath(cleaned string) bool {
	rest := cleaned[len(filepath.VolumeName(cleaned)):]
	return rest == "" || rest == `\`
}
