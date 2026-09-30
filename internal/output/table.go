package output

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// minPathWidth is the narrowest the PATH column gets when fitting a width;
// lines overflow the terminal rather than turn the path into an unreadable
// stub. The table never wraps.
const minPathWidth = 20

// colSep separates table columns.
const colSep = "  "

func init() { Register(tableFormatter{}) }

// tableFormatter is the default human-readable format: findings grouped by
// detector with per-group and overall totals. It is a pure function of the
// report and Options; it never touches the filesystem or the terminal.
type tableFormatter struct{}

func (tableFormatter) Name() string { return "table" }

// Write renders the grouped table. It renders into a buffer first so a
// formatting problem never leaves half a table on the writer.
func (tableFormatter) Write(w io.Writer, r *findings.Report, opts Options) error {
	var buf bytes.Buffer
	p := newPainter(opts.Color)
	sorted := sortedCopy(r.Findings)

	switch {
	case len(sorted) == 0:
		if !opts.Quiet {
			writeNothing(&buf, p, r.Errors)
		}
	case opts.Quiet:
		renderRows(&buf, p, newLayout(sorted, opts.Width), sorted)
	default:
		renderGroups(&buf, p, sorted, opts)
	}
	renderErrors(&buf, p, r.Errors)
	_, err := w.Write(buf.Bytes())
	return err
}

// sortedCopy sorts a copy so the caller's report is never mutated and
// filtered or hand-edited reports render in a stable order.
func sortedCopy(fs []findings.Finding) []findings.Finding {
	out := slices.Clone(fs)
	findings.Sort(out)
	return out
}

// writeNothing prints the empty-state message, shared with the summary and
// tree formats. It stays silent when scan errors exist: an empty result of a
// scan that failed must not read as a clean bill of health.
func writeNothing(buf *bytes.Buffer, p painter, errs []findings.ScanError) {
	if len(errs) > 0 {
		return
	}
	buf.WriteString(p.green("Nothing to sweep.") + "\n")
}

// renderErrors lists non-fatal scan errors at the end of the output, separated
// from what precedes by a blank line.
func renderErrors(buf *bytes.Buffer, p painter, errs []findings.ScanError) {
	if len(errs) == 0 {
		return
	}
	if buf.Len() > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(p.red(fmt.Sprintf("Scan errors (%d):", len(errs))) + "\n")
	for _, e := range errs {
		var parts []string
		for _, s := range []string{e.Detector, e.Path} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		line := Sanitize(strings.Join(parts, " "))
		if line != "" {
			line += ": "
		}
		buf.WriteString("  " + line + Sanitize(e.Message) + "\n")
	}
}

// totalsLine is the shared wording of group and overall totals.
func totalsLine(t findings.Totals) string {
	noun := "findings"
	if t.Findings == 1 {
		noun = "finding"
	}
	return fmt.Sprintf("%d %s, %d actionable, %s reclaimable", t.Findings, noun, t.Actionable, FormatSize(t.ReclaimableBytes))
}

// renderGroups renders one block per detector followed by the overall total.
// Totals are recomputed from the findings, not read from Report.Totals, so
// filtered reports stay consistent.
func renderGroups(buf *bytes.Buffer, p painter, sorted []findings.Finding, opts Options) {
	for _, group := range groupByDetector(sorted) {
		l := newLayout(group, opts.Width)
		buf.WriteString(p.bold(groupHeader(group[0].Detector, len(group))) + "\n")
		buf.WriteString(l.render(func(c colID) string { return colHeaders[c] }, func(_ colID, s string) string { return p.dim(s) }) + "\n")
		renderRows(buf, p, l, group)
		buf.WriteString(p.dim(totalsLine(findings.ComputeTotals(group))) + "\n\n")
	}
	buf.WriteString(p.bold(totalsLine(findings.ComputeTotals(sorted))) + "\n")
}

// groupByDetector splits an already sorted slice into per-detector runs.
func groupByDetector(sorted []findings.Finding) [][]findings.Finding {
	var groups [][]findings.Finding
	for _, f := range sorted {
		if n := len(groups); n > 0 && groups[n-1][0].Detector == f.Detector {
			groups[n-1] = append(groups[n-1], f)
			continue
		}
		groups = append(groups, []findings.Finding{f})
	}
	return groups
}

// groupHeader is "<detector> - <description> (<count>)". The description comes
// from the detector registry because the JSON schema deliberately does not
// carry it; reports loaded from a file may name unregistered detectors.
func groupHeader(name string, count int) string {
	name = Sanitize(name)
	if d, ok := detect.Get(name); ok && d.Description() != "" {
		return fmt.Sprintf("%s - %s (%d)", name, d.Description(), count)
	}
	return fmt.Sprintf("%s (%d)", name, count)
}

// colID identifies a table column.
type colID int

const (
	colSize colID = iota
	colAge
	colConf
	colAction
	colPath
	colRef
	colFlags
)

var colHeaders = map[colID]string{
	colSize: "SIZE", colAge: "AGE", colConf: "CONF", colAction: "ACTION",
	colPath: "PATH", colRef: "REF", colFlags: "FLAGS",
}

