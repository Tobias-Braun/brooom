package buildartifacts

import (
	"context"
	"fmt"
	"path"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// facts is everything known about one candidate when its finding is built.
type facts struct {
	c    candidate
	path string
	sum  walk.DirSummary
	act  activity
	git  gitState
}

// projectState summarizes the inactivity check of the surrounding project.
type projectState struct {
	// known is false when neither a commit nor a source file gave a signal.
	known bool
	// days since the last activity (meaningful when known).
	days int
	// inactive is days >= inactive_days.
	inactive bool
}

// assess computes size, project activity and git state of one candidate and
// builds its finding. It returns nil for candidates that are dropped: below
// the size threshold, outside the guard or unreadable.
func (s *scan) assess(ctx context.Context, c candidate) *findings.Finding {
	p, err := s.resolve(c)
	if err != nil {
		return nil
	}
	sum, ok := s.size(ctx, c, p)
	if !ok || sum.SizeBytes < s.cfg.Thresholds.MinSizeBytes {
		return nil
	}
	f := facts{c: c, path: p, sum: sum, act: s.projectActivity(ctx, c.m.parentRel), git: s.gitStateOf(ctx, c.rel)}
	out := s.build(f)
	return &out
}

// size measures the candidate with a Fresh walk: the newest mtime feeds
// LastModified, AgeDays and recently_modified, and artifacts such as
// node_modules or target grow and change in place, which a cached directory
// record cannot notice. A symlink is reported with size 0 and the link's own
// mtime: it is never followed, only the link would be removed.
func (s *scan) size(ctx context.Context, c candidate, p string) (walk.DirSummary, bool) {
	if c.link {
		return walk.DirSummary{NewestModTime: c.linkModTime}, true
	}
	sum, err := walk.DirSize(ctx, p, walk.Options{CacheDir: s.env.CacheDir, Fresh: true})
	return sum, err == nil
}

// state derives the project inactivity from the activity signals.
func (s *scan) state(a activity) projectState {
	last := a.last()
	if last.IsZero() {
		return projectState{}
	}
	days := s.env.AgeDays(last)
	return projectState{known: true, days: days, inactive: days >= s.cfg.Detectors.BuildArtifacts.InactiveDays}
}

// confidence combines the inactivity with the entry's cap. Only an inactive
// project whose marker matched gets high; an active or unknown project, or a
// stray install without its marker, is medium; the entry cap can lower it
// further but never raises it.
func confidence(f facts, st projectState) findings.Confidence {
	c := findings.ConfidenceMedium
	if st.known && st.inactive && !f.c.m.missing {
		c = findings.ConfidenceHigh
	}
	if limit := f.c.m.rule.limit; limit.Rank() < c.Rank() {
		c = limit
	}
	return c
}

// build assembles the finding from the collected facts.
func (s *scan) build(f facts) findings.Finding {
	st := s.state(f.act)
	flags := s.riskFlags(f, st)
	var lastMod *time.Time
	if t := f.sum.NewestModTime; !t.IsZero() {
		lastMod = &t
	}
	return findings.Finding{
		ID:              findings.NewID(Name, findings.KindDir, f.path, ""),
		Detector:        Name,
		Scope:           s.target.Scope,
		Path:            f.path,
		Kind:            findings.KindDir,
		Tool:            f.c.m.rule.ecosystem,
		SizeBytes:       f.sum.SizeBytes,
		LastModified:    lastMod,
		AgeDays:         s.env.AgeDays(f.sum.NewestModTime),
		Confidence:      confidence(f, st),
		Evidence:        s.evidence(f, st),
		SuggestedAction: s.action(f, flags),
		RiskFlags:       flags,
	}
}

// riskFlags sets tracked_files (blocking), recently_modified when the project
// is active or the artifact itself changed within recent_days, gitignored and
// symlink.
func (s *scan) riskFlags(f facts, st projectState) []findings.RiskFlag {
	flags := []findings.RiskFlag{}
	if f.git.tracked {
		flags = append(flags, findings.RiskTrackedFiles)
	}
	if s.recentlyModified(f, st) {
		flags = append(flags, findings.RiskRecentlyModified)
	}
	if f.git.ignoreKnown && f.git.ignored {
		flags = append(flags, findings.RiskGitignored)
	}
	if f.c.link {
		flags = append(flags, findings.RiskSymlink)
	}
	return flags
}

func (s *scan) recentlyModified(f facts, st projectState) bool {
	if st.known && !st.inactive {
		return true
	}
	newest := f.sum.NewestModTime
	return !newest.IsZero() && s.env.AgeDays(newest) < s.cfg.Thresholds.RecentDays
}

// evidence lists why the directory was claimed and how confident the
// verdict is.
func (s *scan) evidence(f facts, st projectState) []findings.Evidence {
	r := f.c.m.rule
	ev := []findings.Evidence{
		{Code: "matches_catalog", Message: fmt.Sprintf("matches build artifact rule %s: %s", r.id, r.description), Value: r.id},
		{Code: "ecosystem", Message: "belongs to the " + r.ecosystem + " ecosystem", Value: r.ecosystem},
	}
	ev = append(ev, markerEvidence(f)...)
	ev = append(ev, s.projectEvidence(f, st)...)
	ev = append(ev, gitEvidence(f)...)
	if f.sum.Incomplete {
		ev = append(ev, findings.Evidence{Code: "unreadable", Message: "part of the directory could not be read, so the size is a lower bound", Value: true})
	}
	return ev
}

func markerEvidence(f facts) []findings.Evidence {
	switch {
	case f.c.m.missing:
		return []findings.Evidence{{Code: "marker_missing", Message: "no project marker next to it, so this may be a stray install", Value: true}}
	case f.c.m.marker != "":
		return []findings.Evidence{{Code: "marker", Message: "project marker " + f.c.m.marker + " found next to it", Value: f.c.m.marker}}
	}
	return nil
}

func (s *scan) projectEvidence(f facts, st projectState) []findings.Evidence {
	var ev []findings.Evidence
	switch {
	case !st.known:
		ev = append(ev, findings.Evidence{Code: "project_activity_unknown", Message: "no commit or source file shows when the project was last used", Value: true})
	case st.inactive:
		ev = append(ev, findings.Evidence{Code: "project_inactive_days", Message: fmt.Sprintf("project untouched for %d days", st.days), Value: st.days})
	default:
		ev = append(ev, findings.Evidence{Code: "project_active", Message: fmt.Sprintf("project active %d days ago", st.days), Value: st.days})
	}
	if !f.act.commit.IsZero() {
		ev = append(ev, findings.Evidence{
			Code:    "last_commit",
			Message: fmt.Sprintf("last commit touching the project %d days ago", s.env.AgeDays(f.act.commit)),
			Value:   f.act.commit.UTC().Format(time.RFC3339),
		})
	}
	return ev
}

func gitEvidence(f facts) []findings.Evidence {
	var ev []findings.Evidence
	if f.git.tracked {
		ev = append(ev, findings.Evidence{Code: "tracked_files", Message: "git tracks files below this directory", Value: true})
	}
	switch {
	case !f.git.ignoreKnown:
	case f.git.ignored:
		ev = append(ev, findings.Evidence{Code: "gitignored", Message: "ignored by git", Value: true})
	default:
		ev = append(ev, findings.Evidence{Code: "not_gitignored", Message: "not ignored by git, so git status may show it", Value: true})
	}
	return ev
}

// action suggests trash unless a blocking flag applies. Tracked directories
// are only suggested for trash under Force; the trash action re-checks and
// enforces the same rule on its own.
//
// A directory that could not be read completely is never suggested: the size
// is only a lower bound and the trash action refuses such directories at
// apply time anyway ("cannot inspect the whole directory"), so suggesting it
// would only end in "nothing to clean".
func (s *scan) action(f facts, flags []findings.RiskFlag) findings.SuggestedAction {
	if f.sum.Incomplete {
		return findings.SuggestedAction{Type: findings.ActionNone, Reason: "cannot read part of the directory"}
	}
	cmd := "trash " + f.path
	if f.git.tracked && s.env.Force && findings.Actionable(flags, true) {
		return findings.SuggestedAction{Type: findings.ActionTrash, Command: cmd, Reason: "forced: contains files tracked by git"}
	}
	if !findings.Actionable(flags, false) {
		reason := fmt.Sprintf("contains files tracked by git (committed %s/); removing it would show as deletions", path.Base(f.c.rel))
		return findings.SuggestedAction{Type: findings.ActionNone, Reason: reason}
	}
	return findings.SuggestedAction{Type: findings.ActionTrash, Command: cmd, Reason: f.c.m.rule.description}
}
