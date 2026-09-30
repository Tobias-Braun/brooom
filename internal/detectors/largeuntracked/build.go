package largeuntracked

import (
	"fmt"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// metaUserDataRisk is the Meta key presets and the trash action read.
const (
	metaUserDataRisk = "user_data_risk"
	riskUntracked    = "untracked"
)

// untrackedReason warns in the finding itself, since the risk flags cannot.
const untrackedReason = "untracked file: git has no copy, this may be your only version; permanent deletion is refused"

// finding builds the finding of a candidate. open is the (reliable) open
// state and unknown tells that the open-file check could not answer for
// entries that are not open.
func (s *scan) finding(c candidate, open, unknown bool) findings.Finding {
	kind := findings.KindFile
	if c.dir {
		kind = findings.KindDir
	}
	mod := c.mod
	f := findings.Finding{
		ID:           findings.NewID(Name, kind, c.path, ""),
		Detector:     Name,
		Scope:        s.target.Scope,
		Path:         c.path,
		Kind:         kind,
		SizeBytes:    c.size,
		LastModified: &mod,
		AgeDays:      s.env.AgeDays(mod),
		Confidence:   findings.ConfidenceLow,
		Evidence:     s.evidence(c, unknown && !open),
		RiskFlags:    []findings.RiskFlag{},
	}
	if c.ignored {
		f.Confidence = findings.ConfidenceMedium
		f.RiskFlags = append(f.RiskFlags, findings.RiskGitignored)
	} else {
		f.Meta = map[string]string{metaUserDataRisk: riskUntracked}
	}
	if s.recent(mod) {
		f.RiskFlags = append(f.RiskFlags, findings.RiskRecentlyModified)
	}
	if open {
		f.RiskFlags = append(f.RiskFlags, findings.RiskFileOpen)
	}
	f.SuggestedAction = s.action(c, open)
	return f
}

// recent reports whether mod lies within thresholds.recent_days of the scan
// time. Times in the future count as recent.
func (s *scan) recent(mod time.Time) bool {
	days := s.cfg.Thresholds.RecentDays
	return days > 0 && mod.After(s.env.Now.Add(-time.Duration(days)*24*time.Hour))
}

func (s *scan) evidence(c candidate, unavailable bool) []findings.Evidence {
	ev := []findings.Evidence{{Code: "untracked_file", Message: "git does not track this file"}}
	if c.ignored {
		ev = []findings.Evidence{{Code: "ignored_by_git", Message: "matched by a gitignore rule"}}
	}
	ev = append(ev,
		findings.Evidence{Code: "size_over_threshold", Message: fmt.Sprintf("size is at least %d bytes", s.min), Value: c.size},
		findings.Evidence{Code: "last_modified_age", Message: "days since the last modification", Value: s.env.AgeDays(c.mod)},
	)
	if unavailable {
		ev = append(ev, findings.Evidence{Code: "open_check_unavailable", Message: "could not check whether a process has this open"})
	}
	return ev
}

// action suggests trash, or nothing while a process holds the path open.
func (s *scan) action(c candidate, open bool) findings.SuggestedAction {
	switch {
	case open:
		return findings.SuggestedAction{Type: findings.ActionNone, Reason: "a process has this open; close it before cleaning up"}
	case c.ignored:
		return findings.SuggestedAction{Type: findings.ActionTrash, Reason: "ignored by git and large: usually regenerable, moved to the trash so it can be restored"}
	default:
		return findings.SuggestedAction{Type: findings.ActionTrash, Reason: untrackedReason}
	}
}
