package trash

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"
)

// sessionDirPattern is the shape of a session id (see session.NewID, e.g.
// "20260929-224501-3f9a"). Only directories with such a name are ever
// considered session directories, so anything else a user keeps in the
// quarantine directory is never listed and never deleted. The trash package
// cannot import session (session imports trash), hence the local copy.
var sessionDirPattern = regexp.MustCompile(`^\d{8}-\d{6}-[0-9a-f]{4,}$`)

// QuarantinedSession is one <QuarantineDir>/<session-id>/ directory.
type QuarantinedSession struct {
	// ID is the directory name, which is the session id.
	ID string
	// Dir is the absolute path of the session directory.
	Dir string
	// CreatedAt is the manifest's created_at, or the directory's mtime when
	// the manifest is missing, corrupt or has no usable time.
	CreatedAt time.Time
	// SizeBytes sums the manifest's item sizes, or is measured recursively
	// when the manifest cannot be used.
	SizeBytes int64
	// FromManifest is false when time and size are fallbacks.
	FromManifest bool
}

// Age is how long the session has been in quarantine at now.
func (s QuarantinedSession) Age(now time.Time) time.Duration { return now.Sub(s.CreatedAt) }

// QuarantineListing is the result of ListQuarantine.
type QuarantineListing struct {
	// Expired are the sessions older than the retention, oldest first.
	Expired []QuarantinedSession
	// Skipped names entries that look like sessions but are symlinks: they
	// are never followed or deleted, only reported.
	Skipped []string
}

// TotalBytes sums the size of the expired sessions.
func (l *QuarantineListing) TotalBytes() int64 {
	var n int64
	for _, s := range l.Expired {
		n += s.SizeBytes
	}
	return n
}

// ListQuarantine lists the sessions in dir that are older than retentionDays
// at now. Zero (or a negative value) means never expire and lists nothing. A
// missing quarantine directory is an empty listing. Time and size come from
// each session's manifest.json (one small file read); only a session without
// a usable manifest is walked recursively.
func ListQuarantine(dir string, now time.Time, retentionDays int) (*QuarantineListing, error) {
	res := &QuarantineListing{}
	if retentionDays <= 0 {
		return res, nil
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return res, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read quarantine directory %s: %w", dir, err)
	}
	cutoff := time.Duration(retentionDays) * 24 * time.Hour
	for _, e := range entries {
		if !sessionDirPattern.MatchString(e.Name()) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			res.Skipped = append(res.Skipped, path)
			continue
		}
		if !e.IsDir() {
			continue
		}
		s := inspectSession(path, e.Name())
		if s.Age(now) > cutoff {
			res.Expired = append(res.Expired, s)
		}
	}
	sort.Slice(res.Expired, func(i, j int) bool { return res.Expired[i].CreatedAt.Before(res.Expired[j].CreatedAt) })
	return res, nil
}

// inspectSession reads the session's manifest and falls back to the directory
// mtime and a recursive size for whatever the manifest cannot tell.
func inspectSession(path, id string) QuarantinedSession {
	s := QuarantinedSession{ID: id, Dir: path}
	if m, err := loadQuarantineManifest(path); err == nil && !m.CreatedAt.IsZero() {
		s.CreatedAt = m.CreatedAt
		s.FromManifest = true
		for _, it := range m.Items {
			s.SizeBytes += it.SizeBytes
		}
		return s
	}
	if fi, err := os.Lstat(path); err == nil {
		s.CreatedAt = fi.ModTime()
	}
	s.SizeBytes, _ = treeSize(path)
	return s
}

// PurgeResult is the outcome for one session directory.
type PurgeResult struct {
	Session QuarantinedSession
	// Err is nil when the directory was deleted.
	Err error
}

// Purge permanently deletes the given session directories below dir. The
// sessions are not trusted: each target is rebuilt as <dir>/<id> from a
// validated id (so nothing outside dir can be addressed), must be a plain
// directory (a symlink is refused, never followed) and is removed with
// removeTree, which clears read-only attributes on Windows. One failing
// session does not stop the others.
func Purge(dir string, sessions []QuarantinedSession) []PurgeResult {
	out := make([]PurgeResult, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, PurgeResult{Session: s, Err: purgeOne(dir, s)})
	}
	return out
}

func purgeOne(dir string, s QuarantinedSession) error {
	if !sessionDirPattern.MatchString(s.ID) {
		return fmt.Errorf("refusing to purge %q: not a session directory name", s.ID)
	}
	target := filepath.Join(dir, s.ID)
	if filepath.Clean(s.Dir) != target {
		return fmt.Errorf("refusing to purge %s: not directly inside the quarantine directory %s", s.Dir, dir)
	}
	fi, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot inspect %s: %w", target, err)
	}
	if isSymlink(fi) || !fi.IsDir() {
		return fmt.Errorf("refusing to purge %s: not a plain directory", target)
	}
	if err := removeTree(target); err != nil {
		return fmt.Errorf("cannot delete %s: %w", target, err)
	}
	return nil
}
