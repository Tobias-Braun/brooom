package output

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// hostileReport carries ESC and newline in a branch name, a file name, a
// reason, a detector name and a scan error, the places where untrusted text
// reaches the human formatters.
func hostileReport() *findings.Report {
	scope := findings.Scope{Type: findings.ScopeRepo, Path: "/work/shop"}
	fs := []findings.Finding{
		{Detector: "merged-branch", Scope: scope, Path: "/work/shop", Kind: findings.KindBranch,
			Ref: "feat/\x1b[2Jevil\nFORGED LINE", Confidence: findings.ConfidenceHigh,
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionNone, Reason: "why\x1b[31m\nfake"},
			RiskFlags:       []findings.RiskFlag{findings.RiskUnpushedCommits}},
		{Detector: "build-artifacts", Scope: scope, Path: "/work/shop/dir\x1b]0;pwn\x07/na\nme.log", Kind: findings.KindFile,
			SizeBytes: 10, Confidence: findings.ConfidenceHigh, SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash}},
		{Detector: "odd\ndetector", Scope: scope, Path: "/work/shop/x", Kind: findings.KindFile,
			Confidence: findings.ConfidenceHigh, SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash}},
	}
	errs := []findings.ScanError{{Detector: "git\x1bbloat", Path: "/work/sh\nop", Message: "bad\x1b[0m\nmsg"}}
	return findings.NewReport("test", fixtureNow, []findings.Scope{scope}, fs, errs)
}

// TestHumanFormattersSanitize checks that no human formatter emits a raw
// control character (other than its own newlines and, with color, its own
// ANSI styling) for hostile names, and that a newline in a name cannot forge
// a line starting with the injected text.
func TestHumanFormattersSanitize(t *testing.T) {
	for _, format := range []string{"table", "tree", "summary"} {
		for _, quiet := range []bool{false, true} {
			t.Run(format, func(t *testing.T) {
				opts := Options{Quiet: quiet}
				plain := checkInvariants(t, format, hostileReport(), opts)
				for _, r := range strings.ReplaceAll(plain, "\n", "") {
					if r < 0x20 || r == 0x7f {
						t.Fatalf("control rune %q in output:\n%q", r, plain)
					}
				}
				for _, line := range strings.Split(plain, "\n") {
					if strings.HasPrefix(line, "FORGED LINE") || strings.HasPrefix(line, "fake") || strings.HasPrefix(line, "me.log") || strings.HasPrefix(line, "msg") {
						t.Errorf("forged line %q in output:\n%s", line, plain)
					}
				}
			})
		}
	}
}

// TestColoredOutputHasOnlyOwnEscapes strips the formatter's own SGR sequences
// and expects no escape byte to remain.
func TestColoredOutputHasOnlyOwnEscapes(t *testing.T) {
	for _, format := range []string{"table", "tree", "summary"} {
		out := render(t, format, hostileReport(), Options{Color: true})
		if strings.Contains(stripANSI(out), "\x1b") {
			t.Errorf("%s: injected escape survived color rendering:\n%q", format, out)
		}
	}
}

// The table prints the path relative to the scope with the platform's
// separator, so the expected file name joins its parts with filepath.Separator.
func TestTableShowsEscapedBranchAndFile(t *testing.T) {
	out := render(t, "table", hostileReport(), Options{})
	for _, want := range []string{`feat/\x1b[2Jevil\nFORGED LINE`, `dir\x1b]0;pwn\x07` + string(filepath.Separator) + `na\nme.log`, `why\x1b[31m\nfake`} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
}
