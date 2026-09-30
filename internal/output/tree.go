package output

import (
	"bytes"
	"io"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/findings"
)

func init() { Register(treeFormatter{}) }

// Box drawing pieces of the tree. Each continuation is exactly four columns
// wide so nested levels line up.
const (
	branchMid  = "├── "
	branchLast = "└── "
	pipePad    = "│   "
	blankPad   = "    "
)

// treeFormatter shows findings in their directory structure, one tree per
// scope. Width is not used: a tree line is never truncated, because cutting
// a path segment would make the tree lie about where something is.
type treeFormatter struct{}

func (treeFormatter) Name() string { return "tree" }

// Write renders the tree into a buffer first so a problem never leaves half a
// tree on the writer. Scopes without findings are left out: a workspace scan
// over hundreds of clean repositories would otherwise drown the result.
func (treeFormatter) Write(w io.Writer, r *findings.Report, opts Options) error {
	var buf bytes.Buffer
	p := newPainter(opts.Color)

	if len(r.Findings) == 0 {
		if !opts.Quiet {
			writeNothing(&buf, p)
		}
	} else {
		renderTrees(&buf, p, r, opts)
	}
	renderErrors(&buf, p, r.Errors)
	_, err := w.Write(buf.Bytes())
	return err
}

// treeNode is one line of the tree. Real nodes stand for a path component;
// pseudo nodes are leaves for findings that are not files (branches, git
// maintenance) and hang below the node of their Path.
type treeNode struct {
	name   string
	pseudo bool
	// own is the finding whose Path ends at this node, if any.
	own *findings.Finding
	// all holds every finding at or below the node, the basis of the
	// aggregated size (findings.Reclaimable avoids double counting nesting).
	all   []findings.Finding
	kids  []*treeNode
	index map[string]*treeNode
}

func newTreeNode(name string) *treeNode {
	return &treeNode{name: name, index: map[string]*treeNode{}}
}

// child returns the real child with the given name, creating it on demand.
func (n *treeNode) child(name string) *treeNode {
	if c, ok := n.index[name]; ok {
		return c
	}
	c := newTreeNode(name)
	n.index[name] = c
	n.kids = append(n.kids, c)
	return c
}

// size is the aggregated reclaimable size below (and including) the node.
func (n *treeNode) size() int64 { return findings.Reclaimable(n.all) }

// isFSKind mirrors the filesystem kinds of findings.Reclaimable: only these
// have a path node of their own; everything else is a pseudo child.
func isFSKind(k findings.Kind) bool {
	return k == findings.KindFile || k == findings.KindDir || k == findings.KindWorktree
}

// treePathParts splits the scope-relative path into components. A path that
// is not below the scope stays one absolute component, so it hangs under the
// root as a separate top-level node and is never mistaken for a scope child.
func treePathParts(scopePath, path string) []string {
	rel := relPath(scopePath, path)
	if rel == "." {
		return nil
	}
	// relPath returns its input unchanged for paths outside the scope; the
	// equality check also covers rooted-but-driveless paths on Windows, which
	// IsAbs does not consider absolute.
	if rel == path || filepath.IsAbs(rel) {
		return []string{Sanitize(rel)}
	}
	return strings.Split(Sanitize(filepath.ToSlash(rel)), "/")
}

// addFinding inserts f below root, recording it in the aggregate of every node
// on the way.
func addFinding(root *treeNode, f findings.Finding) {
	n := root
	n.all = append(n.all, f)
	for _, part := range treePathParts(f.Scope.Path, f.Path) {
		n = n.child(part)
		n.all = append(n.all, f)
	}
	if isFSKind(f.Kind) && n != root && n.own == nil {
		n.own = &f
		return
	}
	n.kids = append(n.kids, &treeNode{name: pseudoLabel(f), pseudo: true, own: &f, all: []findings.Finding{f}})
}

// pseudoLabel names a finding that is not a plain path: "[kind] ref", "[kind]"
// without a ref. A second filesystem finding for an already used path is
// labelled with its detector, which is the only thing telling the two apart.
func pseudoLabel(f findings.Finding) string {
	label := "[" + Sanitize(string(f.Kind)) + "]"
	if isFSKind(f.Kind) {
		label = "[" + Sanitize(f.Detector) + "]"
	}
	if f.Ref != "" {
		label += " " + Sanitize(f.Ref)
	}
	return label
}

// compact merges chains of real directories that have exactly one real child
// and no finding of their own into a single node, so the tree does not spend
// a line per path component. Nodes with pseudo children keep their own line
// because merging would attach the pseudo leaf to the wrong path.
func compact(n *treeNode) {
	for _, k := range n.kids {
		for k.canMerge() {
			c := k.kids[0]
			k.name += string(filepath.Separator) + c.name
			k.own, k.kids = c.own, c.kids
		}
		compact(k)
	}
}

