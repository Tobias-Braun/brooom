//go:build !windows

package output

import "os"

// IsMSYSPty is always false outside Windows: MSYS and Cygwin ptys are a
// Windows-only construct, elsewhere a pty is a real terminal device.
func IsMSYSPty(*os.File) bool { return false }
