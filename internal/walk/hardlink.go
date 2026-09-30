package walk

// HardLinkID identifies the file behind an entry that is a regular file with
// more than one hard link. Callers that sum sizes themselves use it to count
// such a file once, exactly like DirSize does, so their totals agree with the
// rest of Brooom. It reports false for everything else, and always on Windows
// where no file identity is available.
func (e Entry) HardLinkID() (string, bool) {
	if e.Type.IsRegular() && e.fid.ok && e.fid.nlink > 1 {
		return e.fid.String(), true
	}
	return "", false
}
