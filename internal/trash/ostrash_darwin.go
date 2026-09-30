package trash

import "os"

// newOSTrasher returns the macOS Trash implementation. The home directory is
// only needed by the ~/.Trash fallback, so a missing one is not an error here:
// the fallback reports it for the item that needs it.
func newOSTrasher(opts Options) (Trasher, error) {
	home, _ := os.UserHomeDir()
	return newMacTrash(home), nil
}
