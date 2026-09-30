//go:build !unix

package gitx

import (
	"os/exec"
	"syscall"
)

// createNewProcessGroup is the Windows CREATE_NEW_PROCESS_GROUP flag. It is
// spelled out because the syscall package does not export it on every
// non-unix target.
const createNewProcessGroup = 0x00000200

// ownProcessGroup keeps a console Ctrl-C from reaching git directly, for the
// reasons given in the unix variant. The default Cancel (Process.Kill) is
// kept: it ends git, and WaitDelay bounds a lingering grandchild.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}
