package gitx

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
)

// Version is a parsed git version. Vendor suffixes such as ".windows.1" or
// "(Apple Git-154)" are ignored.
type Version struct {
	Major, Minor, Patch int
}

// MinGitVersion is the oldest git Brooom supports (`%(worktreepath)` in
// for-each-ref arrived in 2.23). Newer features degrade gracefully.
var MinGitVersion = Version{Major: 2, Minor: 23}

var versionRe = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion parses the output of `git version`, for example
// "git version 2.43.0.windows.1" or "git version 2.39.5 (Apple Git-154)".
func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("gitx: cannot parse git version %q", s)
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	return v, nil
}

// AtLeast reports whether v is at least major.minor.
func (v Version) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

// GitVersion returns the version of the git binary behind r.
func GitVersion(ctx context.Context, r Runner) (Version, error) {
	out, err := r.Run(ctx, ".", "version")
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(out)
}
