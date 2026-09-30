// Package findings defines the findings schema: the stable contract between
// detectors, output formats, actions and any future client (dashboard, agent).
//
// Detectors produce Findings, formatters render them, actions consume them.
// The JSON representation of Finding and Report is a public, versioned
// interface (see SchemaVersion and docs/findings.md). Fields may be added in a
// backwards-compatible way; renaming or removing a field requires bumping
// SchemaVersion.
package findings

import (
	"slices"
	"time"
)

// SchemaVersion is the version of the JSON findings schema. Bump it only for
// breaking changes (removed/renamed fields, changed semantics).
const SchemaVersion = 1

// Finding is a single piece of reclaimable (or suspicious) clutter reported by
// a detector. A Finding never implies that anything will be modified; it only
// describes what was found, why, how risky acting on it is and which action
// would clean it up.
type Finding struct {
	// ID is a deterministic identifier derived from detector, kind, path and
	// ref (see NewID). The same clutter found in two runs gets the same ID,
	// which allows reviewing a findings file and applying a subset later.
	ID string `json:"id"`
	// Detector is the name of the detector that produced the finding, e.g.
	// "merged-branch" or "build-artifacts".
	Detector string `json:"detector"`
	// Scope is the repository, workspace root or user-level location the
	// finding belongs to.
	Scope Scope `json:"scope"`
	// Path is the absolute, symlink-resolved filesystem path the finding is
	// about. For branches it is the repository root; the branch name is in Ref.
	Path string `json:"path"`
	// Kind classifies what Path/Ref points at (file, dir, branch, ...).
	Kind Kind `json:"kind"`
	// Ref names a non-filesystem object inside Path: a branch name for
	// KindBranch, empty for plain files and directories.
	Ref string `json:"ref,omitempty"`
	// Tool names the tool that produced the clutter when known, e.g.
	// "claude-code", "cursor", "npm", "pytest".
	Tool string `json:"tool,omitempty"`
	// SizeBytes is the number of bytes that acting on the finding would free
	// (recursive size for directories, estimated for git maintenance).
	SizeBytes int64 `json:"size_bytes"`
	// LastModified is the most recent modification time relevant to the
	// finding: newest file mtime for paths, last commit time for branches.
	LastModified *time.Time `json:"last_modified,omitempty"`
	// AgeDays is the number of whole days between LastModified and the scan.
	AgeDays int `json:"age_days"`
	// Confidence expresses how sure the detector is that the finding is
	// clutter that can be removed.
	Confidence Confidence `json:"confidence"`
	// Evidence lists the structured, human-readable reasons for the finding.
	Evidence []Evidence `json:"evidence"`
	// SuggestedAction is the action that would clean the finding up.
	// ActionNone means "flagged, not suggested" (e.g. a dirty worktree).
	SuggestedAction SuggestedAction `json:"suggested_action"`
	// RiskFlags lists reasons to be careful. Blocking flags (see
	// RiskFlag.Blocking) force SuggestedAction to ActionNone unless the user
	// explicitly overrides with --force.
	RiskFlags []RiskFlag `json:"risk_flags"`
	// Meta carries detector-specific extra data that is not part of the
	// common schema (e.g. "upstream": "origin/feat/x"). Keys are snake_case.
	Meta map[string]string `json:"meta,omitempty"`
}

// ScopeType distinguishes where a finding was found.
type ScopeType string

const (
	// ScopeRepo is a single git repository (the default, detected from cwd).
	ScopeRepo ScopeType = "repo"
	// ScopeRoot is a folder given as the path argument, below which
	// repositories and project folders were discovered. The value "root" is
	// kept from the workspace roots of earlier releases for schema stability.
	ScopeRoot ScopeType = "root"
	// ScopeUser is a well-known user-level location from the embedded tool
	// catalog (e.g. ~/.cache/<tool>); only scanned when explicitly enabled.
	ScopeUser ScopeType = "user"
)

