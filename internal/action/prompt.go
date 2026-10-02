package action

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// isTerminal is the default StdinIsTTY: only a real *os.File can be a
// terminal, scripted readers (tests, pipes wrapped by callers) never are.
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && output.IsTerminal(f)
}

// confirmer asks the confirmation questions. All prompts share one
// bufio.Reader: a second reader on the same stream would swallow buffered
// input that belongs to the next question.
type confirmer struct {
	r   *bufio.Reader
	out io.Writer
	// sel is Options.Select: when set, "e" opens it from the question.
	sel func(*Plan) (bool, error)
}

func newConfirmer(in io.Reader, out io.Writer) *confirmer {
	if in == nil {
		in = strings.NewReader("")
	}
	return &confirmer{r: bufio.NewReader(in), out: out}
}

// Confirm asks one yes/no question on out and reads the answer from in. Only
// an explicit yes counts: an empty answer, no and a closed input all mean no,
// so it can never be read as consent by accident.
func Confirm(in io.Reader, out io.Writer, prompt string) bool {
	return newConfirmer(in, out).ask(prompt, "yn") == ansYes
}

// answer values returned by ask.
const (
	ansYes  = 'y'
	ansNo   = 'n'
	ansQuit = 'q'
	ansEdit = 'e'
)

// ask prints the prompt and reads answers until one is valid. Answers are
// trimmed and case-insensitive (so a Windows CRLF never matters), an empty
// answer means no, and end of input means quit so a closed stdin can never
// be read as consent. allowed lists the accepted answer letters.
func (c *confirmer) ask(prompt, allowed string) rune {
	for {
		fmt.Fprint(c.out, prompt)
		line, err := c.r.ReadString('\n')
		if err != nil && line == "" {
			// EOF or a read error: quit, and end the prompt line.
			fmt.Fprintln(c.out)
			return ansQuit
		}
		a := strings.ToLower(strings.TrimSpace(line))
		if a == "" {
			return ansNo
		}
		if r := matchAnswer(a, allowed); r != 0 {
			return r
		}
		fmt.Fprintf(c.out, "please answer with one of: %s\n", allowed)
	}
}

// matchAnswer accepts the letter or the full word ("y" or "yes").
func matchAnswer(a, allowed string) rune {
	words := map[rune]string{ansYes: "yes", ansNo: "no", ansQuit: "quit", ansEdit: "edit"}
	for _, r := range allowed {
		if a == string(r) || a == words[r] {
			return r
		}
	}
	return 0
}

// confirm asks one question for the whole plan, which was printed right
// above it, and sets Item.Confirmed on every item for a yes. Anything else
// (no, an empty answer, end of input) confirms nothing, so the default is
// always to change nothing.
func (c *confirmer) confirm(p *Plan) bool {
	what := fmt.Sprintf("%s (%s)", plural(p.itemCount(), "item"), output.FormatSize(p.ReclaimableBytes()))
	prompt, allowed := "Proceed with "+what+"? [y/N] ", "yn"
	if c.sel != nil {
		prompt, allowed = "Proceed with "+what+"? [y/N/e to choose] ", "yne"
	}
	switch c.ask(prompt, allowed) {
	case ansYes:
		p.setConfirmed(true)
		return true
	case ansEdit:
		return c.choose(p)
	}
	p.clearConfirmed()
	return false
}

// choose lets the user untick items (Options.Select) with all of them
// checked to begin with. Aborting, an error or unticking everything changes
// nothing.
func (c *confirmer) choose(p *Plan) bool {
	p.setConfirmed(true)
	ok, err := c.sel(p)
	if err != nil {
		fmt.Fprintf(c.out, "cannot show the list: %v\n", err)
	}
	if err != nil || !ok || p.confirmedCount() == 0 {
		p.clearConfirmed()
		return false
	}
	fmt.Fprintf(c.out, "cleaning %d of %s\n", p.confirmedCount(), plural(p.itemCount(), "item"))
	return true
}

// Header is how the plan names a group: detector and action label.
func (g Group) Header() string {
	return output.Sanitize(g.Detector) + " / " + g.Label()
}

// Line is how the plan lists an item: its description and size.
func (it Item) Line() string { return itemLine(it.Step) }

// confirmedCount is the number of confirmed items over all groups.
func (p *Plan) confirmedCount() int {
	n := 0
	for _, g := range p.Groups {
		for _, it := range g.Items {
			if it.Confirmed {
				n++
			}
		}
	}
	return n
}

// itemLine is a step's description followed by its size, the one place the
// size of an item is printed. Steps without a size (branches, git
// maintenance, whose finding size is 0) get no "(0 B)" suffix.
func itemLine(s Step) string {
	line := output.Sanitize(s.Description)
	if s.Finding.SizeBytes > 0 {
		line += " (" + output.FormatSize(s.Finding.SizeBytes) + ")"
	}
	return line
}

func (p *Plan) clearConfirmed() { p.setConfirmed(false) }

