package output

import (
	"os"
	"regexp"

	"golang.org/x/term"
)

// msysPtyName matches the name of the named pipe that MSYS2, Cygwin and Git
// Bash (mintty) use as their pseudo terminal, for example
// \msys-1888ae32e00d56aa-pty0-to-master. Such a stream is a pipe to the
// Windows API, so term.IsTerminal is false for it although a person is
// typing on the other end.
var msysPtyName = regexp.MustCompile(`^\\(?:cygwin|msys)-[0-9a-f]{16}-pty[0-9]+-(?:to|from)-master(?:-nat)?$`)

// isMSYSPtyName reports whether name (as returned by
// GetFileInformationByHandleEx with FileNameInfo) is an MSYS/Cygwin pty pipe.
// It is a pure function without a build tag so it is tested on every OS.
func isMSYSPtyName(name string) bool { return msysPtyName.MatchString(name) }

// IsTerminal reports whether f is an interactive terminal: a real terminal
// or console, or (on Windows) an MSYS/Cygwin pty such as Git Bash's mintty.
// It is the one definition shared by prompts, colour and width detection, so
// they cannot disagree about whether a person is there.
func IsTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd())) || IsMSYSPty(f)
}
