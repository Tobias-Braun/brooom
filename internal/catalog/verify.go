package catalog

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

// Verifier names of Entry.Verify. A pattern such as "core" or "*.dmp" is only
// a name; a build script called core or an Oracle Data Pump export called
// prod-export.dmp matches it just as well, and losing one of those is
// permanent under the delete strategy. A verifier therefore checks the file
// header before a match may be reported.
const (
	// VerifyCoreDump requires the header of a Unix core dump: an ELF file of
	// type ET_CORE, or a Mach-O file of type MH_CORE.
	VerifyCoreDump = "core-dump"
	// VerifyMinidump requires the signature of a Windows dump: MDMP
	// (minidump), PAGEDUMP or PAGEDU64 (kernel dumps).
	VerifyMinidump = "minidump"
)

var knownVerifiers = []string{VerifyCoreDump, VerifyMinidump}

// headerLen covers the longest header field read below (the Mach-O file type
// at offset 12 plus its 4 bytes, the ELF e_type at offset 16 plus 2 bytes).
const headerLen = 20

// VerifyFile reports whether the file at path carries the header that the
// verifier expects. Anything that cannot be proven to match (unknown
// verifier, symlink, non-regular file, unreadable or short file) is false, so
// an unverifiable candidate is never reported. It reads at most headerLen
// bytes and never modifies anything.
func VerifyFile(verifier, path string) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, headerLen)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return false
	}
	return verifyHeader(verifier, buf[:n])
}

// verifyHeader checks the leading bytes of a file against a verifier.
func verifyHeader(verifier string, h []byte) bool {
	switch verifier {
	case VerifyCoreDump:
		return isELFCore(h) || isMachOCore(h)
	case VerifyMinidump:
		return hasPrefix(h, "MDMP") || hasPrefix(h, "PAGEDUMP") || hasPrefix(h, "PAGEDU64")
	}
	return false
}

func hasPrefix(h []byte, s string) bool {
	return len(h) >= len(s) && string(h[:len(s)]) == s
}

// isELFCore accepts an ELF file whose e_type (offset 16, byte order given by
// EI_DATA at offset 5) is ET_CORE (4).
func isELFCore(h []byte) bool {
	if len(h) < 18 || !hasPrefix(h, "\x7fELF") {
		return false
	}
	var order binary.ByteOrder
	switch h[5] {
	case 1:
		order = binary.LittleEndian
	case 2:
		order = binary.BigEndian
	default:
		return false
	}
	return order.Uint16(h[16:18]) == 4
}

// isMachOCore accepts a thin Mach-O file whose filetype (offset 12) is
// MH_CORE (4); the magic decides the byte order of the header.
func isMachOCore(h []byte) bool {
	if len(h) < 16 {
		return false
	}
	switch binary.BigEndian.Uint32(h[:4]) {
	case 0xfeedface, 0xfeedfacf: // big-endian header
		return binary.BigEndian.Uint32(h[12:16]) == 4
	case 0xcefaedfe, 0xcffaedfe: // little-endian header
		return binary.LittleEndian.Uint32(h[12:16]) == 4
	}
	return false
}
