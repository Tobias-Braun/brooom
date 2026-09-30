package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Store reads and writes manifests in a sessions directory.
type Store struct {
	Dir string
}

// NewStore returns a store rooted at dir (usually config.Dirs.Sessions).
func NewStore(dir string) *Store { return &Store{Dir: dir} }

const manifestExt = ".json"

// validID rejects ids that could address anything outside the sessions
// directory: empty, separators, "..", NUL.
func validID(id string) error {
	if id == "" {
		return errors.New("session id is empty")
	}
	if strings.ContainsAny(id, "/\\\x00") || strings.Contains(id, "..") {
		return fmt.Errorf("invalid session id %q", id)
	}
	return nil
}

// Save writes the manifest atomically: a temp file in the same directory is
// written and fsynced, then renamed over <id>.json, so readers and crashes
// only ever see a complete file.
func (s *Store) Save(m *Manifest) error {
	if err := validID(m.ID); err != nil {
		return err
	}
	if m.Version == 0 {
		m.Version = ManifestVersion
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return fmt.Errorf("create sessions dir %s: %w", s.Dir, err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode session %s: %w", m.ID, err)
	}
	final := filepath.Join(s.Dir, m.ID+manifestExt)
	if err := writeAtomic(s.Dir, m.ID, final, data); err != nil {
		return fmt.Errorf("save session %s: %w", final, err)
	}
	syncDir(s.Dir)
	return nil
}

// writeAtomic writes data to a temp file next to final and renames it into
// place, removing the temp file on any failure.
func writeAtomic(dir, id, final string, data []byte) error {
	f, err := os.CreateTemp(dir, "."+id+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	if err := fillTemp(f, data); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// fillTemp sets permissions, writes, fsyncs and closes f.
func fillTemp(f *os.File, data []byte) error {
	// CreateTemp already uses 0600; the explicit Chmod documents the
	// requirement. It is a no-op or unsupported on some platforms.
	_ = f.Chmod(0o600)
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir flushes the directory entry of the rename where the platform
// supports it. Failure is ignored: the data itself is already synced.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Load reads a manifest by full id or unique prefix. An exact match always
// wins over prefix matching.
func (s *Store) Load(idOrPrefix string) (*Manifest, error) {
	if err := validID(idOrPrefix); err != nil {
		return nil, err
	}
	m, err := s.readFile(filepath.Join(s.Dir, idOrPrefix+manifestExt))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return m, err
	}
	ids, err := s.ids()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read sessions dir %s: %w", s.Dir, err)
	}
	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(id, idOrPrefix) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("%w: %s", ErrNotFound, idOrPrefix)
	case 1:
		return s.readFile(filepath.Join(s.Dir, matches[0]+manifestExt))
	}
	sort.Strings(matches)
	return nil, fmt.Errorf("%w: %q matches %s", ErrAmbiguous, idOrPrefix, strings.Join(matches, ", "))
}

// ids lists the ids of all <id>.json files. Temp files (dot-prefixed, .tmp)
// never match.
func (s *Store) ids() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, manifestExt) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, manifestExt))
	}
	return ids, nil
}

func (s *Store) readFile(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if m.Version != ManifestVersion {
		return nil, fmt.Errorf("%s: unsupported manifest version %d (this brooom supports %d)", path, m.Version, ManifestVersion)
	}
	if m.ID == "" {
		return nil, fmt.Errorf("%s: manifest has no id", path)
	}
	return &m, nil
}

// List returns all readable manifests, newest first (StartedAt, ties by ID
// descending). Unusable files are returned as problems; the error is only
// set when the directory itself cannot be read. A missing directory is an
// empty history.
func (s *Store) List() ([]*Manifest, []Problem, error) {
	ids, err := s.ids()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read sessions dir %s: %w", s.Dir, err)
	}
	var list []*Manifest
	var problems []Problem
	for _, id := range ids {
		file := filepath.Join(s.Dir, id+manifestExt)
		m, err := s.readFile(file)
		if err != nil {
			problems = append(problems, Problem{File: file, Err: err})
			continue
		}
		list = append(list, m)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].StartedAt.Equal(list[j].StartedAt) {
			return list[i].StartedAt.After(list[j].StartedAt)
		}
		return list[i].ID > list[j].ID
	})
	return list, problems, nil
}

// Latest returns the newest manifest or ErrNotFound.
func (s *Store) Latest() (*Manifest, error) {
	list, _, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("%w: no sessions yet", ErrNotFound)
	}
	return list[0], nil
}
