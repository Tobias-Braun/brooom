//go:build windows

package action

import "golang.org/x/sys/windows"

// winID is a volume serial number plus file index, the identity Windows
// gives an object for as long as it exists.
type winID struct {
	volume uint32
	index  uint64
}

func (w winID) sameAs(o fileID) bool {
	other, ok := o.(winID)
	return ok && w == other
}

// platformIdentity opens the entry with access 0 and reads its identity from
// the handle. os.SameFile cannot be used: on Windows it loads the file ID
// lazily by opening the entry again and answers false on any error, so a
// delete-pending or ACL-denied entry would look like "not the same" instead
// of "unknown". Here every failure is returned, and the callers treat an
// error for an existing entry as a match. Without follow the final element is
// opened as a reparse point, so a link is identified as itself.
func platformIdentity(path string, follow bool) (fileID, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	flags := uint32(windows.FILE_FLAG_BACKUP_SEMANTICS)
	if !follow {
		flags |= windows.FILE_FLAG_OPEN_REPARSE_POINT
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	h, err := windows.CreateFile(p, 0, share, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, err
	}
	return winID{info.VolumeSerialNumber, uint64(info.FileIndexHigh)<<32 | uint64(info.FileIndexLow)}, nil
}
