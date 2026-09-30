package output

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

// filepathSep joins compacted chains, which use the OS separator.
const filepathSep = filepath.Separator

func TestTreeGoldens(t *testing.T) {
	tests := []struct {
		name string
		r    *findings.Report
		opts Options
	}{
		{"tree_nocolor", extendedReport(), Options{}},
		{"tree_color", extendedReport(), Options{Color: true}},
		{"tree_quiet", extendedReport(), Options{Quiet: true}},
		{"tree_empty", emptyReport(), Options{}},
		{"tree_errors_only", errorsOnlyReport(), Options{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertGolden(t, tt.name, render(t, "tree", tt.r, tt.opts))
		})
	}
}

func TestTreeColorOnlyAddsEscapes(t *testing.T) {
	plain := render(t, "tree", extendedReport(), Options{})
	color := render(t, "tree", extendedReport(), Options{Color: true})
	if strings.Contains(plain, "\x1b") {
		t.Error("tree without color contains ANSI")
	}
	if stripANSI(color) != plain {
		t.Errorf("color output differs beyond escapes\n%s\n---\n%s", stripANSI(color), plain)
	}
}

func TestTreeIgnoresWidth(t *testing.T) {
	if render(t, "tree", extendedReport(), Options{Width: 20}) != render(t, "tree", extendedReport(), Options{}) {
		t.Error("tree output depends on Width")
	}
}

func TestTreeEmptyMatchesTable(t *testing.T) {
	if render(t, "tree", emptyReport(), Options{}) != render(t, "table", emptyReport(), Options{}) {
		t.Error("empty tree differs from empty table")
	}
	if out := render(t, "tree", emptyReport(), Options{Quiet: true}); out != "" {
		t.Errorf("quiet empty tree wrote %q", out)
	}
}

func TestTreeLastLineMatchesTable(t *testing.T) {
	last := func(s string) string {
		lines := strings.Split(strings.TrimSpace(s), "\n")
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.Contains(lines[i], "reclaimable") {
				return lines[i]
			}
		}
		return ""
	}
	tree := last(render(t, "tree", fixtureReport(), Options{}))
	table := last(render(t, "table", fixtureReport(), Options{}))
	if tree == "" || tree != table {
		t.Errorf("tree total %q, table total %q", tree, table)
	}
}

// buildFor builds the tree of a single scope for structural assertions.
func buildFor(fs ...findings.Finding) *treeNode {
	return buildScopeTree("/s", fs)
}

func fsFinding(path string, size int64, action findings.ActionType) findings.Finding {
	return findings.Finding{Detector: "d", Scope: findings.Scope{Type: findings.ScopeRepo, Path: "/s"}, Path: path,
		Kind: findings.KindDir, SizeBytes: size, SuggestedAction: findings.SuggestedAction{Type: action}}
}

func TestTreeCompactsSingleChildChains(t *testing.T) {
	root := buildFor(fsFinding("/s/a/b/c/node_modules", 10, findings.ActionTrash))
	if len(root.kids) != 1 || root.kids[0].name != strings.Join([]string{"a", "b", "c", "node_modules"}, string(filepathSep)) {
		t.Fatalf("chain not compacted: %+v", root.kids[0].name)
	}
}

func TestTreeDoesNotCompactThroughFindingsOrForks(t *testing.T) {
	root := buildFor(
		fsFinding("/s/a", 100, findings.ActionTrash),
		fsFinding("/s/a/b/c", 10, findings.ActionTrash),
		fsFinding("/s/x/y", 5, findings.ActionTrash),
		fsFinding("/s/x/z", 5, findings.ActionTrash),
	)
	names := []string{}
	for _, k := range root.kids {
		names = append(names, k.name)
	}
	if strings.Join(names, ",") != "a,x" {
		t.Fatalf("top level = %v", names)
	}
	if got := root.kids[0].kids[0].name; got != "b"+string(filepathSep)+"c" {
		t.Errorf("below a finding the chain must still compact, got %q", got)
	}
	if len(root.kids[1].kids) != 2 {
		t.Error("fork x must keep both children")
	}
}

func TestTreeAggregatesWithoutDoubleCounting(t *testing.T) {
	root := buildFor(
		fsFinding("/s/nm", 100, findings.ActionTrash),
		fsFinding("/s/nm/.cache", 40, findings.ActionTrash),
		fsFinding("/s/other", 30, findings.ActionNone),
	)
	if got := root.size(); got != 100 {
		t.Errorf("root size = %d, want 100 (nested and flagged not added)", got)
	}
}

func TestTreeSortsBySizeThenName(t *testing.T) {
	root := buildFor(
		fsFinding("/s/b", 10, findings.ActionTrash),
		fsFinding("/s/a", 10, findings.ActionTrash),
		fsFinding("/s/big", 99, findings.ActionTrash),
		fsFinding("/s/flagged", 500, findings.ActionNone),
	)
	var names []string
	for _, k := range root.kids {
		names = append(names, k.name)
	}
	if got := strings.Join(names, ","); got != "big,a,b,flagged" {
		t.Errorf("order = %s", got)
	}
}

func TestTreePseudoChildrenAndOutsideScope(t *testing.T) {
	branch := findings.Finding{Detector: "merged-branch", Scope: findings.Scope{Path: "/s"}, Path: "/s", Kind: findings.KindBranch,
		Ref: "feat/x", SuggestedAction: findings.SuggestedAction{Type: findings.ActionDeleteBranch}}
	gc := findings.Finding{Detector: "git-bloat", Scope: findings.Scope{Path: "/s"}, Path: "/s", Kind: findings.KindGitPacks,
		SizeBytes: 7, SuggestedAction: findings.SuggestedAction{Type: findings.ActionGitGC}}
	outside := fsFinding("/elsewhere/wt", 50, findings.ActionRemoveWorktree)

	root := buildFor(branch, gc, outside)
	labels := map[string]bool{}
	for _, k := range root.kids {
		labels[k.name] = true
	}
	for _, want := range []string{"[branch] feat/x", "[git-packs]", "/elsewhere/wt"} {
		if !labels[want] {
			t.Errorf("missing top-level node %q in %v", want, labels)
		}
	}
}
