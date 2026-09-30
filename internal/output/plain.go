package output

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func init() { Register(plainFormatter{}) }

// plainFormatter prints paths only, one per line, for xargs-style use. Its
// output is piped into other tools, so it lists only what is safe and
// unambiguous to hand to them (see plainListable). Options are ignored.
type plainFormatter struct{}

func (plainFormatter) Name() string { return "plain" }

// plainListable decides whether a finding may appear in plain output. Only
// actionable findings of filesystem kinds and branches qualify: flagged
// findings must never end up in an rm pipeline, and the git maintenance kinds
// point at a repository root (their path is the whole repository) while
// worktree-missing points at a path that no longer exists.
func plainListable(f findings.Finding) bool {
	if !f.Actionable() {
		return false
	}
	switch f.Kind {
	case findings.KindFile, findings.KindDir, findings.KindWorktree, findings.KindBranch:
		return true
	default:
		return false
	}
}

// plainLine is the text of one finding: the path, or "<repo>\t<branch>" for
// branches.
func plainLine(f findings.Finding) string {
	if f.Kind == findings.KindBranch {
		return f.Path + "\t" + f.Ref
	}
	return f.Path
}

// hasLineBreak reports whether text would split a record in line-oriented
// consumers.
func hasLineBreak(s string) bool { return strings.ContainsAny(s, "\r\n") }

// Write lists the listable findings in report order. Findings whose path or
// ref contains a line break are not written, because they would be read as
// several records; after the rest is written an error tells how many were left
// out so the omission is never silent. Worktree paths are listed for
// inspection only: removing them with rm leaves git metadata behind (see
// docs/findings.md).
func (plainFormatter) Write(w io.Writer, r *findings.Report, _ Options) error {
	var buf bytes.Buffer
	skipped := 0
	for _, f := range r.Findings {
		if !plainListable(f) {
			continue
		}
		if hasLineBreak(f.Path) || hasLineBreak(f.Ref) {
			skipped++
			continue
		}
		buf.WriteString(plainLine(f) + "\n")
	}
	if buf.Len() > 0 {
		if _, err := w.Write(buf.Bytes()); err != nil {
			return err
		}
	}
	if skipped > 0 {
		noun := "findings"
		if skipped == 1 {
			noun = "finding"
		}
		return fmt.Errorf("plain: %d %s skipped because their path contains a line break (use --format json)", skipped, noun)
	}
	return nil
}
