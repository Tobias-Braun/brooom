//go:build unix && !darwin

package trash

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// deletionDateLayout is the spec's local time without zone.
const deletionDateLayout = "2006-01-02T15:04:05"

// encodePath percent-encodes s per RFC 3986, keeping unreserved characters
// and "/" as they are. Everything else, including newlines, "%" and every
// byte of multi-byte characters, becomes %XX so the value stays on one line.
func encodePath(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '.', c == '_', c == '~', c == '/':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hex[c>>4])
			b.WriteByte(hex[c&0xf])
		}
	}
	return b.String()
}

// trashInfoContent renders the .trashinfo file for a recorded path.
func trashInfoContent(recordedPath string, at time.Time) string {
	return "[Trash Info]\nPath=" + encodePath(recordedPath) + "\nDeletionDate=" + at.Format(deletionDateLayout) + "\n"
}

// reserveName picks a unique name for base in trashDir and creates its
// .trashinfo with O_EXCL, which is what makes the choice race-free between
// concurrent trashers: the loser gets EEXIST and tries the next suffix. Names
// already used by files/<name> or by an info file without an item (left by a
// crash or another tool) are skipped.
func reserveName(trashDir, base, content string) (name, infoPath string, err error) {
	for n := 1; ; n++ {
		name = base
		if n > 1 {
			name = base + "." + strconv.Itoa(n)
		}
		if _, err := os.Lstat(filepath.Join(trashDir, "files", name)); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", "", fmt.Errorf("cannot inspect trash directory %q: %w", trashDir, err)
		}
		infoPath = filepath.Join(trashDir, "info", name+".trashinfo")
		err = writeExclusive(infoPath, content)
		if err == nil {
			return name, infoPath, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", "", fmt.Errorf("cannot write trash info %q: %w", infoPath, err)
		}
	}
}

func writeExclusive(path, content string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// appendDirSize records a trashed directory in trashDir/directorysizes. The
// cache is optional in the spec, so it is only maintained when the file
// already exists, and failures are ignored: a stale cache is recomputed by
// the desktop, whereas failing here would leave a trashed item unreported.
func appendDirSize(trashDir string, size int64, infoPath, name string) {
	cache := filepath.Join(trashDir, "directorysizes")
	if _, err := os.Stat(cache); err != nil {
		return
	}
	fi, err := os.Stat(infoPath)
	if err != nil {
		return
	}
	f, err := os.OpenFile(cache, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return
	}
	line := fmt.Sprintf("%d %d %s\n", size, fi.ModTime().Unix(), encodePath(name))
	_, _ = f.WriteString(line)
	_ = f.Close()
}

// dropDirSize removes the directorysizes line of name, if the cache exists.
// The file is rewritten through a temp file and rename so a concurrent reader
// never sees a half-written cache. Errors are ignored for the same reason as
// in appendDirSize.
func dropDirSize(trashDir, name string) {
	cache := filepath.Join(trashDir, "directorysizes")
	data, err := os.ReadFile(cache)
	if err != nil {
		return
	}
	want := encodePath(name)
	var kept []string
	changed := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		if line == "" {
			continue
		}
		fields := strings.SplitN(strings.TrimSuffix(line, "\n"), " ", 3)
		if len(fields) == 3 && fields[2] == want {
			changed = true
			continue
		}
		kept = append(kept, line)
	}
	if !changed {
		return
	}
	tmp := cache + ".brooom-tmp"
	if err := os.WriteFile(tmp, []byte(strings.Join(kept, "")), 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, cache); err != nil {
		_ = os.Remove(tmp)
	}
}
