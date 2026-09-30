package scope

import (
	"os"
	"strings"
)

// probeCaseInsensitive reports whether the filesystem holding dir treats
// names that differ only in case as the same file.
//
// It flips the case of the last component of dir that has any letters, and
// checks whether that spelling exists and is the same file as the original.
// Nothing is ever created, so the probe is safe on read-only mounts and never
// leaves traces. Whenever the answer is not clear (no component with letters,
// the flipped name cannot be inspected, or it is a different file) the result
// is false, i.e. case-sensitive: that is the stricter choice, because a false
// refusal is safe while a false allowance is not.
//
// The probe looks at the directory that contains the flipped component, so a
// per-directory case setting (ext4 casefold, NTFS case-sensitive directories)
// of dir's own children is not observable; it is documented as a limit.
func probeCaseInsensitive(dir string) bool {
	vol := volumeName(dir)
	comps := splitComponents(dir[len(vol):])
	for i := len(comps) - 1; i >= 0; i-- {
		flipped, ok := flipCase(comps[i])
		if !ok {
			continue
		}
		original, err := os.Lstat(joinComponents(vol, comps[:i+1]))
		if err != nil {
			return false
		}
		alt := append(append([]string(nil), comps[:i]...), flipped)
		other, err := os.Lstat(joinComponents(vol, alt))
		return err == nil && os.SameFile(original, other)
	}
	return false
}

// flipCase returns name with its letters' case changed, or false when the
// name has no letter that has a different case.
func flipCase(name string) (string, bool) {
	for _, conv := range []func(string) string{strings.ToUpper, strings.ToLower} {
		if flipped := conv(name); flipped != name {
			return flipped, true
		}
	}
	// Names without cased letters (digits, CJK) say nothing about folding.
	return "", false
}
