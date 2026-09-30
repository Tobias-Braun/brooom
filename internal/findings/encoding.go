package findings

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// decodeText returns a reader of UTF-8 for a findings file that may have been
// written by a Windows shell. Windows PowerShell 5.1 redirects (`>`) with
// UTF-16 LE and a byte order mark, and other tools add a UTF-8 BOM; both make
// the JSON decoder fail on the first byte. A BOM selects the decoding: UTF-8
// (BOM stripped), UTF-16 LE or UTF-16 BE (transcoded). Input without a BOM is
// passed through as UTF-8. The reader streams, so the size cap of ReadReport
// keeps bounding memory.
func decodeText(r io.Reader) io.Reader {
	br := bufio.NewReader(r)
	head, _ := br.Peek(3)
	switch {
	// Without a BOM, a JSON document starts with an ASCII character (`{` or
	// whitespace), so a NUL in one of the first two bytes gives away UTF-16
	// and its byte order (tools such as `iconv -t UTF-16LE` omit the BOM).
	case len(head) >= 2 && head[0] != 0 && head[1] == 0:
		return &utf16Reader{r: br, order: binary.LittleEndian}
	case len(head) >= 2 && head[0] == 0 && head[1] != 0:
		return &utf16Reader{r: br, order: binary.BigEndian}
	case bytes.HasPrefix(head, []byte{0xEF, 0xBB, 0xBF}):
		_, _ = br.Discard(3)
		return br
	case bytes.HasPrefix(head, []byte{0xFF, 0xFE}):
		_, _ = br.Discard(2)
		return &utf16Reader{r: br, order: binary.LittleEndian}
	case bytes.HasPrefix(head, []byte{0xFE, 0xFF}):
		_, _ = br.Discard(2)
		return &utf16Reader{r: br, order: binary.BigEndian}
	}
	return br
}

// utf16Reader transcodes UTF-16 code units to UTF-8. An unpaired surrogate
// becomes U+FFFD, like utf16.Decode does; a dangling odd byte is an error.
type utf16Reader struct {
	r       *bufio.Reader
	order   binary.ByteOrder
	pending []byte // encoded output not yet handed out
	err     error
}

func (u *utf16Reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(u.pending) == 0 && u.err == nil {
		u.fill()
	}
	if len(u.pending) == 0 {
		return 0, u.err
	}
	n := copy(p, u.pending)
	u.pending = u.pending[n:]
	return n, nil
}

// fill decodes one code point into pending, or records the error.
func (u *utf16Reader) fill() {
	unit, err := u.unit()
	if err != nil {
		u.err = err
		return
	}
	r := rune(unit)
	if utf16.IsSurrogate(r) {
		r = utf8.RuneError
		if next, perr := u.r.Peek(2); perr == nil {
			if dec := utf16.DecodeRune(rune(unit), rune(u.order.Uint16(next))); dec != utf8.RuneError {
				_, _ = u.r.Discard(2)
				r = dec
			}
		}
	}
	u.pending = utf8.AppendRune(u.pending[:0], r)
}

func (u *utf16Reader) unit() (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(u.r, b[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, errors.New("invalid UTF-16 input: odd number of bytes")
		}
		return 0, err
	}
	return u.order.Uint16(b[:]), nil
}
