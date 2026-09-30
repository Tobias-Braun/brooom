package scope

import (
	"path"
	"strings"
)

// Excluded reports whether the directory at rel (relative to the discovery
// root, forward slashes) is excluded by one of the glob patterns.
//
// A pattern without "/" matches the base name at any depth ("tmp" hides
// every directory called tmp). A pattern with "/" is anchored at the root
// and split into segments: "**" matches zero or more whole segments, other
// segments use path.Match ("a/*/b", "**/scratch"). A trailing or leading
// slash is ignored. Invalid patterns never match; validating them is the
// job of config loading. The root itself (empty rel) is never excluded.
//
// Names compare case-insensitively where the filesystem does (Windows and
// macOS, see foldNames), like every other name matcher: otherwise a path the
// user excluded as "Scratch" would reach the plan when it is spelled
// "scratch" on disk.
func Excluded(patterns []string, rel string) bool {
	return excluded(patterns, rel, foldNames())
}

// excluded is Excluded with the case folding decided by the caller, so both
// behaviours are testable on every OS. Folding lower-cases pattern and path
// alike before matching.
func excluded(patterns []string, rel string, fold bool) bool {
	rel = strings.Trim(rel, "/")
	if fold {
		rel = strings.ToLower(rel)
	}
	if rel == "" {
		return false
	}
	segs := strings.Split(rel, "/")
	for _, p := range patterns {
		p = strings.Trim(p, "/")
		if fold {
			p = strings.ToLower(p)
		}
		if matchPattern(p, segs) {
			return true
		}
	}
	return false
}

// matchPattern matches one pattern against the segments of a path.
func matchPattern(pattern string, segs []string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "/") {
		if pattern == "**" {
			return true
		}
		ok, err := path.Match(pattern, segs[len(segs)-1])
		return err == nil && ok
	}
	return matchSegments(strings.Split(pattern, "/"), segs)
}

// matchSegments matches pattern segments against path segments; "**" may
// consume any number of segments, including none.
func matchSegments(pat, segs []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			rest := pat[1:]
			for i := 0; i <= len(segs); i++ {
				if matchSegments(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}
