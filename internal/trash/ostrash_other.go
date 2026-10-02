//go:build !unix && !windows

package trash

import "errors"

// newOSTrasher reports that this platform has no supported OS trash.
func newOSTrasher() (Trasher, error) {
	return nil, errors.New("no OS trash on this platform; brooom cannot remove files here")
}