// itemCount is the number of planned items over all groups.
func (p *Plan) itemCount() int {
	n := 0
	for _, g := range p.Groups {
		n += len(g.Items)
	}
	return n
}

func (p *Plan) setConfirmed(v bool) {
	for gi := range p.Groups {
		for i := range p.Groups[gi].Items {
			p.Groups[gi].Items[i].Confirmed = v
		}
	}
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// renderPlan prints the plan grouped by detector and action, with sizes,
// the skipped list and the plan-time failures. A brief plan leaves out the
// items and their commands; the checklist ("e") and --dry-run list them.
func renderPlan(w io.Writer, p *Plan, brief bool) {
	for _, g := range p.Groups {
		fmt.Fprintf(w, "%s / %s: %s, %s\n", output.Sanitize(g.Detector), g.Label(), plural(len(g.Items), "item"), output.FormatSize(g.ReclaimableBytes()))
		if brief {
			continue
		}
		for _, it := range g.Items {
			fmt.Fprintf(w, "  %s\n", itemLine(it.Step))
			if it.Step.Command != "" {
				// Shell quoting keeps a command copy-pasteable but does not
				// neutralise control characters (an ESC in a file name is
				// still an ESC inside quotes), so the display escapes them.
				// Sanitize leaves backslashes alone, so Windows paths stay valid.
				fmt.Fprintf(w, "    $ %s\n", output.Sanitize(it.Step.Command))
			}
		}
	}
	if len(p.Groups) > 0 {
		fmt.Fprintf(w, "total reclaimable: %s\n", output.FormatSize(p.ReclaimableBytes()))
	}
	renderSkips(w, "skipped", p.Skipped)
	renderSkips(w, "failed to plan", p.Failed)
	if p.FlaggedOnly > 0 {
		fmt.Fprintf(w, "flagged, not actionable: %d (listed in the --dry-run report)\n", p.FlaggedOnly)
	}
}

func renderSkips(w io.Writer, title string, skips []Skip) {
	if len(skips) == 0 {
		return
	}
	fmt.Fprintf(w, "%s (%d):\n", title, len(skips))
	for _, s := range skips {
		fmt.Fprintf(w, "  %s: %s\n", describeFinding(s.Finding), output.Sanitize(s.Reason))
	}
}

// describeFinding names a finding by path and ref for skip and failure lines.
func describeFinding(f findings.Finding) string {
	if f.Ref != "" {
		return fmt.Sprintf("%s (%s)", output.Sanitize(f.Path), output.Sanitize(f.Ref))
	}
	return output.Sanitize(f.Path)
}

// renderSummary prints the end-of-run summary. applyPhaseSkips are the skips
// that happened after the plan was shown (declined, re-plan, interrupted);
// plan-time skips were already listed with the plan.
//
// With quiet the reclaimed size, the session line and the skip list are
// dropped; failures, recovery hints and the undo command stay, because they
// are the part a user must not miss.
func renderSummary(w io.Writer, r *Result, applyPhaseSkips []Skip, quiet bool) {
	fmt.Fprintf(w, "summary: %d applied, %d skipped, %d failed\n", r.Applied, r.Skipped, r.Failed)
	if !quiet {
		fmt.Fprintf(w, "reclaimed: %s\n", output.FormatSize(r.ReclaimedBytes))
		if r.SessionID != "" {
			fmt.Fprintf(w, "session: %s\n", output.Sanitize(r.SessionID))
		}
		renderSkips(w, "skipped during apply", applyPhaseSkips)
	}
	if len(r.Failures) > 0 {
		fmt.Fprintf(w, "failures (%d):\n", len(r.Failures))
		for _, f := range r.Failures {
			fmt.Fprintf(w, "  %s: %s\n", entryLabel(f.Path, f.Ref), output.Sanitize(f.Error))
		}
	}
	renderHints(w, r)
	if r.Restorable() {
		fmt.Fprintf(w, "undo: brooom undo %s\n", undoCommandTail(r))
	}
}

// undoCommandTail is the session id plus the scope flags of the original run,
// so pasting the printed command restores from any directory. The flags are
// quoted by the caller, the id is sanitized here.
func undoCommandTail(r *Result) string {
	parts := append([]string{output.Sanitize(r.SessionID)}, r.UndoFlags...)
	return strings.Join(parts, " ")
}

func renderHints(w io.Writer, r *Result) {
	var lines []string
	for _, e := range r.Entries {
		if e.RecoveryHint != "" {
			lines = append(lines, fmt.Sprintf("  %s: %s", entryLabel(e.Path, e.Ref), output.Sanitize(e.RecoveryHint)))
		}
	}
	if len(lines) > 0 {
		fmt.Fprintln(w, "recovery hints:")
		fmt.Fprintln(w, strings.Join(lines, "\n"))
	}
}

func entryLabel(path, ref string) string {
	if ref != "" {
		return fmt.Sprintf("%s (%s)", output.Sanitize(path), output.Sanitize(ref))
	}
	return output.Sanitize(path)
}
