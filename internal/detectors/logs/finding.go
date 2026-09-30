package logs

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// finding builds the finding of an item. Evidence codes are stable; the
// entry description becomes the reason of a suggested action.
func (r *run) finding(it *item) findings.Finding {
	kind := findings.KindFile
	if it.cand.isDir {
		kind = findings.KindDir
	}
	mod := it.mod
	evidence := []findings.Evidence{
		{
			Code:    "matches_pattern",
			Message: fmt.Sprintf("matches %s pattern %s", r.toolName(it.cand.toolID), it.cand.pattern),
			Value:   it.cand.pattern,
		},
		ageEvidence(it.age),
	}
	return findings.Finding{
		ID:              findings.NewID(Name, kind, it.path, ""),
		Detector:        Name,
		Scope:           r.target.Scope,
		Path:            it.path,
		Kind:            kind,
		Tool:            it.cand.toolID,
		SizeBytes:       it.size,
		LastModified:    &mod,
		AgeDays:         it.age,
		Confidence:      r.confidence(it),
		Evidence:        append(evidence, it.evidence...),
		SuggestedAction: r.action(it),
		RiskFlags:       it.flags,
		Meta: map[string]string{
			"catalog_tool": it.cand.toolID,
			"category":     string(it.cand.category),
			"pattern":      it.cand.pattern,
		},
	}
}

// confidence is the entry's confidence, except for a recently modified log
// file: a rotated or numbered log that changed within thresholds.recent_days
// may still be the one a tool or a person is reading, so high drops to
// medium (low stays low). Only names ending in .log or .log.N count; a
// directory never does.
func (r *run) confidence(it *item) findings.Confidence {
	conf := findings.Confidence(it.cand.entry.Confidence)
	if conf == findings.ConfidenceHigh && !it.cand.isDir && it.age < r.cfg.Thresholds.RecentDays && isLogName(filepath.Base(it.path)) {
		return findings.ConfidenceMedium
	}
	return conf
}

// rotatedLog matches the numbered suffix of a rotated log, app.log.1.
var rotatedLog = regexp.MustCompile(`\.log\.\d+$`)

// isLogName reports whether a base name is a log file name, rotated or not,
// ignoring case since Windows and macOS filesystems fold it.
func isLogName(name string) bool {
	name = strings.ToLower(name)
	return strings.HasSuffix(name, ".log") || rotatedLog.MatchString(name)
}

// action derives the suggested action from the risk flags.
//
//   - No blocking flag: trash, with the catalog description as the reason.
//   - Blocking flags that --force may lift (tracked_files) and the run is
//     forced: trash, reason "forced: <flags>".
//   - Otherwise none, with the reason naming the blocking flags. An open file
//     is called out because --force cannot override it.
//
// The command stays empty: removal goes through the trash action, there is
// no equivalent shell command to print.
func (r *run) action(it *item) findings.SuggestedAction {
	blocking := blockingFlags(it.flags)
	switch {
	case len(blocking) == 0:
		return findings.SuggestedAction{Type: findings.ActionTrash, Reason: it.cand.entry.Description}
	case r.env.Force && findings.Actionable(it.flags, true):
		return findings.SuggestedAction{Type: findings.ActionTrash, Reason: "forced: " + strings.Join(blocking, ", ")}
	}
	reason := "not suggested, blocked by " + strings.Join(blocking, ", ")
	if slices.Contains(blocking, string(findings.RiskFileOpen)) {
		reason += " (a process has it open, --force cannot override this)"
	}
	return findings.SuggestedAction{Type: findings.ActionNone, Reason: reason}
}

// blockingFlags lists the blocking flags by name, in flag order.
func blockingFlags(flags []findings.RiskFlag) []string {
	var out []string
	for _, f := range flags {
		if f.Blocking() {
			out = append(out, string(f))
		}
	}
	return out
}
