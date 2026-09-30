package output

import (
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// extendedReport builds on fixtureReport (whose goldens must not change) and
// adds what the machine formats and the tree need: a git maintenance finding
// and a worktree-missing finding (both must stay out of plain), an actionable
// worktree outside its scope's path, a second, user-level scope with a path
// containing characters that HTML escaping would mangle, and findings with
// nil evidence/risk flags. Paths use forward slashes so json goldens are
// byte-identical on Windows.
func extendedReport() *findings.Report {
	base := fixtureReport()
	shop := findings.Scope{Type: findings.ScopeRepo, Path: "/work/shop"}
	user := findings.Scope{Type: findings.ScopeUser, Path: "/home/dev/.cache/tool"}

	extra := []findings.Finding{
		{ID: "gc1", Detector: "git-bloat", Scope: shop, Path: "/work/shop", Kind: findings.KindGitPacks, SizeBytes: 3_000_000,
			Confidence: findings.ConfidenceMedium,
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionGitGC, Command: "git gc",
				Args: map[string]string{"expire": "2.weeks.ago"}}},
		{ID: "wt2", Detector: "worktrees", Scope: shop, Path: "/work/shop-wt2", Kind: findings.KindWorktree, Ref: "feat/done",
			SizeBytes: 10_000_000, LastModified: fixtureTime(60), AgeDays: 60, Confidence: findings.ConfidenceHigh,
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionRemoveWorktree}},
		{ID: "wt3", Detector: "worktrees", Scope: shop, Path: "/work/gone-wt", Kind: findings.KindWorktreeMissing,
			Confidence: findings.ConfidenceHigh, SuggestedAction: findings.SuggestedAction{Type: findings.ActionPruneWorktrees}},
		{ID: "ai1", Detector: "ai-artifacts", Scope: user, Path: "/home/dev/.cache/tool/logs/a&b<c>.log", Kind: findings.KindFile,
			Tool: "claude-code", SizeBytes: 4096, LastModified: fixtureTime(30), AgeDays: 30, Confidence: findings.ConfidenceHigh,
			Evidence:        []findings.Evidence{{Code: "age", Message: "last modified 30 days ago", Value: "30d"}},
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash},
			RiskFlags:       []findings.RiskFlag{findings.RiskOutsideRepo},
			Meta:            map[string]string{"tool_version": "1.0"}},
	}
	all := append(append([]findings.Finding{}, base.Findings...), extra...)
	return findings.NewReport("test", fixtureNow, []findings.Scope{shop, user}, all, base.Errors)
}

// lineBreakReport has one listable finding, one with a newline in its path
// and one with a carriage return in its branch name.
func lineBreakReport() *findings.Report {
	shop := findings.Scope{Type: findings.ScopeRepo, Path: "/work/shop"}
	trash := findings.SuggestedAction{Type: findings.ActionTrash}
	fs := []findings.Finding{
		{Detector: "build-artifacts", Scope: shop, Path: "/work/shop/dist", Kind: findings.KindDir, SizeBytes: 10, SuggestedAction: trash},
		{Detector: "build-artifacts", Scope: shop, Path: "/work/shop/evil\nname", Kind: findings.KindDir, SizeBytes: 10, SuggestedAction: trash},
		{Detector: "merged-branch", Scope: shop, Path: "/work/shop", Kind: findings.KindBranch, Ref: "odd\rbranch",
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionDeleteBranch}},
	}
	return findings.NewReport("test", fixtureNow, []findings.Scope{shop}, fs, nil)
}
