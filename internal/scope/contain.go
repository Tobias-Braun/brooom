package scope

import (
	"os"
	"path/filepath"
	"strings"
)

// location is one allowed directory together with the case sensitivity of the
// filesystem it lives on.
type location struct {
	// path is absolute, symlink-resolved and cleaned.
	path string
	// fold is true when the filesystem was probed as case-insensitive.
	fold bool
}

// contains reports whether the resolved absolute path candidate lies inside
// or equals the location. Containment is component-wise, so /a/b contains
// /a/b/c but neither /a/bc nor /a/b.evil. It returns the candidate respelled
// with the location's own prefix (so later string comparisons work) and the
// number of components below the location.
//
// Case folding applies only to locations probed as case-insensitive; on a
// case-sensitive location /a/Repo and /a/repo stay distinct. The volume name
// (drive letter, UNC server and share) is always compared case-insensitively
// because it is empty on unix and case-insensitive on Windows.
func (l location) contains(candidate string) (string, int, bool) {
	baseVol := volumeName(l.path)
	base := splitComponents(l.path[len(baseVol):])
	candVol := volumeName(candidate)
	cand := splitComponents(candidate[len(candVol):])
	if !strings.EqualFold(baseVol, candVol) || len(cand) < len(base) {
		return "", 0, false
	}
	if l.samePrefix(base, cand[:len(base)]) {
		return respell(l.path, cand[len(base):]), len(cand) - len(base), true
	}
	if !l.fold {
		return "", 0, false
	}
	return l.containsBySameFile(candVol, cand, len(base))
}

// samePrefix compares the location's components with the candidate's.
func (l location) samePrefix(base, cand []string) bool {
	for i := range base {
		if base[i] == cand[i] {
			continue
		}
		if !l.fold || !strings.EqualFold(base[i], cand[i]) {
			return false
		}
	}
	return true
}

// containsBySameFile is the fallback for case-insensitive locations when the
// string comparison fails: Unicode normalization differences (NFC versus NFD
// on macOS) look like different names although the filesystem treats them as
// one. It walks the candidate's existing ancestors, deepest first, and accepts
// the candidate when one of them is the very same file as the location.
func (l location) containsBySameFile(vol string, cand []string, minDepth int) (string, int, bool) {
	baseInfo, err := os.Lstat(l.path)
	if err != nil {
		return "", 0, false
	}
	for d := len(cand); d >= minDepth && d > 0; d-- {
		info, err := os.Lstat(joinComponents(vol, cand[:d]))
		if err != nil || !os.SameFile(baseInfo, info) {
			continue
		}
		return respell(l.path, cand[d:]), len(cand) - d, true
	}
	return "", 0, false
}

// joinComponents builds an absolute path from a volume and components.
func joinComponents(vol string, comps []string) string {
	return vol + string(os.PathSeparator) + strings.Join(comps, string(os.PathSeparator))
}

// respell appends the components below a location to the location's path.
func respell(base string, rest []string) string {
	if len(rest) == 0 {
		return base
	}
	return filepath.Join(append([]string{base}, rest...)...)
}
