package output

import (
	"bytes"
	"encoding/json"
	"io"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func init() { Register(ndjsonFormatter{}) }

// encodeFinding renders one finding as a compact, newline-terminated JSON
// line with the same normalization and escaping rules as the json format.
func encodeFinding(f findings.Finding) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf) // Encode appends the terminating newline
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalizeFinding(f)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ndjsonFormatter prints one Finding per line. Report.Errors, totals and
// scopes are not part of the stream; use json for those.
type ndjsonFormatter struct{}

func (ndjsonFormatter) Name() string { return "ndjson" }

// Write renders all findings; an empty report writes zero bytes.
func (ndjsonFormatter) Write(w io.Writer, r *findings.Report, _ Options) error {
	var buf bytes.Buffer
	for _, f := range r.Findings {
		line, err := encodeFinding(f)
		if err != nil {
			return err
		}
		buf.Write(line)
	}
	if buf.Len() == 0 {
		return nil
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// NewStream returns a callback that writes each finding as it arrives and a
// finish function reporting the first write error. The plain func types are
// deliberate: the scan command consumes them through its own small interface
// without importing a named type from this package.
func (ndjsonFormatter) NewStream(w io.Writer, _ Options) (onFinding func(findings.Finding), finish func() error) {
	s := NewNDJSONStream(w)
	return s.OnFinding, s.Err
}

// NDJSONStream writes findings to a writer one line at a time while a scan is
// still running, so a pipe consumer sees progress immediately. It is safe for
// concurrent use because detectors emit from several goroutines. Nothing is
// buffered: every finding is one Write call.
type NDJSONStream struct {
	mu  sync.Mutex
	w   io.Writer
	err error
}

// NewNDJSONStream returns a stream writing to w.
func NewNDJSONStream(w io.Writer) *NDJSONStream { return &NDJSONStream{w: w} }

// OnFinding writes f as one line. After the first write error all later
// findings are ignored (a closed pipe stays closed) and the error is kept for
// Err. Its signature matches detect.RunOptions.OnFinding.
func (s *NDJSONStream) OnFinding(f findings.Finding) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return
	}
	line, err := encodeFinding(f)
	if err == nil {
		_, err = s.w.Write(line)
	}
	s.err = err
}

// Err returns the first error encountered, or nil.
func (s *NDJSONStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
