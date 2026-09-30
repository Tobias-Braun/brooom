package gitx

import (
	"strconv"
	"strings"
)

// RefusedBranchName returns why a branch name is too dangerous to hand to
// `git branch` (or to be used as a ref) before git is even asked, or "" when
// the static check passes. Reasons: a leading dash (option injection), a refs/
// prefix (would address another namespace), revision syntax such as @{-1}, "@"
// and "HEAD", and control characters. It is the single definition shared by the
// delete-branch action and by the detectors that suggest it, so a detector
// never offers a deletion the action would refuse at plan time.
func RefusedBranchName(name string) string {
	switch {
	case strings.HasPrefix(name, "-"):
		return "invalid branch name " + strconv.Quote(name) + ": starts with '-'"
	case strings.HasPrefix(name, "refs/"):
		return "invalid branch name " + strconv.Quote(name) + ": must not start with refs/"
	case strings.Contains(name, "@{"), name == "@", name == "HEAD":
		return "invalid branch name " + strconv.Quote(name)
	case strings.ContainsAny(name, "\x00\n"):
		return "invalid branch name: contains control characters"
	}
	return ""
}
