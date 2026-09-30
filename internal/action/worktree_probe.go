package action

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// probeDirHandles is true where the open-file check is blind to directory
// handles. On Windows the Restart Manager only knows regular files, so a
// shell or IDE whose working directory is below a worktree holds nothing it
// can see, yet Windows refuses to rename or remove a directory that any
// process holds a handle in. It is a variable so tests can exercise the probe
// on every OS.
var probeDirHandles = runtime.GOOS == "windows"

// probeWorktreeRename is the rename probe, replaceable by tests.
var probeWorktreeRename = renameProbe

// errProbeRestore means the probe moved the directory but could not move it
// back; the message names both places so nothing is lost.
type errProbeRestore struct{ from, to string }

func (e errProbeRestore) Error() string {
	return fmt.Sprintf("probe moved %s to %s and could not move it back; restore it manually", e.from, e.to)
}

// renameProbe renames path to a sibling and back. A directory a process
// stands in cannot be renamed on Windows (sharing violation), which is the
// answer the Restart Manager cannot give. The rename is atomic and reverted
// at once, and the sibling lives in the same parent, so no data is copied or
// deleted. A failed first rename is reported as in use; a failed rename back
// is an errProbeRestore.
func renameProbe(path string) (inUse error, err error) {
	sibling := filepath.Join(filepath.Dir(path), fmt.Sprintf(".%s.brooom-probe-%d", filepath.Base(path), time.Now().UnixNano()))
	if rerr := os.Rename(path, sibling); rerr != nil {
		return rerr, nil
	}
	if rerr := os.Rename(sibling, path); rerr != nil {
		return nil, errProbeRestore{from: path, to: sibling}
	}
	return nil, nil
}

// checkWorktreeRenamable runs before `git worktree remove` under the delete
// strategy on Windows. git would otherwise empty the directory and then fail
// on the directory itself, leaving a partly deleted, still registered
// worktree. The trash and quarantine strategies move the directory with one
// rename and are safe without it. A refusal is not overridable by --force.
func checkWorktreeRenamable(path string) error {
	if !probeDirHandles {
		return nil
	}
	inUse, err := probeWorktreeRename(path)
	if err != nil {
		return err
	}
	if inUse != nil {
		return skipf("a process holds the worktree directory or a directory inside it (%v); close shells and editors there first", inUse)
	}
	return nil
}
