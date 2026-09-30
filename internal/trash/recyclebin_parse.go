package trash

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// This file holds the parts of the Windows Recycle Bin support that are pure
// functions on strings and bytes. They carry no build tag on purpose: the
// safety-relevant decisions (which paths are refused, when a bin cannot take
// an item, how the $I metadata is read) are tested on every OS instead of on
// Windows CI only. The API calls that feed them live in recyclebin_windows.go.

// quarantineHint is appended to every refusal so the user always learns the
// safe alternative. A Windows removal never degrades into permanent deletion.
const quarantineHint = "use --trash-strategy quarantine instead"

// maxShellPath is the longest path SHFileOperationW accepts (MAX_PATH - 1).
// The shell does not understand the \\?\ prefix, and a long path passed as is
// makes it fail or, worse, act on a different item, so longer paths are
// refused up front.
//
// Finding: no Windows machine was available while developing this, so the
// \\?\ form could not be verified and is not attempted. Long paths get a
// clear error recommending quarantine, which handles them through the
// regular Go file APIs.
const maxShellPath = 259

// binDirName is the per-volume Recycle Bin directory.
const binDirName = "$Recycle.Bin"

// buildFromBuffer builds the double-NUL-terminated UTF-16 pFrom buffer for
// SHFileOperationW. Every path is followed by one NUL and the list by a second
// one. It refuses what the shell would misinterpret: an embedded NUL would end
// the list early, and the wildcards * and ? would be expanded by the shell
// into a possibly much larger set of items, which is a data-loss risk. Unlike
// a bare []uint16 return it reports the refusal as an error.
func buildFromBuffer(paths []string) ([]uint16, error) {
	if len(paths) == 0 {
		return nil, errors.New("no path to move to the Recycle Bin")
	}
	var buf []uint16
	for _, p := range paths {
		if p == "" {
			return nil, errors.New("refusing an empty path")
		}
		if strings.ContainsRune(p, 0) {
			return nil, fmt.Errorf("refusing path %q: contains a NUL character", p)
		}
		if strings.ContainsAny(p, "*?") {
			return nil, fmt.Errorf("refusing path %q: wildcard characters are expanded by the shell", p)
		}
		buf = append(buf, utf16.Encode([]rune(p))...)
		buf = append(buf, 0)
	}
	return append(buf, 0), nil
}