// Scope identifies the repository, root or user-level location a finding
// belongs to.
type Scope struct {
	Type ScopeType `json:"type"`
	// Path is the absolute, symlink-resolved path of the repo/root/location.
	Path string `json:"path"`
}

// Kind classifies the object a finding points at.
type Kind string

const (
	KindFile            Kind = "file"
	KindDir             Kind = "dir"
	KindBranch          Kind = "branch"
	KindWorktree        Kind = "worktree"
	KindWorktreeMissing Kind = "worktree-missing"
	KindGitObjects      Kind = "git-loose-objects"
	KindGitPacks        Kind = "git-packs"
	KindGitReflog       Kind = "git-reflog"
	KindGitLargeBlob    Kind = "git-large-blob"
)

// Confidence expresses how sure a detector is that a finding is removable.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Rank orders confidences so callers can filter with a minimum confidence.
func (c Confidence) Rank() int {
	switch c {
	case ConfidenceHigh:
		return 3
	case ConfidenceMedium:
		return 2
	case ConfidenceLow:
		return 1
	default:
		return 0
	}
}

// Evidence is one structured, human-readable reason behind a finding.
type Evidence struct {
	// Code is a stable, machine-readable snake_case identifier of the reason,
	// e.g. "last_commit_age", "merged_into", "matches_pattern".
	Code string `json:"code"`
	// Message is a short human-readable sentence, e.g.
	// "last commit 94 days ago".
	Message string `json:"message"`
	// Value is the raw value behind the reason (number, string, bool) for
	// machine consumers. Optional.
	Value any `json:"value,omitempty"`
}

// ActionType names an action that can consume a finding.
type ActionType string

const (
	// ActionNone means the finding is reported but no action is suggested.
	ActionNone ActionType = "none"
	// ActionTrash removes a file or directory via the configured trash
	// strategy (OS trash, quarantine or delete).
	ActionTrash ActionType = "trash"
	// ActionDeleteBranch deletes a local branch (git branch -d, -D with --force).
	ActionDeleteBranch ActionType = "delete-branch"
	// ActionRemoveWorktree trashes a worktree directory and then
	// deregisters it from git (the finding carries no shell Command: a bare
	// git worktree remove would permanently delete ignored files).
	ActionRemoveWorktree ActionType = "remove-worktree"
	// ActionPruneWorktrees prunes worktree metadata whose directory is missing.
	ActionPruneWorktrees ActionType = "prune-worktrees"
	// ActionGitGC runs git gc.
	ActionGitGC ActionType = "git-gc"
	// ActionGitPrune runs git prune with an expiry.
	ActionGitPrune ActionType = "git-prune"
	// ActionGitReflogExpire runs git reflog expire with an expiry.
	ActionGitReflogExpire ActionType = "git-reflog-expire"
)

// SuggestedAction describes how a finding would be cleaned up.
type SuggestedAction struct {
	Type ActionType `json:"type"`
	// Args are action-specific parameters, e.g. {"expire": "90.days.ago"}.
	Args map[string]string `json:"args,omitempty"`
	// Command is the equivalent shell command for humans, e.g.
	// "git branch -d feat/x". Informational only; never executed via a shell.
	Command string `json:"command,omitempty"`
	// Reason explains why this action (or ActionNone) was chosen.
	Reason string `json:"reason,omitempty"`
}

// Actionable reports whether the finding has a suggested action other than
// ActionNone.
func (f Finding) Actionable() bool {
	return f.SuggestedAction.Type != "" && f.SuggestedAction.Type != ActionNone
}

// HasRisk reports whether the finding carries the given risk flag.
func (f Finding) HasRisk(r RiskFlag) bool {
	return slices.Contains(f.RiskFlags, r)
}

// Blocked reports whether any of the finding's risk flags is blocking.
func (f Finding) Blocked() bool {
	return slices.ContainsFunc(f.RiskFlags, RiskFlag.Blocking)
}
