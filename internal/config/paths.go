package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// ExpandPath expands a leading "~", "~/" or "~\" (the user's home directory,
// from os.UserHomeDir so HOME/USERPROFILE overrides work in tests), "$VAR" and
// "${VAR}" references and, on Windows, "%VAR%" references.
//
// An undefined or empty variable is an error rather than an empty expansion:
// "$WORK/x" silently becoming "/x" would point a root at the wrong place.
// "~user" is not supported and left untouched (validation then rejects it as
// a non-absolute path).
func ExpandPath(s string) (string, error) {
	return expandPath(s, runtime.GOOS == "windows", os.LookupEnv, os.UserHomeDir)
}

// expandPath is ExpandPath with its environment injected so the Windows
// syntax can be tested on every platform.
func expandPath(s string, percent bool, lookup func(string) (string, bool), home func() (string, error)) (string, error) {
	prefix := ""
	if s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`) {
		h, err := home()
		if err != nil {
			return "", fmt.Errorf("expand %q: cannot determine home directory: %w", s, err)
		}
		prefix, s = h, s[1:]
	}
	rest, err := expandVars(s, percent, lookup)
	if err != nil {
		return "", err
	}
	// The home directory is inserted after variable expansion so a "$" or
	// "%" inside it is never interpreted.
	return prefix + rest, nil
}

var envNameChars = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

func expandVars(s string, percent bool, lookup func(string) (string, bool)) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); {
		name, n := varAt(s[i:], percent)
		if n == 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		val, ok := lookup(name)
		if !ok || val == "" {
			return "", fmt.Errorf("expand %q: environment variable %s is not set or empty", s, name)
		}
		b.WriteString(val)
		i += n
	}
	return b.String(), nil
}

// varAt recognises a variable reference at the start of s and returns its
// name and length, or length 0 when s does not start with a reference (a lone
// "$" or an unterminated "${" / "%" stays literal).
func varAt(s string, percent bool) (string, int) {
	switch {
	case strings.HasPrefix(s, "${"):
		if end := strings.IndexByte(s, '}'); end > 2 {
			return s[2:end], end + 1
		}
	case s[0] == '$':
		return bareVarAt(s)
	case percent && s[0] == '%':
		if end := strings.IndexByte(s[1:], '%'); end > 0 && envNameChars.MatchString(s[1:1+end]) {
			return s[1 : 1+end], end + 2
		}
	}
	return "", 0
}

// bareVarAt parses "$NAME" at the start of s.
func bareVarAt(s string) (string, int) {
	n := 1
	for n < len(s) && isNameByte(s[n]) {
		n++
	}
	if n == 1 {
		return "", 0
	}
	return s[1:n], n
}

func isNameByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// IsFilesystemRoot reports whether path, after cleaning, is the root of a
// filesystem: "/" on unix; on Windows also a volume root ("C:\", "C:") or a
// UNC share root (`\\server\share`, `\\?\C:\`). Brooom refuses such paths
// because a scan from there would cover the whole machine.
//
// This is the shared helper for every check of that kind (the path argument);
// do not reimplement it elsewhere.
func IsFilesystemRoot(path string) bool {
	if path == "" {
		return false
	}
	return isRootPath(filepath.Clean(path))
}

// pathKey normalises a cleaned path for equality comparison: the default
// filesystems of Windows and macOS are case-insensitive.
func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// pathWithin reports whether target equals or lies below root, comparing
// whole components ("/a/b" does not contain "/a/bc").
//
// It is only a selector for configuration overrides and approximates
// containment lexically (plus best-effort symlink resolution by callers); the
// security boundary for scanned paths remains scope.Guard.
func pathWithin(root, target string) bool {
	rel, err := filepath.Rel(pathKey(root), pathKey(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// canonicalPath cleans p and resolves symlinks on a best-effort basis: if the
// path does not exist (yet) the cleaned path is used.
func canonicalPath(p string) string {
	p = filepath.Clean(p)
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
