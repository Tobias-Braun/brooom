// Package session records every applied cleanup in a manifest so it can be
// reviewed (`brooom sessions`) and undone (`brooom undo`).
//
// A session is created for each command run with --apply. Its manifest is
// written to ~/.brooom/sessions/<id>.json before the first action runs and
// rewritten after every entry, so a crash mid-session still leaves an
// accurate record of what was already done.
package session

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// ManifestVersion is the version of the manifest file format.
const ManifestVersion = 1

// Status of a single manifest entry.
type Status string

const (
	StatusApplied  Status = "applied"
	StatusFailed   Status = "failed"
	StatusSkipped  Status = "skipped"
	StatusRestored Status = "restored"
)

// Manifest is the record of one applied session.
type Manifest struct {
	Version   int       `json:"version"`
	ID        string    `json:"id"`
	StartedAt time.Time `json:"started_at"`
	// FinishedAt is zero while the session is running or if it crashed.
	FinishedAt time.Time `json:"finished_at,omitzero"`
	// Command is the command line that created the session (for display).
	Command string  `json:"command"`
	Entries []Entry `json:"entries"`
	// ReclaimedBytes sums SizeBytes of applied entries.
	ReclaimedBytes int64 `json:"reclaimed_bytes"`
}

// Entry is one action applied to one finding.
type Entry struct {
	FindingID string              `json:"finding_id"`
	Detector  string              `json:"detector"`
	Action    findings.ActionType `json:"action"`
	Path      string              `json:"path"`
	Ref       string              `json:"ref,omitempty"`
	SizeBytes int64               `json:"size_bytes"`
	Status    Status              `json:"status"`
	Error     string              `json:"error,omitempty"`
	At        time.Time           `json:"at"`
	// Trash is set for ActionTrash entries.
	Trash *trash.Record `json:"trash,omitempty"`
	// Undo holds what is needed to reverse non-file actions, e.g.
	// {"branch": "feat/x", "sha": "<tip>"} for a deleted branch or
	// {"worktree": "<path>", "branch": "feat/x", "head": "<sha>"} for a
	// removed worktree. Git maintenance actions are not reversible.
	Undo map[string]string `json:"undo,omitempty"`
	// Restorable is true when `brooom undo` can reverse this entry.
	Restorable bool `json:"restorable"`
	// RecoveryHint is a human-readable way to recover manually, e.g.
	// "git branch feat/x 1a2b3c4" (the commits exist until git
	// garbage-collects unreachable objects).
	RecoveryHint string `json:"recovery_hint,omitempty"`
}

// ErrNotFound is returned when no session matches an id or prefix, or when
// there are no sessions at all.
var ErrNotFound = errors.New("session not found")

// ErrAmbiguous is returned (wrapped, with the candidate ids) when an id
// prefix matches several sessions.
var ErrAmbiguous = errors.New("ambiguous session id")

// Problem describes a manifest file that could not be used. Listing reports
// problems instead of failing because manifests are the only undo record and
// one damaged file must never hide the rest of the history.
type Problem struct {
	File string
	Err  error
}

// MarshalJSON renders Err as a string, since error values do not marshal.
func (p Problem) MarshalJSON() ([]byte, error) {
	msg := ""
	if p.Err != nil {
		msg = p.Err.Error()
	}
	return json.Marshal(struct {
		File  string `json:"file"`
		Error string `json:"error"`
	}{p.File, msg})
}

// NewID returns a new, sortable session ID: UTC timestamp plus a random
// suffix, e.g. "20260929-224501-3f9a".
func NewID(now time.Time) string {
	return now.UTC().Format("20060102-150405") + "-" + randomSuffix()
}
