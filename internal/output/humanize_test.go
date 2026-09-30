package output

import (
	"math"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func TestFormatSize(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{-5, "0 B"}, {0, "0 B"}, {1, "1 B"}, {999, "999 B"},
		{1000, "1.0 kB"}, {1234, "1.2 kB"}, {999_949, "999.9 kB"},
		{999_950, "1.0 MB"}, {1_234_567, "1.2 MB"}, {1_234_567_890, "1.2 GB"},
		{999_950_000, "1.0 GB"}, {5_000_000_000_000, "5.0 TB"},
		{1_500_000_000_000_000, "1.5 PB"}, {math.MaxInt64, "9223.4 PB"},
	}
	for _, tt := range tests {
		if got := FormatSize(tt.in); got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		in   int
		want string
	}{
		{-3, "0d"}, {0, "0d"}, {3, "3d"}, {29, "29d"}, {30, "1mo"},
		{59, "1mo"}, {60, "2mo"}, {364, "12mo"}, {365, "1y"}, {800, "2y"},
	}
	for _, tt := range tests {
		if got := FormatAge(tt.in); got != tt.want {
			t.Errorf("FormatAge(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestTruncateMiddle(t *testing.T) {
	tests := []struct {
		in    string
		width int
		want  string
	}{
		{"", 5, ""}, {"abc", 5, "abc"}, {"abcde", 5, "abcde"}, {"abcdef", 5, "ab…ef"},
		{"abcdefg", 6, "ab…efg"}, {"abcdef", 1, "…"}, {"abcdef", 0, "abcdef"},
		{"äöüßäöü", 4, "ä…öü"},
	}
	for _, tt := range tests {
		if got := truncateMiddle(tt.in, tt.width); got != tt.want {
			t.Errorf("truncateMiddle(%q, %d) = %q, want %q", tt.in, tt.width, got, tt.want)
		}
	}
}

func TestRelPath(t *testing.T) {
	tests := []struct{ scope, path, want string }{
		{"/work/shop", "/work/shop", "."},
		{"/work/shop", "/work/shop/dist", "dist"},
		{"/work/shop", "/work/other", "/work/other"},
		{"/work/shop", "/work/shop-wt", "/work/shop-wt"},
		{"", "/work/x", "/work/x"},
	}
	for _, tt := range tests {
		got := strings.ReplaceAll(relPath(tt.scope, tt.path), "\\", "/")
		if got != tt.want {
			t.Errorf("relPath(%q, %q) = %q, want %q", tt.scope, tt.path, got, tt.want)
		}
	}
}

func TestShortRiskFlagsCoverage(t *testing.T) {
	seen := map[string]findings.RiskFlag{}
	for _, f := range findings.AllRiskFlags() {
		label, ok := riskLabels[f]
		if !ok {
			t.Errorf("risk flag %q has no short label", f)
			continue
		}
		if label != strings.ToLower(label) || len(label) == 0 || len(label) > 10 {
			t.Errorf("label %q of %q must be lowercase and 1..10 chars", label, f)
		}
		if other, dup := seen[label]; dup {
			t.Errorf("label %q used by %q and %q", label, other, f)
		}
		seen[label] = f
	}
}

func TestShortRiskFlags(t *testing.T) {
	got := ShortRiskFlags([]findings.RiskFlag{findings.RiskWorktreeDirty, "future_flag", findings.RiskHasOpenPR})
	if got != "dirty,future_flag,open-pr" {
		t.Errorf("got %q", got)
	}
	if ShortRiskFlags(nil) != "" {
		t.Error("nil flags must give empty string")
	}
}

func TestPainter(t *testing.T) {
	if got := newPainter(false).red("x"); got != "x" {
		t.Errorf("off painter wrapped: %q", got)
	}
	if got := newPainter(true).red(""); got != "" {
		t.Errorf("empty string wrapped: %q", got)
	}
	if got := newPainter(true).bold("x"); got != "\x1b[1mx\x1b[0m" {
		t.Errorf("got %q", got)
	}
}