// winSplit splits a Windows path into its volume prefix and the remaining
// components, using string handling only so it behaves the same on every OS.
// Both separators are accepted. kind is "drive" (C:), "unc" (\\server\share),
// "verbatim" (\\?\ and \\.\ forms) or "" for paths without a volume.
func winSplit(path string) (volume string, rest []string, kind string) {
	p := strings.ReplaceAll(path, "/", `\`)
	switch {
	case strings.HasPrefix(p, `\\?\`) || strings.HasPrefix(p, `\\.\`):
		return p[:4], splitComponents(p[4:]), "verbatim"
	case strings.HasPrefix(p, `\\`):
		parts := splitComponents(p[2:])
		if len(parts) < 2 {
			return `\\` + strings.Join(parts, `\`), nil, "unc"
		}
		return `\\` + parts[0] + `\` + parts[1], parts[2:], "unc"
	case len(p) >= 2 && p[1] == ':' && isDriveLetter(p[0]):
		return p[:2], splitComponents(p[2:]), "drive"
	}
	return "", splitComponents(p), ""
}

// splitComponents splits on backslashes and drops empty elements.
func splitComponents(p string) []string {
	var out []string
	for _, c := range strings.Split(p, `\`) {
		if c != "" {
			out = append(out, c)
		}
	}
	return out
}

func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// validateBinPath is the pure part of the refusals in Remove: everything that
// can be decided from the path text alone. Empty and relative paths, drive
// and UNC roots, wildcards and NULs, paths inside a Recycle Bin and paths the
// shell cannot take (verbatim and longer than MAX_PATH) never reach the shell.
func validateBinPath(path string) error {
	if err := checkPathText(path); err != nil {
		return err
	}
	vol, rest, kind := winSplit(path)
	if kind == "verbatim" {
		return fmt.Errorf("refusing path %q: the Recycle Bin API does not accept \\\\?\\ paths; %s", path, quarantineHint)
	}
	if kind == "" || (kind == "drive" && !isRooted(path)) {
		return fmt.Errorf("refusing relative path %q", path)
	}
	if len(rest) == 0 {
		return fmt.Errorf("refusing to move the root %q of volume %s to the Recycle Bin", path, vol)
	}
	if err := checkComponents(path, rest); err != nil {
		return err
	}
	// The limit counts UTF-16 code units, not UTF-8 bytes: a path of many
	// non-ASCII characters is short for the shell but long in bytes.
	if len(utf16.Encode([]rune(path))) > maxShellPath {
		return fmt.Errorf("cannot move %s to the Recycle Bin: the path is longer than %d characters, which the Windows shell API does not support; %s", path, maxShellPath, quarantineHint)
	}
	return nil
}

// checkPathText refuses empty paths and characters the shell would misread.
func checkPathText(path string) error {
	if path == "" {
		return errors.New("refusing to move an empty path to the Recycle Bin")
	}
	if strings.ContainsRune(path, 0) {
		return fmt.Errorf("refusing path %q: contains a NUL character", path)
	}
	if strings.ContainsAny(path, "*?") {
		return fmt.Errorf("refusing path %q: wildcard characters are expanded by the shell", path)
	}
	return nil
}

// checkComponents refuses dot components and anything inside a Recycle Bin.
func checkComponents(path string, rest []string) error {
	for _, c := range rest {
		if c == "." || c == ".." {
			return fmt.Errorf("refusing path %q: not a clean absolute path", path)
		}
		if strings.EqualFold(c, binDirName) {
			return fmt.Errorf("refusing path %q: it is inside a Recycle Bin", path)
		}
	}
	return nil
}

// isRooted reports whether a drive path has a separator after the colon.
// "C:foo" is relative to the current directory of drive C.
func isRooted(path string) bool {
	return len(path) >= 3 && (path[2] == '\\' || path[2] == '/')
}

// normalizeWinPath makes two spellings of one path comparable: forward
// slashes become backslashes, repeated and trailing separators go, and the
// result is lower-cased because Windows paths are case-insensitive.
func normalizeWinPath(p string) string {
	vol, rest, _ := winSplit(p)
	return strings.ToLower(vol + `\` + strings.Join(rest, `\`))
}

// binSettings are the per-volume Recycle Bin settings from
// HKCU\Software\Microsoft\Windows\CurrentVersion\Explorer\BitBucket\Volume\<GUID>.
type binSettings struct {
	// NukeOnDelete is the "do not move files to the Recycle Bin" option:
	// the shell deletes permanently on that volume.
	NukeOnDelete bool
	// MaxCapacityMB is the bin's size limit for the volume in megabytes.
	MaxCapacityMB uint32
}

// decideBinAvailability decides whether the bin of the item's volume can take
// an item of itemSize bytes. settingsErr is the error of reading the
// settings; unknown always means refuse, because the shell would otherwise
// delete permanently what it cannot fit. A nil result is the only go-ahead.
// An item exactly as large as MaxCapacity still fits.
func decideBinAvailability(path string, s binSettings, settingsErr error, itemSize int64) error {
	if settingsErr != nil {
		return fmt.Errorf("cannot move %s to the Recycle Bin: its settings for this volume are unknown (%w), so Windows might delete permanently; %s", path, settingsErr, quarantineHint)
	}
	if s.NukeOnDelete {
		return fmt.Errorf("cannot move %s to the Recycle Bin: files on this volume are deleted permanently (the Recycle Bin is disabled for it); %s", path, quarantineHint)
	}
	if itemSize > int64(s.MaxCapacityMB)*1024*1024 {
		return fmt.Errorf("cannot move %s to the Recycle Bin: its size (%d bytes) exceeds the Recycle Bin limit of %d MB for this volume, so Windows would delete it permanently; %s", path, itemSize, s.MaxCapacityMB, quarantineHint)
	}
	return nil
}

// volumeGUIDFromName extracts "{guid}" from a volume name of the form
// \\?\Volume{guid}\, which is the key name below BitBucket\Volume.
func volumeGUIDFromName(name string) (string, error) {
	const prefix = `\\?\Volume`
	if !strings.HasPrefix(name, prefix) {
		return "", fmt.Errorf("unexpected volume name %q", name)
	}
	guid := strings.TrimSuffix(strings.TrimPrefix(name, prefix), `\`)
	if len(guid) != 38 || guid[0] != '{' || guid[37] != '}' {
		return "", fmt.Errorf("unexpected volume GUID in %q", name)
	}
	return guid, nil
}

// infoRecord is a parsed $I file.
type infoRecord struct {
	Version   int64
	Size      int64
	DeletedAt time.Time
	Path      string
}

const (
	infoHeader   = 24  // version, size and FILETIME, 8 bytes each
	infoV1Length = 544 // header + 520 bytes of UTF-16 path
	infoMaxChars = 32768
)

// parseInfo parses the contents of a $I metadata file. Version 1 (Vista to
// Windows 8) has a fixed 520-byte path field, version 2 (Windows 10 and
// later) a length-prefixed path. Anything else, truncated or malformed data
// is an error; callers skip such files rather than guess.
func parseInfo(data []byte) (infoRecord, error) {
	if len(data) < infoHeader {
		return infoRecord{}, fmt.Errorf("$I file too short (%d bytes)", len(data))
	}
	rec := infoRecord{
		Version:   int64(binary.LittleEndian.Uint64(data[0:8])),
		Size:      int64(binary.LittleEndian.Uint64(data[8:16])),
		DeletedAt: filetimeToTime(int64(binary.LittleEndian.Uint64(data[16:24]))),
	}
	if rec.Size < 0 {
		return infoRecord{}, errors.New("$I file has a negative size")
	}
	raw, err := infoPathBytes(rec.Version, data)
	if err != nil {
		return infoRecord{}, err
	}
	rec.Path = decodeUTF16Z(raw)
	if rec.Path == "" {
		return infoRecord{}, errors.New("$I file has an empty path")
	}
	return rec, nil
}

// infoPathBytes returns the raw UTF-16 path field of a $I file for its
// version, after checking that the data is long enough for it.
func infoPathBytes(version int64, data []byte) ([]byte, error) {
	switch version {
	case 1:
		if len(data) < infoV1Length {
			return nil, fmt.Errorf("truncated version 1 $I file (%d bytes)", len(data))
		}
		return data[infoHeader:infoV1Length], nil
	case 2:
		if len(data) < infoHeader+4 {
			return nil, errors.New("truncated version 2 $I file")
		}
		n := int(int32(binary.LittleEndian.Uint32(data[infoHeader : infoHeader+4])))
		if n <= 0 || n > infoMaxChars || len(data) < infoHeader+4+2*n {
			return nil, fmt.Errorf("version 2 $I file has an invalid path length %d", n)
		}
		return data[infoHeader+4 : infoHeader+4+2*n], nil
	}
	return nil, fmt.Errorf("unsupported $I version %d", version)
}

// decodeUTF16Z decodes little-endian UTF-16 up to the first NUL.
func decodeUTF16Z(raw []byte) string {
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		u := binary.LittleEndian.Uint16(raw[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}

// filetimeEpochOffset is the number of seconds between 1601-01-01 (FILETIME
// epoch) and 1970-01-01.
const filetimeEpochOffset = 11644473600

// filetimeToTime converts a FILETIME (100 ns ticks since 1601, UTC).
func filetimeToTime(ft int64) time.Time {
	sec := ft/1e7 - filetimeEpochOffset
	nsec := (ft % 1e7) * 100
	return time.Unix(sec, nsec).UTC()
}

// binEntry is one $I file found in a bin directory together with its parsed
// contents. Name is the file name, for example "$IABC123.txt"; its $R
// counterpart is "$RABC123.txt".
type binEntry struct {
	Name string
	Info infoRecord
}

// storedName returns the name of the $R item belonging to a $I file name.
func storedName(infoName string) string { return "$R" + strings.TrimPrefix(infoName, "$I") }

// infoNameOf returns the name of the $I file belonging to a $R item name.
func infoNameOf(storedItem string) string { return "$I" + strings.TrimPrefix(storedItem, "$R") }

// matchTolerance is how far a $I deletion time may differ from the time of
// the call. The shell stamps it a moment after brooom read the clock.
// Issue #24 asks for "a few seconds"; this deliberately deviates to 30 s
// (it was 5 s at first). It is generous because the shell stamps the time only after it has walked
// and moved the whole tree, which takes long for a large directory. A wider
// window stays safe: the original path must match too and the newest match wins.
const matchTolerance = 30 * time.Second

// chooseRecycled returns the newest entry whose original path equals orig
// (case-insensitive, normalised) and whose deletion time is within tol of at.
func chooseRecycled(entries []binEntry, orig string, at time.Time, tol time.Duration) (binEntry, bool) {
	want := normalizeWinPath(orig)
	var best binEntry
	found := false
	for _, e := range entries {
		if normalizeWinPath(e.Info.Path) != want {
			continue
		}
		d := e.Info.DeletedAt.Sub(at)
		if d < -tol || d > tol {
			continue
		}
		if !found || e.Info.DeletedAt.After(best.Info.DeletedAt) {
			best, found = e, true
		}
	}
	return best, found
}

// winParentName returns the name of the directory that directly contains the
// last component of a Windows path, or "" if there is none.
func winParentName(p string) string {
	_, rest, _ := winSplit(p)
	if len(rest) < 2 {
		return ""
	}
	return rest[len(rest)-2]
}

// chooseRecycledAny is chooseRecycled over several spellings of one path (for
// example the long and the 8.3 form). The first spelling with a match wins.
func chooseRecycledAny(entries []binEntry, origs []string, at time.Time, tol time.Duration) (binEntry, bool) {
	for _, o := range origs {
		if e, ok := chooseRecycled(entries, o, at, tol); ok {
			return e, true
		}
	}
	return binEntry{}, false
}

// checkBinOwnerSID is the pure part of checkBinOwner: it compares the SID
// directory of p (case-insensitively) with sid.
func checkBinOwnerSID(p, sid string) error {
	if dir := winParentName(p); !strings.EqualFold(dir, sid) {
		return fmt.Errorf("refusing %q: it is not in the Recycle Bin of the current user", p)
	}
	return nil
}

// checkBinInfoPath verifies that info is exactly the $I file that belongs to
// the $R item stored: same directory, name derived from the stored name. A
// tampered manifest thus cannot get the metadata of another item deleted.
func checkBinInfoPath(info, stored string) error {
	svol, srest, _ := winSplit(stored)
	ivol, irest, _ := winSplit(info)
	if len(srest) == 0 || len(irest) != len(srest) || !strings.EqualFold(svol, ivol) {
		return fmt.Errorf("refusing %q: it does not belong to %q", info, stored)
	}
	for i := 0; i < len(srest)-1; i++ {
		if !strings.EqualFold(srest[i], irest[i]) {
			return fmt.Errorf("refusing %q: it does not belong to %q", info, stored)
		}
	}
	if irest[len(irest)-1] != infoNameOf(srest[len(srest)-1]) {
		return fmt.Errorf("refusing %q: it does not belong to %q", info, stored)
	}
	return nil
}

// checkBinItemPath verifies that p is a direct item of the user's bin
// directory binDir (<volume>\$Recycle.Bin\<sid>, as derived from the original
// path by userBinDir) and that its name starts with prefix. Restore acts on
// paths from a manifest, so this is the boundary that keeps a tampered record
// from moving arbitrary files. Comparing against the derived directory instead
// of a fixed drive-letter shape also accepts volumes mounted into a folder
// (C:\mnt\data), whose bin sits at the mount point.
func checkBinItemPath(p, prefix, binDir string) error {
	bvol, brest, bkind := winSplit(binDir)
	if bkind != "drive" || len(brest) < 2 || !strings.EqualFold(brest[len(brest)-2], binDirName) {
		return fmt.Errorf("refusing %q: %q is not a per-user %s directory", p, binDir, binDirName)
	}
	pvol, prest, pkind := winSplit(p)
	if pkind != "drive" || !strings.EqualFold(pvol, bvol) || !isDirectChild(prest, brest) {
		return fmt.Errorf("refusing %q: not an item directly inside %q", p, binDir)
	}
	name := prest[len(prest)-1]
	if !strings.HasPrefix(name, prefix) || len(name) <= len(prefix) {
		return fmt.Errorf("refusing %q: expected a name starting with %s", p, prefix)
	}
	return nil
}

// isDirectChild reports whether child is exactly one component below parent,
// comparing the shared components case-insensitively.
func isDirectChild(child, parent []string) bool {
	if len(child) != len(parent)+1 {
		return false
	}
	for i, c := range parent {
		if !strings.EqualFold(child[i], c) {
			return false
		}
	}
	return true
}

// recycledSuffixLen is the length of the "$R" prefix plus the six random
// characters of a bin item name.
const recycledSuffixLen = 2 + 6

// utf16Len is the length of s in UTF-16 code units, the unit of MAX_PATH.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

// recycledPrefixLen returns the length, in UTF-16 units, of the directory
// part an item gets inside the bin: <volume>\$Recycle.Bin\<sid>\$R<random><ext>.
// The extension of the item is kept by the shell; it is counted whenever the
// name has a dot, which can only overestimate and so errs towards refusing.
func recycledPrefixLen(path, sid string) int {
	vol, rest, _ := winSplit(path)
	ext := ""
	if len(rest) > 0 {
		name := rest[len(rest)-1]
		if i := strings.LastIndexByte(name, '.'); i >= 0 {
			ext = name[i:]
		}
	}
	return utf16Len(vol) + 1 + len(binDirName) + 1 + utf16Len(sid) + 1 + recycledSuffixLen + utf16Len(ext)
}

// checkTreeDepth refuses a tree whose deepest descendant would not fit
// MAX_PATH, which the shell API (and therefore the bin) cannot handle.
// longestRel is the UTF-16 length of the longest descendant path relative to
// path, including its leading separator (0 for a file or an empty directory).
//
// Two limits apply. The descendants must already fit under the original
// path, otherwise the shell cannot even open them. And they are re-rooted
// below $Recycle.Bin\<sid>\$R<random> when recycled, a prefix of roughly 70
// characters that is usually longer than a short original path: a tree that
// only just fits before overflows in the bin, where the shell reports "too
// long to recycle" and waits on the permanent-deletion dialog (see winTrash).
func checkTreeDepth(path, sid string, longestRel int) error {
	if longestRel <= 0 {
		return nil
	}
	if n := utf16Len(path) + longestRel; n > maxShellPath {
		return fmt.Errorf("cannot move %s to the Recycle Bin: it contains a path of %d characters, longer than the %d the Windows shell API supports; %s", path, n, maxShellPath, quarantineHint)
	}
	if n := recycledPrefixLen(path, sid) + longestRel; n > maxShellPath {
		return fmt.Errorf("cannot move %s to the Recycle Bin: its deepest item would have a path of %d characters inside the Recycle Bin, longer than the %d the Windows shell API supports (Windows would stall on a confirmation dialog); %s", path, n, maxShellPath, quarantineHint)
	}
	return nil
}

// errCallBound reports that a bounded call did not return in time.
var errCallBound = fmt.Errorf("the call did not return within its time limit: %w", context.DeadlineExceeded)

// callBounded runs a blocking shell call in its own goroutine and gives up
// waiting when ctx ends or timeout elapses. A blocked SHFileOperationW cannot
// be cancelled, so the goroutine is left to finish on its own; its result is
// dropped. err is non-nil exactly when the call was abandoned, in which case
// the outcome is unknown and the caller must inspect the item (Lstat) before
// doing anything else with it.
func callBounded(ctx context.Context, timeout time.Duration, call func() (int, bool)) (code int, aborted bool, err error) {
	type result struct {
		code    int
		aborted bool
	}
	done := make(chan result, 1)
	go func() {
		c, a := call()
		done <- result{c, a}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-done:
		return r.code, r.aborted, nil
	case <-ctx.Done():
		return 0, false, ctx.Err()
	case <-timer.C:
		return 0, false, errCallBound
	}
}

// infoCache remembers parsed $I files by name for the lifetime of a trasher.
// A batch of N removals lists the bin N times; without the cache every
// listing re-reads and re-parses all M existing $I files (N x M reads). A $I
// file is written once by the shell and never modified, so its name identifies
// its contents.
type infoCache struct {
	mu sync.Mutex
	m  map[string]cachedInfo
}

// cachedInfo is one remembered $I result; ok is false for a file that could
// not be parsed, which is remembered too so it is not re-read every time.
type cachedInfo struct {
	rec infoRecord
	ok  bool
}

// collectEntries turns $I file names into entries. read loads one file and
// reports whether it is usable and whether that verdict may be remembered
// (a $I whose $R is not there yet must be looked at again). A nil cache reads
// everything.
func collectEntries(names []string, cache *infoCache, read func(name string) (rec infoRecord, ok, cacheable bool)) []binEntry {
	var out []binEntry
	for _, n := range names {
		if rec, ok, hit := cache.get(n); hit {
			if ok {
				out = append(out, binEntry{Name: n, Info: rec})
			}
			continue
		}
		rec, ok, cacheable := read(n)
		if cacheable {
			cache.put(n, rec, ok)
		}
		if ok {
			out = append(out, binEntry{Name: n, Info: rec})
		}
	}
	return out
}

// get returns the remembered result for name; a nil cache never hits.
func (c *infoCache) get(name string) (rec infoRecord, ok, hit bool) {
	if c == nil {
		return infoRecord{}, false, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ci, hit := c.m[name]
	return ci.rec, ci.ok, hit
}

// put remembers a result; a nil cache ignores it.
func (c *infoCache) put(name string, rec infoRecord, ok bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedInfo{}
	}
	c.m[name] = cachedInfo{rec: rec, ok: ok}
}

// shellErrorTable maps the legacy DE_* codes SHFileOperation returns (they
// are not Win32 errors, apart from a few that coincide) to readable text.
var shellErrorTable = map[int]string{
	0x71:    "source and destination are the same file",
	0x72:    "multiple file paths were specified for a single destination",
	0x73:    "rename operation on a different volume",
	0x74:    "the item is a root directory and cannot be moved",
	0x75:    "the operation was cancelled",
	0x76:    "the destination is a subtree of the source",
	0x78:    "access denied",
	0x79:    "the path is too long for the shell",
	0x7C:    "the path is invalid",
	0x7E:    "the destination is a root directory",
	0x7F:    "the source is a root directory",
	0x80:    "the file exists already",
	0x81:    "the destination path is too long",
	0x82:    "an error occurred on the destination",
	0x83:    "the destination is a root directory",
	0x84:    "the operation was cancelled by the user",
	0x85:    "security settings deny the operation",
	0x86:    "the source path is too long",
	0x87:    "the source is a directory with a long path",
	0x88:    "the destination is invalid",
	0x402:   "an unknown error occurred",
	0x10000: "an unspecified error occurred",
	0x2:     "the path was not found",
	0x3:     "the path was not found",
	0x5:     "access denied",
	0x20:    "the file is in use by another process",
	0x21:    "part of the file is locked by another process",
}

// shellErrorMessage returns the readable text for a SHFileOperation result.
func shellErrorMessage(code int) string {
	if msg, ok := shellErrorTable[code]; ok {
		return msg
	}
	return fmt.Sprintf("unknown shell error 0x%X", code)
}

// isInUseCode reports whether a shell result code means a locked file:
// ERROR_SHARING_VIOLATION (32) and ERROR_LOCK_VIOLATION (33).
func isInUseCode(code int) bool {
	return code == errorSharingViolation || code == errorLockViolation
}

// Win32 error codes used to classify shell results. They are plain numbers
// here because this file has no build tag and syscall.Errno constants for
// them exist on Windows only.
const (
	errorSharingViolation = 32
	errorLockViolation    = 33
)
