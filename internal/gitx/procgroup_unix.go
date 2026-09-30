//go:build unix

package gitx

import (
	"os/exec"
	"syscall"
)

// ownProcessGroup starts the child in its own process group. Without it a
// terminal Ctrl-C reaches git through the shared foreground group and kills
// it half way, even for calls that run on an uncancellable context (a gc or
// worktree removal would be cut in the middle). With it only Brooom sees the
// signal and decides what to do; cancellable calls are killed by Cancel.
func ownProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Kill the whole group so hooks and aliases spawned by git die with it.
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
