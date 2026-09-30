//go:build !windows

package config

// isRootPath reports whether the cleaned path is the filesystem root. Only
// "/" qualifies on unix; there are no volume names.
func isRootPath(cleaned string) bool {
	return cleaned == "/"
}
