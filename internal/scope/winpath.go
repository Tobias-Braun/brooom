package scope

import (
	"errors"
	"fmt"
	"strings"
)

// This file holds the pure string logic for Windows path syntax. It has no
// build tag so it can be tested on every platform; only path_windows.go wires
// it into the resolver, and path_other.go turns it off elsewhere (a backslash
// or colon is an ordinary file name character on unix).

// winReservedNames are device names that Windows resolves in every directory,
// with or without an extension ("C:\work\NUL.txt" is the NUL device).
var winReservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// winNormalizeExtended strips the verbatim prefixes so that
// `\\?\C:\dir` and `\\?\UNC\server\share\dir` compare like `C:\dir` and
// `\\server\share\dir`. Other verbatim forms (Volume{...}, GLOBALROOT) are
// returned unchanged and refused by winCheckInput.
func winNormalizeExtended(p string) string {
	s := strings.ReplaceAll(p, "/", `\`)
	const unc = `\\?\UNC\`
	if len(s) >= len(unc) && strings.EqualFold(s[:len(unc)], unc) {
		return `\\` + s[len(unc):]
	}
	if strings.HasPrefix(s, `\\?\`) && len(s) >= 6 && isDriveLetter(s[4]) && s[5] == ':' {
		return s[4:]
	}
	return p
}

// isDriveLetter reports whether c is an ASCII letter.
func isDriveLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// winCheckInput refuses device paths: the `\\.\` namespace, NT object paths
// and verbatim paths that are neither a drive nor a UNC share
// (`\\?\GLOBALROOT`, `\\?\Volume{...}`). They address things outside any
// directory tree the guard could reason about.
func winCheckInput(p string) error {
	s := strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(s, `\\.\`):
		return errors.New("device paths are not allowed")
	case strings.HasPrefix(s, `\??\`):
		return errors.New("NT object paths are not allowed")
	case strings.HasPrefix(s, `\\?\`) && winNormalizeExtended(s) == s:
		return errors.New("verbatim device or volume paths are not allowed")
	}
	return nil
}

// winRejectComponent refuses file name components that Windows interprets
// specially: alternate data streams and wildcard characters, trailing dots and
// spaces (silently dropped by Win32, so the name would not be what it seems)
// and reserved device names.
func winRejectComponent(c string) error {
	if c == "." || c == ".." {
		return nil
	}
	if strings.ContainsAny(c, "<>:\"|?*") {
		return fmt.Errorf("component %q contains a character Windows treats specially (alternate data stream or wildcard)", c)
	}
	for _, r := range c {
		if r < 0x20 {
			return fmt.Errorf("component %q contains a control character", c)
		}
	}
	if last := c[len(c)-1]; last == '.' || last == ' ' {
		return fmt.Errorf("component %q ends in a dot or space, which Windows strips", c)
	}
	stem := c
	if i := strings.IndexByte(c, '.'); i >= 0 {
		stem = c[:i]
	}
	if winReservedNames[strings.ToUpper(strings.TrimRight(stem, " "))] {
		return fmt.Errorf("component %q is a reserved device name", c)
	}
	return nil
}
