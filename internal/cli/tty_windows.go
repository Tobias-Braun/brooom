//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// enableVirtualTerminal switches the console behind f to virtual terminal
// processing so ANSI escapes render instead of printing as garbage. It
// reports false when the mode cannot be read or set (for example on an old
// console host), in which case the caller disables color.
func enableVirtualTerminal(f *os.File) bool {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return false
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return true
	}
	return windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) == nil
}
