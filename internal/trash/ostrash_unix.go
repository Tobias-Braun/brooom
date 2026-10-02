//go:build unix && !darwin

package trash

import (
	"os"
	"time"
)

// newOSTrasher returns the freedesktop.org trash implementation used on
// Linux and the BSDs. The trash location is resolved once, from the
// environment at construction time.
func newOSTrasher() (Trasher, error) {
	home, err := homeTrashDir(os.Getenv)
	if err != nil {
		return nil, err
	}
	return &freedesktop{
		homeTrash: home,
		uid:       os.Getuid(),
		now:       time.Now,
		deviceOf:  deviceOf,
		lstat:     os.Lstat,
		ownerOf:   statOwner,
	}, nil
}
