package procs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// lsofMaxPaths bounds how many file paths go into one lsof invocation.
	lsofMaxPaths = 100
	// lsofMaxArgBytes bounds the total size of the path arguments, well
	// below the kernel's ARG_MAX so long paths cannot make exec fail.
	lsofMaxArgBytes = 64 * 1024
)

// lsofRunner runs lsof with the given arguments and returns its stdout. It is
// a parameter so the parsing and mapping logic is testable without lsof. An
// unusable lsof (not installed) must be reported as ErrUnavailable.
type lsofRunner func(ctx context.Context, args []string) ([]byte, error)

// parseLsof extracts the file names from `lsof -F0n` output. Fields are
// NUL-terminated and a record ends with a newline, so a name containing
// spaces survives; only the single newline that terminates the previous
// record is stripped from the front of a token. Names are returned as lsof
// printed them: lsof escapes non-printable characters even with -F0 (see
// lsofSpellings), so callers compare against the escaped spellings of their
// paths instead of decoding, which would be ambiguous for a name that really
// contains a backslash followed by an n.
func parseLsof(out []byte) []string {
	var names []string
	for _, tok := range bytes.Split(out, []byte{0}) {
		tok = bytes.TrimPrefix(tok, []byte{'\n'})
		if len(tok) > 1 && tok[0] == 'n' {
			names = append(names, string(tok[1:]))
		}
	}
	return names
}

// lsofOpenFiles implements openFiles on top of a runner: file batches first,
// then one +D invocation per directory with an equal slice of the remaining
// budget. A timed out directory makes the result incomplete but does not stop
// the remaining ones.
func lsofOpenFiles(ctx context.Context, run lsofRunner, files, dirs []string, res map[string]bool) error {
	incomplete := false
	for _, batch := range batchPaths(files, lsofMaxPaths, lsofMaxArgBytes) {
		args := append([]string{"-n", "-P", "-w", "-F0n", "--"}, batch...)
		names, err := runNames(ctx, run, args)
		// Names parsed before a timeout are true positives, so they are
		// applied before the error is handled.
		markFileNames(names, batch, res)
		if err != nil {
			return finishLsof(ctx, err, incomplete)
		}
	}
	for i, dir := range dirs {
		sliceCtx, cancel := sliceBudget(ctx, len(dirs)-i)
		names, err := runNames(sliceCtx, run, []string{"-n", "-P", "-w", "-F0n", "+D", dir})
		cancel()
		// A directory scan cut short may still have seen an open file.
		if anyBelow(names, dirPrefix(dir)) {
			res[dir] = true
		}
		if err != nil {
			if errors.Is(err, ErrUnavailable) {
				return err
			}
			incomplete = true
			if ctx.Err() != nil {
				break
			}
		}
	}
	return finishLsof(ctx, nil, incomplete)
}

// finishLsof maps the final state to the package's error contract.
func finishLsof(ctx context.Context, err error, incomplete bool) error {
	switch {
	case err != nil && errors.Is(err, ErrUnavailable):
		return err
	case err != nil, incomplete, ctx.Err() != nil:
		return fmt.Errorf("%w: lsof did not finish in time", ErrIncomplete)
	}
	return nil
}

// runNames runs lsof and interprets its exit status: 1 with empty output is
// "nothing open", any other failure without output means lsof is unusable,
// and a killed process is a timeout.
func runNames(ctx context.Context, run lsofRunner, args []string) ([]string, error) {
	out, err := run(ctx, args)
	if ctx.Err() != nil {
		return parseLsof(out), fmt.Errorf("%w: %w", ErrIncomplete, ctx.Err())
	}
	if err == nil {
		return parseLsof(out), nil
	}
	if errors.Is(err, ErrUnavailable) {
		return nil, err
	}
	var ee *exec.ExitError
	if len(bytes.TrimSpace(out)) == 0 {
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: lsof: %w", ErrUnavailable, err)
	}
	// lsof exits non-zero when some arguments matched nothing or some
	// processes were unreadable, yet the output it did produce is valid.
	return parseLsof(out), nil
}

// sliceBudget gives one of n remaining items an equal share of the time that
// is left; without a deadline the context is used as is.
func sliceBudget(ctx context.Context, n int) (context.Context, context.CancelFunc) {
	dl, ok := ctx.Deadline()
	if !ok || n < 1 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Until(dl)/time.Duration(n))
}

// markFileNames flags each batch entry that lsof reported, first by exact
// match and then case-insensitively (APFS is usually case-insensitive and
// lsof may report the on-disk spelling). Each entry is matched under every
// spelling lsof might have printed it in, see lsofSpellings.
func markFileNames(names, batch []string, res map[string]bool) {
	exact := make(map[string]struct{}, len(names))
	for _, n := range names {
		exact[n] = struct{}{}
	}
	for _, f := range batch {
		if fileReported(f, names, exact) {
			res[f] = true
		}
	}
}

// fileReported tells whether any spelling of f appears among names.
func fileReported(f string, names []string, exact map[string]struct{}) bool {
	spellings := lsofSpellings(f)
	for _, sp := range spellings {
		if _, ok := exact[sp]; ok {
			return true
		}
	}
	for _, n := range names {
		for _, sp := range spellings {
			if strings.EqualFold(n, sp) {
				return true
			}
		}
	}
	return false
}

// anyBelow reports whether any name lies below the prefix, ignoring case for
// the same reason as markFileNames. The prefix is matched under every spelling
// of it. This also counts working directories and memory maps of
// subdirectories, which is conservative: it can only flag more, never less.
func anyBelow(names []string, prefix string) bool {
	for _, sp := range lsofSpellings(prefix) {
		for _, n := range names {
			if len(n) > len(sp) && strings.EqualFold(n[:len(sp)], sp) {
				return true
			}
		}
	}
	return false
}

// lsofSpellings returns the spellings lsof may print for a path. lsof
// escapes non-printable characters in names even with -F0: \b \f \n \r \t
// for those five, ^X for other control characters and ^? for DEL. Bytes above
// 0x7f are either passed through or written as \xHH depending on the locale,
// so both forms are offered. Paths without such characters have one spelling.
func lsofSpellings(p string) []string {
	out := []string{p}
	for _, hex := range []bool{false, true} {
		if e := lsofEscape(p, hex); e != p {
			out = append(out, e)
		}
	}
	return out
}

// lsofNamedEscapes are the control characters lsof writes as a backslash
// letter.
var lsofNamedEscapes = map[byte]string{
	'\b': `\b`, '\f': `\f`, '\n': `\n`, '\r': `\r`, '\t': `\t`,
}

// lsofEscape applies lsof's escaping to control characters and, if hex is
// set, to bytes above 0x7f.
func lsofEscape(p string, hex bool) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		if named, ok := lsofNamedEscapes[c]; ok {
			b.WriteString(named)
			continue
		}
		switch {
		case c < 0x20:
			b.WriteByte('^')
			b.WriteByte(c ^ 0x40)
		case c == 0x7f:
			b.WriteString("^?")
		case c >= 0x80 && hex:
			fmt.Fprintf(&b, `\x%02x`, c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// batchPaths splits paths into batches of at most maxN entries whose summed
// length stays within maxBytes. A single oversized path gets its own batch.
func batchPaths(paths []string, maxN, maxBytes int) [][]string {
	var batches [][]string
	var cur []string
	size := 0
	for _, p := range paths {
		if len(cur) > 0 && (len(cur) >= maxN || size+len(p)+1 > maxBytes) {
			batches = append(batches, cur)
			cur, size = nil, 0
		}
		cur = append(cur, p)
		size += len(p) + 1
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches
}
