package catalog

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

// GlobMatch reports whether the forward-slash path name matches pattern.
//
// path.Match cannot be used because it lacks "**". The syntax is:
//
//   - "*" matches any run of characters within one segment, "?" exactly one
//     character; neither ever crosses "/".
//   - "[abc]", "[a-z]" and "[!x]" are character classes (a "]" directly after
//     the opening bracket is a literal).
//   - "**" as a whole segment matches zero or more segments, so "a/**/b"
//     matches "a/b" and "a/x/y/b", and a trailing "a/**" matches "a" itself and
//     everything below it.
//
// There is no escape character: backslashes are ordinary characters, which
// keeps Windows-flavoured input unsurprising. With fold set the comparison is
// case-insensitive (used on Windows and macOS). GlobMatch assumes a pattern
// accepted by ValidateGlob; an invalid class simply never matches.
func GlobMatch(pattern, name string, fold bool) bool {
	if fold {
		pattern, name = foldCase(pattern), foldCase(name)
	}
	return matchSegments(splitSegments(pattern), splitSegments(name))
}

// foldCase lower-cases s. Both sides of a comparison are folded, so class
// ranges such as [A-Z] keep working (they become [a-z]).
func foldCase(s string) string {
	return strings.Map(unicode.ToLower, s)
}

// splitSegments splits a slash separated path; the empty string has no
// segments so that it never matches a real name.
func splitSegments(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}

func matchSegments(pat, name []string) bool {
	if len(pat) > 0 && pat[0] == "**" {
		// Consecutive "**" are equivalent to one.
		for len(pat) > 1 && pat[1] == "**" {
			pat = pat[1:]
		}
		if len(pat) == 1 {
			return true
		}
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(pat) == 0 {
		return len(name) == 0
	}
	if len(name) == 0 || !matchSegment([]rune(pat[0]), []rune(name[0])) {
		return false
	}
	return matchSegments(pat[1:], name[1:])
}

// matchSegment matches a single path segment against a pattern without "/".
func matchSegment(p, s []rune) bool {
	for len(p) > 0 {
		if p[0] == '*' {
			return matchStar(p, s)
		}
		rest, ok := matchOne(p, s)
		if !ok {
			return false
		}
		p, s = rest, s[1:]
	}
	return len(s) == 0
}

// matchStar handles a leading "*" by trying every possible split point.
func matchStar(p, s []rune) bool {
	for len(p) > 0 && p[0] == '*' {
		p = p[1:]
	}
	if len(p) == 0 {
		return true
	}
	for i := 0; i <= len(s); i++ {
		if matchSegment(p, s[i:]) {
			return true
		}
	}
	return false
}

// matchOne matches the first pattern element ("?", a class or a literal)
// against the first rune of s and returns the pattern remainder.
func matchOne(p, s []rune) ([]rune, bool) {
	if len(s) == 0 {
		return nil, false
	}
	switch p[0] {
	case '?':
		return p[1:], true
	case '[':
		matched, rest, err := matchClass(p, s[0])
		return rest, err == nil && matched
	default:
		return p[1:], s[0] == p[0]
	}
}

// matchClass matches r against the class at the start of p (p[0] == '[') and
// returns the pattern remainder after the closing bracket.
func matchClass(p []rune, r rune) (bool, []rune, error) {
	i := 1
	negate := len(p) > 1 && (p[1] == '!' || p[1] == '^')
	if negate {
		i++
	}
	matched := false
	for start := i; i < len(p); i++ {
		if p[i] == ']' && i > start {
			return matched != negate, p[i+1:], nil
		}
		lo, hi, next := classItem(p, i)
		if lo > hi {
			return false, nil, errors.New("reversed range in character class")
		}
		matched = matched || (lo <= r && r <= hi)
		i = next
	}
	return false, nil, errors.New("unbalanced [")
}

// classItem reads a single character or a "lo-hi" range at p[i] and returns
// its bounds and the index of its last rune.
func classItem(p []rune, i int) (lo, hi rune, last int) {
	if i+2 < len(p) && p[i+1] == '-' && p[i+2] != ']' {
		return p[i], p[i+2], i + 2
	}
	return p[i], p[i], i
}

// ValidateGlob checks that pattern is a well-formed catalog glob. It rejects
// empty patterns, a trailing "/", empty segments (leading or doubled "/"),
// "**" embedded in a segment ("a**b", "**.log") and unbalanced or malformed
// "[" classes.
func ValidateGlob(pattern string) error {
	if pattern == "" {
		return errors.New("pattern must not be empty")
	}
	if strings.HasSuffix(pattern, "/") {
		return fmt.Errorf("pattern %q must not end with \"/\"", pattern)
	}
	for _, seg := range strings.Split(pattern, "/") {
		if err := validateSegment(pattern, seg); err != nil {
			return err
		}
	}
	return nil
}

func validateSegment(pattern, seg string) error {
	if seg == "" {
		return fmt.Errorf("pattern %q has an empty path segment", pattern)
	}
	if strings.Contains(seg, "**") && seg != "**" {
		return fmt.Errorf("pattern %q: \"**\" must be a whole path segment", pattern)
	}
	rs := []rune(seg)
	for i := 0; i < len(rs); i++ {
		if rs[i] != '[' {
			continue
		}
		// matchClass consumes the class; the probe rune is irrelevant.
		_, rest, err := matchClass(rs[i:], 0)
		if err != nil {
			return fmt.Errorf("pattern %q: %w", pattern, err)
		}
		i = len(rs) - len(rest) - 1
	}
	return nil
}

// hasWildcard reports whether s contains a glob metacharacter.
func hasWildcard(s string) bool {
	return strings.ContainsAny(s, "*?[")
}
