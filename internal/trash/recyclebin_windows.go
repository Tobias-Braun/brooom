//go:build windows

package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// This file binds the Windows APIs behind the Recycle Bin trasher: the shell
// file operation, volume lookup, the per-volume bin settings in the registry,
// the current user's SID and the enumeration of the bin. The decisions made
// on their results are in recyclebin_parse.go.

// SHFileOperationW function and flag values from shellapi.h.
const (
	foDelete = 0x3

	fofSilent          = 0x0004
	fofNoConfirmation  = 0x0010
	fofAllowUndo       = 0x0040
	fofNoConfirmMkdir  = 0x0200
	fofNoErrorUI       = 0x0400
	fofWantNukeWarning = 0x4000

	// shellDeleteFlags asks for the Recycle Bin (ALLOWUNDO) without any
	// dialog. FOF_WANTNUKEWARNING partially overrides FOF_NOCONFIRMATION so
	// that the shell does not silently delete permanently what it cannot
	// recycle; the pre-flight in Remove is the primary safeguard anyway.
	shellDeleteFlags = fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI | fofNoConfirmMkdir | fofWantNukeWarning
)

// shFileOpStruct is SHFILEOPSTRUCTW. On 64-bit Windows (amd64 and arm64) the
// C structure uses the default 8-byte packing, which is exactly Go's natural
// alignment, so plain fields reproduce it: 56 bytes with fFlags at offset 32
// and the BOOL at offset 36. (Only 32-bit x86 uses pack(1); brooom does not
// ship for it and TestShellStructLayout guards the 64-bit layout.)
type shFileOpStruct struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

var procSHFileOperation = windows.NewLazySystemDLL("shell32.dll").NewProc("SHFileOperationW")

// shellDelete runs FO_DELETE with shellDeleteFlags on the single path in from
// (a buffer made by buildFromBuffer). SHFileOperationW is used instead of
// IFileOperation because it is synchronous and needs no COM apartment
// management; nothing has shown that IFileOperation is required for long
// paths, which are refused before this point anyway.
func shellDelete(from []uint16) (code int, aborted bool) {
	op := shFileOpStruct{wFunc: foDelete, pFrom: &from[0], fFlags: shellDeleteFlags}
	r, _, _ := procSHFileOperation.Call(uintptr(unsafe.Pointer(&op)))
	// op holds a raw pointer into from, which must outlive the call.
	runtime.KeepAlive(from)
	return int(int32(r)), op.fAnyOperationsAborted != 0
}

// binSettingsReader reads the Recycle Bin settings of one volume.
type binSettingsReader interface {
	read(volumeGUID string) (binSettings, error)
}

// binVolumeKey is the registry key (below HKCU) with one subkey per volume
// GUID holding NukeOnDelete and MaxCapacity.
const binVolumeKey = `Software\Microsoft\Windows\CurrentVersion\Explorer\BitBucket\Volume`

// registrySettings reads binSettings from the registry.
type registrySettings struct{}

