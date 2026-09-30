package output

import (
	"fmt"
	"path/filepath"
	"strings"
)

// sizeUnits are the decimal SI units used for sizes. Decimal (1000) rather
// than binary units match what file managers on macOS and most Linux tools
// show, so numbers in the table agree with what users see elsewhere.
var sizeUnits = []string{"B", "kB", "MB", "GB", "TB", "PB"}

// FormatSize renders a byte count with decimal SI units: whole bytes below
// 1000, otherwise exactly one decimal (1.0 kB, 1.2 GB). Negative input renders
// as "0 B". The value is rounded in integer arithmetic so a result like
// 999.95 kB carries over to "1.0 MB" instead of printing "1000.0 kB".
func FormatSize(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	v := uint64(n)
	unit, div := 1, uint64(1000)
	for unit < len(sizeUnits)-1 && v >= div*1000 {
		unit++
		div *= 1000
	}
	// Tenths of the unit, rounded half up.
	tenths := (v + div/20) / (div / 10)
	if tenths >= 10000 && unit < len(sizeUnits)-1 {
		unit++
		div *= 1000
		tenths = (v + div/20) / (div / 10)
	}
	return fmt.Sprintf("%d.%d %s", tenths/10, tenths%10, sizeUnits[unit])
}

// FormatAge renders an age in whole days compactly: days below 30, months
// (30 days each) below a year, then years (365 days each). Negative input
// renders as "0d".
func FormatAge(days int) string {
	switch {
	case days < 30:
		return fmt.Sprintf("%dd", max(days, 0))
	case days < 365:
		return fmt.Sprintf("%dmo", days/30)
	default:
		return fmt.Sprintf("%dy", days/365)
	}
}

// truncateMiddle shortens s to at most width runes by replacing the middle
// with an ellipsis. The tail gets the larger share so file names stay
// visible. Width is counted in runes; East-Asian wide characters and
// combining marks are not measured specially, which keeps the helper
// dependency-free at the cost of slightly misaligned columns for such paths.
// A width of zero or less means "no limit".
func truncateMiddle(s string, width int) string {
	r := []rune(s)
	if width <= 0 || len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	head := (width - 1) / 2
	tail := width - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// relPath renders path relative to the scope path ("." when equal). Paths
// that are not below the scope, or for which no relative form exists, are
// returned absolute so they are never mistaken for something inside the scope.
func relPath(scopePath, path string) string {
	if scopePath == "" {
		return path
	}
	rel, err := filepath.Rel(scopePath, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return rel
}

// runeLen is the display width used for alignment (see truncateMiddle).
func runeLen(s string) int {
	return len([]rune(s))
}

// spaces returns the padding needed to bring s to the given width.
func spaces(s string, width int) string {
	return strings.Repeat(" ", max(width-runeLen(s), 0))
}
