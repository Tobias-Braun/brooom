package findings

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// NewID returns the deterministic ID of a finding. It hashes the detector,
// kind, cleaned path and ref so the same clutter gets the same ID across runs
// and machines-independent orderings. The path should already be absolute and
// symlink-resolved.
func NewID(detector string, kind Kind, path, ref string) string {
	h := sha256.New()
	for _, part := range []string{detector, string(kind), filepath.ToSlash(filepath.Clean(path)), ref} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// Report is the full result of a scan: all findings plus metadata and totals.
// It is what `--format json` prints and what `brooom clean --from` reads.
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	BrooomVersion string    `json:"brooom_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	// Scopes lists every repo/root/user location that was scanned.
	Scopes   []Scope   `json:"scopes"`
	Findings []Finding `json:"findings"`
	Totals   Totals    `json:"totals"`
	// Errors lists non-fatal problems (a detector failed on one repo, a
	// directory was unreadable). A scan with errors still returns findings.
	Errors []ScanError `json:"errors,omitempty"`
}

// ScanError is a non-fatal problem encountered during a scan.
type ScanError struct {
	Detector string `json:"detector,omitempty"`
	Path     string `json:"path,omitempty"`
	Message  string `json:"message"`
}

// Totals summarizes a report overall and per detector.
type Totals struct {
	Findings int `json:"findings"`
	// Actionable counts findings whose suggested action is not ActionNone.
	Actionable int `json:"actionable"`
	// ReclaimableBytes sums SizeBytes of actionable findings, counting nested
	// paths only once (see Reclaimable).
	ReclaimableBytes int64                    `json:"reclaimable_bytes"`
	ByDetector       map[string]DetectorTotal `json:"by_detector"`
}

// DetectorTotal summarizes the findings of one detector.
type DetectorTotal struct {
	Findings         int   `json:"findings"`
	Actionable       int   `json:"actionable"`
	ReclaimableBytes int64 `json:"reclaimable_bytes"`
}

// NewReport builds a report from findings, sorting them into a stable order
// (detector, path, ref) and computing totals.
func NewReport(version string, now time.Time, scopes []Scope, fs []Finding, errs []ScanError) *Report {
	sorted := append([]Finding(nil), fs...)
	Sort(sorted)
	if sorted == nil {
		sorted = []Finding{}
	}
	return &Report{
		SchemaVersion: SchemaVersion,
		BrooomVersion: version,
		GeneratedAt:   now.UTC(),
		Scopes:        scopes,
		Findings:      sorted,
		Totals:        ComputeTotals(sorted),
		Errors:        errs,
	}
}

// Sort orders findings by detector, then path, then ref.
func Sort(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Detector != b.Detector {
			return a.Detector < b.Detector
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Ref < b.Ref
	})
}

// ComputeTotals computes overall and per-detector totals.
func ComputeTotals(fs []Finding) Totals {
	t := Totals{ByDetector: map[string]DetectorTotal{}}
	byDet := map[string][]Finding{}
	for _, f := range fs {
		t.Findings++
		dt := t.ByDetector[f.Detector]
		dt.Findings++
		if f.Actionable() {
			t.Actionable++
			dt.Actionable++
		}
		t.ByDetector[f.Detector] = dt
		byDet[f.Detector] = append(byDet[f.Detector], f)
	}
	for det, list := range byDet {
		dt := t.ByDetector[det]
		dt.ReclaimableBytes = Reclaimable(list)
		t.ByDetector[det] = dt
	}
	t.ReclaimableBytes = Reclaimable(fs)
	return t
}

// Reclaimable sums SizeBytes of actionable findings. Filesystem findings
// (KindFile, KindDir, KindWorktree) nested below another actionable
// filesystem finding are skipped, so a node_modules inside an already
// reported directory is not counted twice.
func Reclaimable(fs []Finding) int64 {
	var paths []string
	var total int64
	for _, f := range fs {
		if !f.Actionable() {
			continue
		}
		if !isFilesystemKind(f.Kind) {
			total += f.SizeBytes
			continue
		}
		paths = append(paths, f.Path)
	}
	// Keyed by the cleaned path because TopLevel returns cleaned paths (on
	// Windows Clean also converts forward slashes).
	sizes := map[string]int64{}
	for _, f := range fs {
		if f.Actionable() && isFilesystemKind(f.Kind) {
			p := filepath.Clean(f.Path)
			if f.SizeBytes > sizes[p] {
				sizes[p] = f.SizeBytes
			}
		}
	}
	for _, p := range TopLevel(paths) {
		total += sizes[p]
	}
	return total
}

func isFilesystemKind(k Kind) bool {
	return k == KindFile || k == KindDir || k == KindWorktree
}

// TopLevel returns the deduplicated paths that are not nested below another
// path in the list. Comparison is separator-aware ("/a/bc" is not below
// "/a/b"). The result is sorted.
func TopLevel(paths []string) []string {
	cleaned := make([]string, 0, len(paths))
	for _, p := range paths {
		cleaned = append(cleaned, filepath.Clean(p))
	}
	// Shorter paths first, so every ancestor is kept before its descendants
	// are checked. Plain lexical order is not enough: "/a/b-x" sorts between
	// "/a/b" and "/a/b/c".
	sort.Slice(cleaned, func(i, j int) bool {
		if len(cleaned[i]) != len(cleaned[j]) {
			return len(cleaned[i]) < len(cleaned[j])
		}
		return cleaned[i] < cleaned[j]
	})
	kept := map[string]bool{}
	var out []string
	for _, p := range cleaned {
		if kept[p] || hasKeptAncestor(kept, p) {
			continue
		}
		kept[p] = true
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

func hasKeptAncestor(kept map[string]bool, p string) bool {
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if kept[dir] {
			return true
		}
		if next := filepath.Dir(dir); next == dir {
			return false
		}
	}
}

// IsWithin reports whether path is strictly below parent. Both paths must be
// clean and absolute; the check is purely lexical and case-sensitive. Use the
// scope package for symlink- and case-aware containment checks.
func IsWithin(parent, path string) bool {
	if parent == path {
		return false
	}
	prefix := parent
	if !strings.HasSuffix(prefix, string(filepath.Separator)) {
		prefix += string(filepath.Separator)
	}
	return strings.HasPrefix(path, prefix)
}