// layout holds the visible columns, their widths and the path truncation
// limit for one block of rows.
type layout struct {
	cols    []colID
	widths  map[colID]int
	pathMax int // 0 = no truncation
}

// newLayout picks the columns (REF and FLAGS only when some row needs them)
// and computes widths from the plain text. With a known terminal width only
// the PATH column shrinks.
func newLayout(fs []findings.Finding, width int) layout {
	l := layout{cols: []colID{colSize, colAge, colConf, colAction, colPath}, widths: map[colID]int{}}
	if slices.ContainsFunc(fs, func(f findings.Finding) bool { return f.Ref != "" }) {
		l.cols = append(l.cols, colRef)
	}
	if slices.ContainsFunc(fs, func(f findings.Finding) bool { return len(f.RiskFlags) > 0 }) {
		l.cols = append(l.cols, colFlags)
	}
	for _, c := range l.cols {
		l.widths[c] = runeLen(colHeaders[c])
		for _, f := range fs {
			l.widths[c] = max(l.widths[c], runeLen(cellText(f, c)))
		}
	}
	l.fitPath(width)
	return l
}

// fitPath shrinks only the PATH column so the line fits width, never below
// minPathWidth. Width 0 means unknown: nothing is truncated.
func (l *layout) fitPath(width int) {
	if width <= 0 {
		return
	}
	fixed := len(colSep) * (len(l.cols) - 1)
	for _, c := range l.cols {
		if c != colPath {
			fixed += l.widths[c]
		}
	}
	avail := max(width-fixed, minPathWidth)
	if l.widths[colPath] > avail {
		l.widths[colPath] = avail
		l.pathMax = avail
	}
}

// cellText returns the untruncated plain text of one cell.
func cellText(f findings.Finding, c colID) string {
	switch c {
	case colSize:
		return sizeCell(f)
	case colAge:
		if f.LastModified == nil {
			return "-"
		}
		return FormatAge(f.AgeDays)
	case colConf:
		return string(f.Confidence)
	case colAction:
		return string(f.SuggestedAction.Type)
	case colPath:
		return Sanitize(relPath(f.Scope.Path, f.Path))
	case colRef:
		return Sanitize(f.Ref)
	default:
		return ShortRiskFlags(f.RiskFlags)
	}
}

// sizeCell shows "-" for objects that have no size on disk.
func sizeCell(f findings.Finding) string {
	if f.SizeBytes == 0 && (f.Kind == findings.KindBranch || f.Kind == findings.KindWorktreeMissing) {
		return "-"
	}
	return FormatSize(f.SizeBytes)
}

// renderRows writes all rows; flagged rows are followed by their reason.
func renderRows(buf *bytes.Buffer, p painter, l layout, fs []findings.Finding) {
	for _, f := range fs {
		text := func(c colID) string { return cellText(f, c) }
		if f.Actionable() {
			buf.WriteString(l.render(text, func(c colID, s string) string { return cellStyle(p, c, f, s) }) + "\n")
			continue
		}
		// Dim the whole row so it is never mistaken for a cleanup candidate.
		buf.WriteString(p.dim(l.render(text, nil)) + "\n")
		buf.WriteString(p.dim("  -> "+notSuggestedReason(f)) + "\n")
	}
}

// notSuggestedReason explains a flagged row: the detector's reason, else the
// blocking flags, else a generic statement, so a dimmed row is never silent.
func notSuggestedReason(f findings.Finding) string {
	if r := f.SuggestedAction.Reason; r != "" {
		return Sanitize(r)
	}
	if l := blockingLabels(f.RiskFlags); l != "" {
		return l
	}
	return "no action suggested"
}

// render assembles one line from plain cell text. Truncation and padding are
// computed on the plain text and style (may be nil) is applied afterwards, so
// stripping escapes yields the uncolored output exactly. SIZE is right-aligned;
// trailing spaces are trimmed.
func (l layout) render(text func(colID) string, style func(colID, string) string) string {
	parts := make([]string, 0, len(l.cols))
	for _, c := range l.cols {
		plain := truncateMiddle(text(c), l.pathLimit(c))
		styled := plain
		if style != nil {
			styled = style(c, plain)
		}
		if c == colSize {
			parts = append(parts, spaces(plain, l.widths[c])+styled)
		} else {
			parts = append(parts, styled+spaces(plain, l.widths[c]))
		}
	}
	return strings.TrimRight(strings.Join(parts, colSep), " ")
}

// pathLimit is the truncation width for a column: only PATH is ever cut.
func (l layout) pathLimit(c colID) int {
	if c == colPath {
		return l.pathMax
	}
	return 0
}

// cellStyle colors confidence and risk flags of a regular row.
func cellStyle(p painter, c colID, f findings.Finding, s string) string {
	switch c {
	case colConf:
		switch f.Confidence {
		case findings.ConfidenceHigh:
			return p.green(s)
		case findings.ConfidenceMedium:
			return p.yellow(s)
		default:
			return p.dim(s)
		}
	case colFlags:
		return p.yellow(s)
	}
	return s
}
