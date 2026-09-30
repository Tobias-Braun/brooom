package findings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// MaxReportBytes caps how much of a findings file ReadReport consumes. A real
// report is a few megabytes at most; the cap keeps a hostile or accidental
// endless input (a device, a pipe) from exhausting memory.
const MaxReportBytes = 256 << 20

// ReadReport decodes a Report from r, as written by `--format json`. The
// input is untrusted: it may have been edited by hand, produced on another
// machine or tampered with, so the loader only checks the envelope. Callers
// must still validate every finding before acting on it.
//
// A UTF-8 BOM or a UTF-16 LE/BE file (what Windows PowerShell 5.1 writes for
// `> file`) is decoded transparently (see decodeText).
//
// Unknown JSON fields are tolerated so later, backwards-compatible additions
// to the schema do not break older binaries. A missing or zero schema_version
// means the input is not a findings file at all, a newer one means this
// binary cannot know the semantics; both are rejected. Older versions are
// accepted (there is only version 1 today).
func ReadReport(r io.Reader) (*Report, error) {
	lr := &io.LimitedReader{R: r, N: MaxReportBytes + 1}
	dec := json.NewDecoder(decodeText(lr))
	var rep Report
	err := dec.Decode(&rep)
	switch {
	case lr.N <= 0:
		return nil, fmt.Errorf("input is larger than %d MiB", MaxReportBytes>>20)
	case errors.Is(err, io.EOF):
		return nil, errors.New("input is empty")
	case err != nil:
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	// A second JSON value means the file is not a single report (for example
	// two concatenated scans or an ndjson stream), and guessing is unsafe.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid JSON: unexpected data after the report")
	}
	if err := checkSchemaVersion(rep.SchemaVersion); err != nil {
		return nil, err
	}
	return &rep, nil
}

// checkSchemaVersion rejects versions this binary cannot interpret.
func checkSchemaVersion(v int) error {
	switch {
	case v <= 0:
		return errors.New("not a brooom findings file: schema_version is missing or zero")
	case v > SchemaVersion:
		return fmt.Errorf("findings file has schema_version %d but this brooom only understands up to %d; upgrade brooom", v, SchemaVersion)
	}
	return nil
}
