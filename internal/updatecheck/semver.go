package updatecheck

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed semantic version. Build metadata is kept for display
// but, as the semver spec demands, never takes part in comparisons.
type Version struct {
	Major, Minor, Patch uint64
	Pre                 []string
	Build               string
}

// ErrInvalidVersion is wrapped by every ParseVersion failure so callers can
// tell a malformed tag from a network problem.
var ErrInvalidVersion = errors.New("invalid version")

// ParseVersion parses "v1.2.3" and "1.2.3" with optional "-pre.release" and
// "+build" suffixes. It is deliberately strict about the core (exactly three
// numeric parts) because the input comes from a remote server and a tag we
// cannot order must be reported instead of guessed at.
func ParseVersion(s string) (Version, error) {
	orig := s
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	var v Version
	s, build, hasBuild := strings.Cut(s, "+")
	if hasBuild {
		if build == "" {
			return Version{}, fmt.Errorf("%w %q: empty build metadata", ErrInvalidVersion, orig)
		}
		v.Build = build
	}
	core, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%w %q: want MAJOR.MINOR.PATCH", ErrInvalidVersion, orig)
	}
	nums := []*uint64{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		n, err := parseNumeric(p)
		if err != nil {
			return Version{}, fmt.Errorf("%w %q: %w", ErrInvalidVersion, orig, err)
		}
		*nums[i] = n
	}
	if hasPre {
		ids := strings.Split(pre, ".")
		for _, id := range ids {
			if err := checkPreIdentifier(id); err != nil {
				return Version{}, fmt.Errorf("%w %q: %w", ErrInvalidVersion, orig, err)
			}
		}
		v.Pre = ids
	}
	return v, nil
}

// parseNumeric parses a digits-only identifier without leading zeros.
func parseNumeric(p string) (uint64, error) {
	if !isDigits(p) {
		return 0, fmt.Errorf("%q is not a number", p)
	}
	if len(p) > 1 && p[0] == '0' {
		return 0, fmt.Errorf("%q has a leading zero", p)
	}
	n, err := strconv.ParseUint(p, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is out of range", p)
	}
	return n, nil
}

func checkPreIdentifier(id string) error {
	if id == "" {
		return errors.New("empty pre-release identifier")
	}
	for _, r := range id {
		if !isIdentifierRune(r) {
			return fmt.Errorf("invalid character %q in pre-release", r)
		}
	}
	if isDigits(id) && len(id) > 1 && id[0] == '0' {
		return fmt.Errorf("numeric pre-release %q has a leading zero", id)
	}
	return nil
}

func isIdentifierRune(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		return true
	}
	return r == '-'
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// String renders the version without a leading "v".
func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if len(v.Pre) > 0 {
		s += "-" + strings.Join(v.Pre, ".")
	}
	if v.Build != "" {
		s += "+" + v.Build
	}
	return s
}

// Compare returns -1, 0 or 1 when v is lower than, equal to or higher than o
// following semver precedence (1.2.3-rc.1 < 1.2.3).
func (v Version) Compare(o Version) int {
	for _, p := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if c := cmpUint(p[0], p[1]); c != 0 {
			return c
		}
	}
	return comparePre(v.Pre, o.Pre)
}

func cmpUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// comparePre orders pre-release lists: a release without one wins, numeric
// identifiers sort below alphanumeric ones, and a longer list wins when all
// shared identifiers are equal.
func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		if c := comparePreIdentifier(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmpUint(uint64(len(a)), uint64(len(b)))
}

func comparePreIdentifier(a, b string) int {
	an, bn := isDigits(a), isDigits(b)
	switch {
	case an && bn:
		// Length first (no leading zeros), so huge numbers cannot overflow.
		if c := cmpUint(uint64(len(a)), uint64(len(b))); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	case an:
		return -1
	case bn:
		return 1
	}
	return strings.Compare(a, b)
}

// pseudoVersion matches the timestamp+commit tail of Go pseudo-versions such
// as v0.0.0-20240101120000-abcdef123456 and v1.2.4-0.20240101120000-abcdef123456,
// which is what a VCS-stamped build from an untagged commit reports.
var pseudoVersion = regexp.MustCompile(`-(0\.)?\d{14}-[0-9a-f]{12}`)

// IsDevVersion reports whether a build version carries no release identity:
// "dev", empty, "(devel)" or a Go pseudo-version. Nothing can be compared for
// those, so callers skip the comparison instead of nagging.
func IsDevVersion(v string) bool {
	v = strings.TrimSpace(v)
	switch v {
	case "", "dev", "(devel)":
		return true
	}
	return pseudoVersion.MatchString(v)
}
