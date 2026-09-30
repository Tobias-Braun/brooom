package walk

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// listDir reads the raw directory entries. It is a variable so tests can
// count directory reads and prove that a warm cache issues none.
var listDir = func(dir string) ([]os.DirEntry, error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Unsorted (-1) is cheaper than os.ReadDir and sibling order is
	// unspecified anyway.
	return f.ReadDir(-1)
}

// readDir lists dir and returns one Entry per child with Path, Name, Type,
// sizes and ModTime filled in (Rel and Depth are the caller's business). A
// child that vanished between listing and Info is dropped silently, other
// Info errors go to onErr. A directory read error is returned together with
// whatever entries were read before it.
func readDir(dir string, onErr func(path string, err error)) ([]Entry, error) {
	des, err := listDir(dir)
	entries := make([]Entry, 0, len(des))
	for _, de := range des {
		path := filepath.Join(dir, de.Name())
		info, ierr := de.Info()
		if ierr != nil {
			if !errors.Is(ierr, fs.ErrNotExist) {
				onErr(path, ierr)
			}
			continue
		}
		entries = append(entries, entryFromInfo(path, info))
	}
	return entries, err
}
