package output

import (
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// riskLabels maps every risk flag to a short label for table cells. Labels
// are lowercase, at most 10 characters and unique; a test walks
// findings.AllRiskFlags so a new flag cannot be forgotten.
var riskLabels = map[findings.RiskFlag]string{
	findings.RiskUnpushedCommits:    "unpushed",
	findings.RiskUncommittedChanges: "changes",
	findings.RiskFileOpen:           "open",
	findings.RiskWorktreeDirty:      "dirty",
	findings.RiskWorktreeLocked:     "locked",
	findings.RiskHasOpenPR:          "open-pr",
	findings.RiskCurrentBranch:      "current",
	findings.RiskProtectedBranch:    "protected",
	findings.RiskTrackedFiles:       "tracked",
	findings.RiskRecentlyModified:   "recent",
	findings.RiskGitignored:         "ignored",
	findings.RiskNeverPushed:        "no-remote",
	findings.RiskUpstreamGone:       "gone",
	findings.RiskSymlink:            "symlink",
	findings.RiskOutsideRepo:        "outside",
}

// ShortRiskFlags joins the short labels of flags with ",". Unknown flags fall
// back to their raw string so a flag added later is still displayed.
func ShortRiskFlags(flags []findings.RiskFlag) string {
	parts := make([]string, 0, len(flags))
	for _, f := range flags {
		if l, ok := riskLabels[f]; ok {
			parts = append(parts, l)
		} else {
			parts = append(parts, string(f))
		}
	}
	return strings.Join(parts, ",")
}

// blockingLabels lists the short labels of the blocking flags, used as the
// reason of a not-suggested row when the detector gave none.
func blockingLabels(flags []findings.RiskFlag) string {
	var blocking []findings.RiskFlag
	for _, f := range flags {
		if f.Blocking() {
			blocking = append(blocking, f)
		}
	}
	return ShortRiskFlags(blocking)
}
