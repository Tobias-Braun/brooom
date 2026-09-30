package action

import "os"

// fileID is the identity of one file system object: two entries are the same
// object exactly when their IDs compare equal, whatever their spelling.
type fileID interface {
	sameAs(other fileID) bool
}

// identityOf reads the identity of path, following a final symlink only when
// follow is set. An error other than "absent" means the identity is unknown
// and callers must fail closed. It is a variable so tests can inject
// failures a privileged test run cannot provoke.
var identityOf = platformIdentity

// statID is the identity of an os.FileInfo (device and inode on Unix).
type statID struct{ fi os.FileInfo }

func (s statID) sameAs(o fileID) bool {
	other, ok := o.(statID)
	return ok && os.SameFile(s.fi, other.fi)
}
