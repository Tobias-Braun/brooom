//go:build unix && !darwin

package trash

// newOSTrasher returns the freedesktop.org trash implementation used on
// Linux and the BSDs.
func newOSTrasher(opts Options) (Trasher, error) {
	return nil, errNotImplemented
}
