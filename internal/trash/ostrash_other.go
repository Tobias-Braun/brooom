//go:build !unix && !windows

package trash

import "errors"

// newOSTrasher reports that this platform has no supported OS trash; use the
// quarantine strategy instead.
func newOSTrasher(opts Options) (Trasher, error) {
	return nil, errors.New("no OS trash on this platform; use --trash-strategy quarantine")
}