// read implements binSettingsReader. A missing key or MaxCapacity is an
// error, never a default: a missing MaxCapacity can mean "default size",
// which is a percentage of the volume and not a limit that could be checked.
// Opening the Recycle Bin properties once creates the key. A missing
// NukeOnDelete is the normal state of a bin that was never switched off.
func (registrySettings) read(volumeGUID string) (binSettings, error) {
	k, err := registry.OpenKey(registry.CURRENT_USER, binVolumeKey+`\`+volumeGUID, registry.QUERY_VALUE)
	if err != nil {
		return binSettings{}, fmt.Errorf("no Recycle Bin settings for volume %s (open the Recycle Bin properties once to create them): %w", volumeGUID, err)
	}
	defer k.Close()
	capacity, _, err := k.GetIntegerValue("MaxCapacity")
	if err != nil {
		return binSettings{}, fmt.Errorf("cannot read MaxCapacity for volume %s (open the Recycle Bin properties once to create it): %w", volumeGUID, err)
	}
	nuke, _, err := k.GetIntegerValue("NukeOnDelete")
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return binSettings{}, fmt.Errorf("cannot read NukeOnDelete for volume %s: %w", volumeGUID, err)
	}
	if capacity > 0xFFFFFFFF {
		return binSettings{}, fmt.Errorf("MaxCapacity %d for volume %s is out of range", capacity, volumeGUID)
	}
	return binSettings{NukeOnDelete: nuke != 0, MaxCapacityMB: uint32(capacity)}, nil
}

// volumeRoot returns the root of the volume that holds path, for example
// "C:\". For a network share it is the UNC share root.
func volumeRoot(path string) (string, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 1024)
	if err := windows.GetVolumePathName(p, &buf[0], uint32(len(buf))); err != nil {
		return "", fmt.Errorf("cannot resolve the volume of %s: %w", path, err)
	}
	return windows.UTF16ToString(buf), nil
}

// volumeGUID returns "{guid}" of the volume holding path. Network shares and
// other volumes without a GUID fail here, which the caller treats as "no
// Recycle Bin available".
func volumeGUID(path string) (string, error) {
	root, err := volumeRoot(path)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(root, `\`) {
		root += `\`
	}
	r16, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return "", err
	}
	buf := make([]uint16, 64)
	if err := windows.GetVolumeNameForVolumeMountPoint(r16, &buf[0], uint32(len(buf))); err != nil {
		return "", fmt.Errorf("volume %s has no volume GUID: %w", root, err)
	}
	return volumeGUIDFromName(windows.UTF16ToString(buf))
}

// isReparsePoint reports whether path is a symlink or junction, by attribute
// rather than by Go's file mode, which does not classify every kind of
// reparse point the same way.
func isReparsePoint(path string) (bool, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return false, err
	}
	return attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0, nil
}

// longPath expands 8.3 short components ("RUNNER~1") of path to their long
// names, which is how the Recycle Bin records original paths. Components that
// do not exist (any more) are kept as given, so the result is still usable for
// an item that was already removed; on any failure the input is returned.
func longPath(path string) string {
	if l, ok := getLongPathName(path); ok {
		return l
	}
	dir := filepath.Dir(path)
	if dir == path || dir == "." {
		return path
	}
	return filepath.Join(longPath(dir), filepath.Base(path))
}

// getLongPathName wraps GetLongPathNameW and reports whether it succeeded.
func getLongPathName(path string) (string, bool) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", false
	}
	buf := make([]uint16, 512)
	n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	if err != nil {
		return "", false
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n)
		if n, err = windows.GetLongPathName(p, &buf[0], uint32(len(buf))); err != nil || int(n) > len(buf) {
			return "", false
		}
	}
	return windows.UTF16ToString(buf[:n]), true
}

// currentSID returns the string SID of the process user, the name of the
// per-user directory below $Recycle.Bin.
func currentSID() (string, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return "", fmt.Errorf("cannot determine the current user: %w", err)
	}
	return u.User.Sid.String(), nil
}

// userBinDir returns <volume>\$Recycle.Bin\<sid> for the volume holding path.
func userBinDir(path string) (string, error) {
	root, err := volumeRoot(path)
	if err != nil {
		return "", err
	}
	sid, err := currentSID()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, binDirName, sid), nil
}

// listBin reads the parsed $I files of the user's bin on the volume holding
// path and returns them with the bin directory. Only entries whose $R item
// exists are returned, and unparsable $I files are skipped: they belong to
// somebody else's or a damaged deletion and must not be guessed at.
//
// cache, when not nil, remembers parsed $I files across calls. Callers that
// list repeatedly (one listing per removed item of a batch) pass one; Restore
// passes nil and always reads fresh, because its answer must reflect the bin
// as it is now.
func listBin(path string, cache *infoCache) (string, []binEntry, error) {
	dir, err := userBinDir(path)
	if err != nil {
		return "", nil, err
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return dir, nil, fmt.Errorf("cannot read the Recycle Bin %s: %w", dir, err)
	}
	var names []string
	for _, de := range des {
		if strings.HasPrefix(de.Name(), "$I") && !de.IsDir() {
			names = append(names, de.Name())
		}
	}
	return dir, collectEntries(names, cache, func(name string) (infoRecord, bool, bool) {
		return readBinInfo(dir, name)
	}), nil
}

// readBinInfo reads and parses one $I file of the bin in dir. Only a usable
// entry is cacheable: a file that fails to read or parse, or whose $R item is
// missing, may be caught mid-write (the shell writes the pair in steps) and
// must be looked at again next time.
func readBinInfo(dir, name string) (rec infoRecord, ok, cacheable bool) {
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return infoRecord{}, false, false
	}
	rec, err = parseInfo(data)
	if err != nil {
		return infoRecord{}, false, false
	}
	if _, err := os.Lstat(filepath.Join(dir, storedName(name))); err != nil {
		return infoRecord{}, false, false
	}
	return rec, true, true
}