func (n *treeNode) canMerge() bool {
	return !n.pseudo && n.own == nil && len(n.kids) == 1 && !n.kids[0].pseudo
}

// sortChildren orders siblings by aggregated size descending, then name, so
// the biggest wins come first and equal sizes stay deterministic.
func sortChildren(n *treeNode) {
	sort.SliceStable(n.kids, func(i, j int) bool {
		a, b := n.kids[i], n.kids[j]
		if sa, sb := a.size(), b.size(); sa != sb {
			return sa > sb
		}
		return a.name < b.name
	})
	for _, k := range n.kids {
		sortChildren(k)
	}
}

// scopeOrder lists scope paths: Report.Scopes in their order, then scopes that
// only occur in findings, sorted.
func scopeOrder(r *findings.Report) []string {
	var order []string
	seen := map[string]bool{}
	for _, s := range r.Scopes {
		if !seen[s.Path] {
			seen[s.Path] = true
			order = append(order, s.Path)
		}
	}
	var extra []string
	for _, f := range r.Findings {
		if !seen[f.Scope.Path] {
			seen[f.Scope.Path] = true
			extra = append(extra, f.Scope.Path)
		}
	}
	slices.Sort(extra)
	return append(order, extra...)
}

// buildScopeTree builds, compacts and sorts the tree of one scope.
func buildScopeTree(scopePath string, fs []findings.Finding) *treeNode {
	root := newTreeNode(scopePath)
	for _, f := range fs {
		addFinding(root, f)
	}
	compact(root)
	sortChildren(root)
	return root
}

// renderTrees writes one tree per scope that has findings, then the overall
// total (dropped by Quiet together with the per-scope sizes).
func renderTrees(buf *bytes.Buffer, p painter, r *findings.Report, opts Options) {
	byScope := map[string][]findings.Finding{}
	for _, f := range sortedCopy(r.Findings) {
		byScope[f.Scope.Path] = append(byScope[f.Scope.Path], f)
	}
	for _, sp := range scopeOrder(r) {
		fs := byScope[sp]
		if len(fs) == 0 {
			continue
		}
		root := buildScopeTree(sp, fs)
		head := p.bold(Sanitize(sp))
		if !opts.Quiet {
			head += "  " + p.dim(FormatSize(root.size()))
		}
		buf.WriteString(head + "\n")
		renderChildren(buf, p, root, "")
		buf.WriteString("\n")
	}
	if !opts.Quiet {
		buf.WriteString(p.bold(totalsLine(findings.ComputeTotals(r.Findings))) + "\n")
	}
}

// renderChildren writes the children of n, threading the prefix of the
// vertical guides through the recursion.
func renderChildren(buf *bytes.Buffer, p painter, n *treeNode, prefix string) {
	for i, k := range n.kids {
		connector, pad := branchMid, pipePad
		if i == len(n.kids)-1 {
			connector, pad = branchLast, blankPad
		}
		buf.WriteString(prefix + connector + renderNode(p, k) + "\n")
		renderChildren(buf, p, k, prefix+pad)
	}
}

// renderNode is the text of one line: an interior node shows its aggregated
// size, a finding node its own details.
func renderNode(p painter, n *treeNode) string {
	if n.own == nil {
		return n.name + "  " + p.dim(FormatSize(n.size()))
	}
	return renderLeaf(p, n.name, *n.own)
}

// renderLeaf renders size, age, confidence, detector, action and short risk
// flags. Flagged rows are one dimmed unit ending in the reason, so they are
// never mistaken for cleanup candidates; padding-free plain text is dimmed as a
// whole to keep nested escapes out.
func renderLeaf(p painter, name string, f findings.Finding) string {
	cells := []string{name, cellText(f, colSize), cellText(f, colAge)}
	conf := cellText(f, colConf)
	tail := []string{Sanitize(f.Detector), cellText(f, colAction)}

	if !f.Actionable() {
		parts := append(append(cells, conf), tail...)
		if fl := ShortRiskFlags(f.RiskFlags); fl != "" {
			parts = append(parts, fl)
		}
		parts = append(parts, "(flagged: "+notSuggestedReason(f)+")")
		return p.dim(strings.Join(parts, "  "))
	}
	parts := append(cells, cellStyle(p, colConf, f, conf))
	parts = append(parts, tail...)
	if fl := ShortRiskFlags(f.RiskFlags); fl != "" {
		parts = append(parts, p.yellow(fl))
	}
	return strings.Join(parts, "  ")
}
