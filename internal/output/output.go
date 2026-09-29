// Package output renders findings reports in the supported formats.
//
// Formats: table (default, grouped by detector, humanized sizes, totals per
// group and overall), tree (findings in their directory structure), json
// (the full Report, stable for scripting), ndjson (one finding per line),
// plain (paths only, one per line, for xargs) and summary (counts and
// reclaimable bytes per detector).
//
// Each format lives in its own file and registers itself with Register.
// Formatters must respect Options.Color (the CLI resolves NO_COLOR, --no-color
// and TTY detection into it) and never write ANSI escapes when it is false.
package output

import (
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// Options controls rendering.
type Options struct {
	// Color enables ANSI colors.
	Color bool
	// Width is the terminal width (0 = unknown, do not wrap/truncate).
	Width int
	// Quiet suppresses headers, hints and totals where the format has them.
	Quiet bool
}

// Formatter renders a complete report.
type Formatter interface {
	// Name is the --format value, e.g. "table".
	Name() string
	// Write renders the report to w.
	Write(w io.Writer, r *findings.Report, opts Options) error
}

var (
	mu         sync.RWMutex
	formatters = map[string]Formatter{}
)

// Register adds a formatter; duplicates panic.
func Register(f Formatter) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := formatters[f.Name()]; dup {
		panic("output: duplicate formatter " + f.Name())
	}
	formatters[f.Name()] = f
}

// Get returns the formatter for a --format value.
func Get(name string) (Formatter, error) {
	mu.RLock()
	defer mu.RUnlock()
	f, ok := formatters[name]
	if !ok {
		return nil, fmt.Errorf("unknown format %q (available: %v)", name, namesLocked())
	}
	return f, nil
}

// Names returns all registered format names, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	return namesLocked()
}

func namesLocked() []string {
	out := make([]string, 0, len(formatters))
	for n := range formatters {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
