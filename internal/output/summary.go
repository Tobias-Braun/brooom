package output

import (
	"bytes"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func init() { Register(summaryFormatter{}) }

// summaryFormatter prints counts and reclaimable bytes per detector.
type summaryFormatter struct{}

func (summaryFormatter) Name() string { return "summary" }

// Write renders the summary. Totals are recomputed from the findings so
// filtered or hand-edited reports stay consistent; the TOTAL row uses the
// deduplicated reclaimable size of the whole report, which can be smaller
// than the sum of the rows when findings nest.
func (summaryFormatter) Write(w io.Writer, r *findings.Report, opts Options) error {
	var buf bytes.Buffer
	p := newPainter(opts.Color)
	t := findings.ComputeTotals(r.Findings)

	switch {
	case opts.Quiet:
		buf.WriteString(p.bold(totalsLine(t)) + "\n")
	case t.Findings == 0:
		writeNothing(&buf, p, r.Errors)
	default:
		renderSummaryTable(&buf, p, t)
	}
	renderErrors(&buf, p, r.Errors)
	_, err := w.Write(buf.Bytes())
	return err
}

// summaryRow is one plain-text row of the summary table.
type summaryRow [4]string

func renderSummaryTable(buf *bytes.Buffer, p painter, t findings.Totals) {
	names := make([]string, 0, len(t.ByDetector))
	for n := range t.ByDetector {
		names = append(names, n)
	}
	slices.Sort(names)

	rows := []summaryRow{{"DETECTOR", "FINDINGS", "ACTIONABLE", "RECLAIMABLE"}}
	for _, n := range names {
		d := t.ByDetector[n]
		rows = append(rows, summaryRow{Sanitize(n), strconv.Itoa(d.Findings), strconv.Itoa(d.Actionable), FormatSize(d.ReclaimableBytes)})
	}
	rows = append(rows, summaryRow{"TOTAL", strconv.Itoa(t.Findings), strconv.Itoa(t.Actionable), FormatSize(t.ReclaimableBytes)})

	var widths [4]int
	for _, row := range rows {
		for i, cell := range row {
			widths[i] = max(widths[i], runeLen(cell))
		}
	}
	last := len(rows) - 1
	for i, row := range rows {
		style := func(s string) string { return s }
		switch i {
		case 0:
			style = p.dim
		case last:
			style = p.bold
		}
		buf.WriteString(summaryLine(row, widths, style) + "\n")
	}
}

// summaryLine pads plain cells first and styles them afterwards. The detector
// column is left-aligned, the numeric columns right-aligned.
func summaryLine(row summaryRow, widths [4]int, style func(string) string) string {
	parts := make([]string, len(row))
	for i, cell := range row {
		if i == 0 {
			parts[i] = style(cell) + spaces(cell, widths[i])
		} else {
			parts[i] = spaces(cell, widths[i]) + style(cell)
		}
	}
	return strings.TrimRight(strings.Join(parts, colSep), " ")
}
