// Package progressui is the live terminal display of a running command: a
// bubbletea program on stderr showing the phase, a spinner, a progress bar,
// counts per detector and per target and the bytes reclaimed so far.
//
// It lives in the CLI layer on purpose. The core packages only see the
// terminal-free progress.Reporter interface; every terminal library is
// confined to this package, and the display never touches stdout, so the
// results of a command (table, tree, json, ...) stay exactly what they were.
package progressui

import (
	"maps"
	"sort"

	"github.com/Tobias-Braun/brooom/internal/progress"
)

// Mode says what the display shows.
type Mode int

const (
	// ModeLive draws the full multi-line display.
	ModeLive Mode = iota
	// ModeHidden draws nothing; used while stdout text or a prompt owns the
	// terminal.
	ModeHidden
	// ModeFailed collapses to the one-line summary of an interrupted or
	// failed run.
	ModeFailed
)

// State is everything the display draws. It is a plain value: the reporter
// mutates one under its lock and the bubbletea model renders copies, so the
// hot reporting path never waits for the terminal and the golden tests can
// render any state without a program.
type State struct {
	Mode    Mode
	Phase   progress.Phase
	Done    int
	Total   int
	Current string
	// Findings is the number of unique findings so far; Detectors and
	// Targets break it down by detector name and target path.
	Findings  int
	Detectors map[string]int
	Targets   map[string]int
	// Bytes is what applied steps reclaimed so far.
	Bytes int64
	// Visited lists the phases that have started, in order, for the summary.
	Visited []progress.Phase
}

// StartPhase begins a phase and restarts its counters. The findings and the
// reclaimed bytes are kept: the plan and apply phases keep showing what the
// scan found.
func (s *State) StartPhase(p progress.Phase, total int) {
	s.Mode = ModeLive
	s.Phase, s.Total, s.Done, s.Current = p, total, 0, ""
	if n := len(s.Visited); n == 0 || s.Visited[n-1] != p {
		s.Visited = append(s.Visited, p)
	}
}

// AddStep counts one finished unit of work.
func (s *State) AddStep(label string) {
	s.Done++
	s.Current = label
}

// AddFinding counts one finding for its detector and target.
func (s *State) AddFinding(detector, target string) {
	s.Findings++
	if s.Detectors == nil {
		s.Detectors = map[string]int{}
	}
	if s.Targets == nil {
		s.Targets = map[string]int{}
	}
	s.Detectors[detector]++
	s.Targets[target]++
}

// Clone returns an independent copy, safe to render while the original keeps
// changing.
func (s State) Clone() State {
	s.Detectors = maps.Clone(s.Detectors)
	s.Targets = maps.Clone(s.Targets)
	s.Visited = append([]progress.Phase(nil), s.Visited...)
	return s
}

// counted is one name with its count, for ordered display.
type counted struct {
	name  string
	count int
}

// byCount orders entries by descending count, ties by name, so the display is
// stable between frames and in golden tests.
func byCount(m map[string]int) []counted {
	out := make([]counted, 0, len(m))
	for k, v := range m {
		out = append(out, counted{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].count != out[j].count {
			return out[i].count > out[j].count
		}
		return out[i].name < out[j].name
	})
	return out
}
