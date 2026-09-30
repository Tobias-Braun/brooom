//go:build !windows

package cli

import "os"

// enableVirtualTerminal is a no-op outside Windows: unix terminals interpret
// ANSI escapes natively.
func enableVirtualTerminal(*os.File) bool { return true }
