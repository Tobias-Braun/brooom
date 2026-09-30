package output

import (
	"bytes"
	"encoding/json"
	"io"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func init() { Register(jsonFormatter{}) }

// jsonFormatter prints the Report as the documented JSON contract. It is
// meant for scripts and other programs, so Options are ignored: there is
// never any ANSI and Quiet does not drop anything.
type jsonFormatter struct{}

func (jsonFormatter) Name() string { return "json" }

// Write renders a normalized copy of the report with a 2-space indent and a
// trailing newline. HTML escaping is off so paths containing &, < or > stay
// readable. It renders into a buffer first so an encoding problem never leaves
// half a document on the writer.
func (jsonFormatter) Write(w io.Writer, r *findings.Report, _ Options) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalizeReport(r)); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

// normalizeReport returns a copy of r in which every array and map of the
// schema is non-nil, so consumers never see null where the schema promises an
// array or object. The input is not modified. Errors keeps its omitempty
// semantics and is left as is.
func normalizeReport(r *findings.Report) *findings.Report {
	out := *r
	out.Scopes = append([]findings.Scope{}, r.Scopes...)
	out.Findings = make([]findings.Finding, len(r.Findings))
	for i, f := range r.Findings {
		out.Findings[i] = normalizeFinding(f)
	}
	out.Totals.ByDetector = make(map[string]findings.DetectorTotal, len(r.Totals.ByDetector))
	for k, v := range r.Totals.ByDetector {
		out.Totals.ByDetector[k] = v
	}
	return &out
}

// normalizeFinding copies the slices that must render as [] instead of null.
// Copying (instead of assigning to the caller's slice header) keeps the
// caller's finding untouched even though Finding is passed by value.
func normalizeFinding(f findings.Finding) findings.Finding {
	f.Evidence = append([]findings.Evidence{}, f.Evidence...)
	f.RiskFlags = append([]findings.RiskFlag{}, f.RiskFlags...)
	return f
}
