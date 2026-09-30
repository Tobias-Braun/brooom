//go:build !windows

package action

import "os"

// platformIdentity reads the identity from a stat call; on Unix os.SameFile
// compares device and inode from data the stat already returned, so it has no
// error path of its own.
func platformIdentity(path string, follow bool) (fileID, error) {
	stat := lstat
	if follow {
		stat = os.Stat
	}
	fi, err := stat(path)
	if err != nil {
		return nil, err
	}
	return statID{fi}, nil
}
