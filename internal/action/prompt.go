package action

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
)

// isTerminal is the default StdinIsTTY: only a real *os.File can be a
// terminal, scripted readers (tests, pipes wrapped by callers) never are.
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// confirmer asks the confirmation questions. All prompts share one
// bufio.Reader: a second reader on the same stream would swallow buffered
// input that belongs to the next question.
type confirmer struct {
	r   *bufio.Reader
	out io.Writer
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
	ansYes   = 'y'
	ansNo    = 'n'
	ansIndiv = 'i'
	ansQuit  = 'q'
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
	words := map[rune]string{ansYes: "yes", ansNo: "no", ansIndiv: "individually", ansQuit: "quit"}
	for _, r := range allowed {
		if a == string(r) || a == words[r] {
			return r
		}
	}
	return 0
}

// confirm walks the plan and sets Item.Confirmed. It returns false when the
// user quit (or input ended); in that case nothing is confirmed at all, so a
// quit halfway through changes nothing.
func (c *confirmer) confirm(p *Plan) bool {
	for gi := range p.Groups {
		g := &p.Groups[gi]
		prompt := fmt.Sprintf("%s / %s: apply %s (%s)? [y]es/[n]o/[i]ndividually/[q]uit ",
			output.Sanitize(g.Detector), g.Action, plural(len(g.Items), "item"), output.FormatSize(g.ReclaimableBytes()))
		switch c.ask(prompt, "yniq") {
		case ansYes:
			for i := range g.Items {
				g.Items[i].Confirmed = true
			}
		case ansIndiv:
			if !c.confirmItems(g) {
				p.clearConfirmed()
				return false
			}
		case ansQuit:
			p.clearConfirmed()
			return false
		}
	}
	return true
}

// confirmItems asks per item; false means quit.
func (c *confirmer) confirmItems(g *Group) bool {
	for i := range g.Items {
		it := &g.Items[i]
		prompt := fmt.Sprintf("  %s (%s)? [y]es/[n]o/[q]uit ", output.Sanitize(it.Step.Description), output.FormatSize(it.Step.Finding.SizeBytes))
		switch c.ask(prompt, "ynq") {
		case ansYes:
			it.Confirmed = true
		case ansQuit:
			return false
		}
	}
	return true
}

func (p *Plan) clearConfirmed() { p.setConfirmed(false) }

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
// the skipped list and the plan-time failures.
func renderPlan(w io.Writer, p *Plan) {
	for _, g := range p.Groups {
		fmt.Fprintf(w, "%s / %s: %s, %s\n", output.Sanitize(g.Detector), g.Action, plural(len(g.Items), "item"), output.FormatSize(g.ReclaimableBytes()))
		for _, it := range g.Items {
			fmt.Fprintf(w, "  %s (%s)\n", output.Sanitize(it.Step.Description), output.FormatSize(it.Step.Finding.SizeBytes))
			if it.Step.Command != "" {
				// Step.Command is deliberately not sanitized here: it is a
				// copy-pasteable shell command whose quoting is handled in #129.
				fmt.Fprintf(w, "    $ %s\n", it.Step.Command)
			}
		}
	}
	if len(p.Groups) > 0 {
		fmt.Fprintf(w, "total reclaimable: %s\n", output.FormatSize(p.ReclaimableBytes()))
	}
	renderSkips(w, "skipped", p.Skipped)
	renderSkips(w, "failed to plan", p.Failed)
	if p.FlaggedOnly > 0 {
		fmt.Fprintf(w, "flagged, not actionable: %d (see brooom scan)\n", p.FlaggedOnly)
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
		fmt.Fprintf(w, "undo: brooom undo %s\n", output.Sanitize(r.SessionID))
	}
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
