//go:build !darwin

package procs

// platformListing is nil where no single listing is cheaper than per-path
// queries (Linux reads /proc once per call already; Windows asks the Restart
// Manager per file), so a Snapshot simply delegates to OpenFiles there.
var platformListing Listing
