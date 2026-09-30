package output

import (
	"context"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// fakeDetector only supplies a description for group headers.
type fakeDetector struct{ name, desc string }

func (d fakeDetector) Name() string              { return d.name }
func (d fakeDetector) Description() string       { return d.desc }
func (d fakeDetector) Category() detect.Category { return detect.CategoryGit }
func (d fakeDetector) Detect(context.Context, *detect.Env, scope.Target, func(findings.Finding)) error {
	return nil
}

// Two detectors of the fixture are registered; "worktrees" deliberately is
// not, to cover the header fallback for unregistered detectors.
func init() {
	detect.Register(fakeDetector{"merged-branch", "branches already merged into the base branch"})
	detect.Register(fakeDetector{"build-artifacts", "rebuildable build output"})
}

var fixtureNow = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

func fixtureTime(daysAgo int) *time.Time {
	t := fixtureNow.AddDate(0, 0, -daysAgo)
	return &t
}

// fixtureReport covers branches with refs and flags, a large directory,
// nested findings, a flagged worktree with a reason and one scan error.
// Scope paths use forward slashes; goldens are slash-normalized.
func fixtureReport() *findings.Report {
	shop := findings.Scope{Type: findings.ScopeRepo, Path: "/work/shop"}
	trash := findings.SuggestedAction{Type: findings.ActionTrash}
	del := findings.SuggestedAction{Type: findings.ActionDeleteBranch}
	fs := []findings.Finding{
		{Detector: "worktrees", Scope: shop, Path: "/work/shop-wt", Kind: findings.KindWorktree, SizeBytes: 52_000_000,
			LastModified: fixtureTime(3), AgeDays: 3, Confidence: findings.ConfidenceLow,
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionNone, Reason: "worktree has uncommitted changes"},
			RiskFlags:       []findings.RiskFlag{findings.RiskWorktreeDirty}},
		{Detector: "merged-branch", Scope: shop, Path: "/work/shop", Kind: findings.KindBranch, Ref: "feat/old-cart",
			LastModified: fixtureTime(94), AgeDays: 94, Confidence: findings.ConfidenceHigh, SuggestedAction: del,
			RiskFlags: []findings.RiskFlag{findings.RiskUpstreamGone}},
		{Detector: "merged-branch", Scope: shop, Path: "/work/shop", Kind: findings.KindBranch, Ref: "feat/wip",
			LastModified: fixtureTime(12), AgeDays: 12, Confidence: findings.ConfidenceMedium,
			SuggestedAction: findings.SuggestedAction{Type: findings.ActionNone},
			RiskFlags:       []findings.RiskFlag{findings.RiskUnpushedCommits, findings.RiskNeverPushed}},
		{Detector: "build-artifacts", Scope: shop, Path: "/work/shop/node_modules", Kind: findings.KindDir, SizeBytes: 1_234_567_890,
			LastModified: fixtureTime(400), AgeDays: 400, Confidence: findings.ConfidenceHigh, SuggestedAction: trash},
		{Detector: "build-artifacts", Scope: shop, Path: "/work/shop/node_modules/.cache", Kind: findings.KindDir, SizeBytes: 500_000_000,
			LastModified: fixtureTime(45), AgeDays: 45, Confidence: findings.ConfidenceLow, SuggestedAction: trash},
		{Detector: "build-artifacts", Scope: shop, Path: "/work/shop/packages/storefront/.next/cache/webpack/client-production", Kind: findings.KindDir, SizeBytes: 88_000_000,
			LastModified: fixtureTime(200), AgeDays: 200, Confidence: findings.ConfidenceHigh, SuggestedAction: trash},
		{Detector: "build-artifacts", Scope: shop, Path: "/work/shop/dist", Kind: findings.KindDir, SizeBytes: 999_950,
			Confidence: findings.ConfidenceMedium, SuggestedAction: trash},
	}
	errs := []findings.ScanError{{Detector: "git-bloat", Path: "/work/shop", Message: "git count-objects failed"}}
	return findings.NewReport("test", fixtureNow, []findings.Scope{shop}, fs, errs)
}

// emptyReport has neither findings nor errors.
func emptyReport() *findings.Report {
	return findings.NewReport("test", fixtureNow, nil, nil, nil)
}

// longPathReport has a single finding with a very long relative path.
func longPathReport() *findings.Report {
	scopePath := "/work/shop"
	p := scopePath + "/" + strings.Repeat("deeply/nested/", 8) + "artifact.log"
	f := findings.Finding{Detector: "build-artifacts", Scope: findings.Scope{Type: findings.ScopeRepo, Path: scopePath},
		Path: p, Kind: findings.KindFile, SizeBytes: 2048, LastModified: fixtureTime(70), AgeDays: 70,
		Confidence: findings.ConfidenceHigh, SuggestedAction: findings.SuggestedAction{Type: findings.ActionTrash}}
	return findings.NewReport("test", fixtureNow, nil, []findings.Finding{f}, nil)
}

// errorsOnlyReport has scan errors but no findings.
func errorsOnlyReport() *findings.Report {
	return findings.NewReport("test", fixtureNow, nil, nil, []findings.ScanError{
		{Detector: "worktrees", Path: "/work/shop", Message: "not a git repository"},
		{Message: "walk aborted"},
	})
}
