package scope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// maxSymlinkHops bounds how many symlinks one resolution follows. Loops
// (a -> b -> a, self links) exhaust it and are refused instead of hanging.
const maxSymlinkHops = 255

// volumeName returns the Windows volume (drive or UNC share) of p; it is
// always empty on other platforms.
func volumeName(p string) string { return filepath.VolumeName(p) }

// isSep reports whether c separates path components on this platform. On
// Windows both '/' and '\' do; on other platforms a backslash is an ordinary
// file name character and must not be split.
func isSep(c byte) bool { return os.IsPathSeparator(c) }

// splitComponents splits p at separators and drops empty components, which
// makes repeated and trailing separators harmless. "." and ".." are kept
// because their meaning depends on symlinks resolved before them.
func splitComponents(p string) []string {
	var comps []string
	start := 0
	for i := 0; i <= len(p); i++ {
		if i < len(p) && !isSep(p[i]) {
			continue
		}
		if i > start {
			comps = append(comps, p[start:i])
		}
		start = i + 1
	}
	return comps
}

// validateInput rejects paths that must never be interpreted: empty strings
// (which would silently mean the working directory), NUL bytes (which C level
// path APIs truncate), over-long paths and platform specific device paths.
func validateInput(path string) error {
	switch {
	case path == "":
		return errors.New("empty path")
	case strings.ContainsRune(path, 0):
		return errors.New("path contains a NUL byte")
	case len(path) > maxPathLen:
		return fmt.Errorf("path is longer than %d bytes", maxPathLen)
	}
	return checkInput(path)
}

// absComponents makes path absolute without touching it lexically: unlike
// filepath.Abs it does not collapse "..", because "link/.." must be decided
// after "link" has been followed. It returns the volume and the components
// below the volume root.
func absComponents(path string) (string, []string, error) {
	path = normalizeExtended(path)
	vol := volumeName(path)
	rest := path[len(vol):]
	rooted := rest != "" && isSep(rest[0])
	if rooted && vol != "" {
		return vol, splitComponents(rest), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", nil, fmt.Errorf("determine working directory: %w", err)
	}
	cwdVol := volumeName(cwd)
	if rooted {
		// Rooted without a volume: "\dir" on Windows uses the current drive,
		// "/dir" elsewhere has no volume at all.
		return cwdVol, splitComponents(rest), nil
	}
	if vol != "" && !strings.EqualFold(vol, cwdVol) {
		// "D:dir" is relative to the per-drive working directory, which Go
		// cannot query. Refusing is the safe answer.
		return "", nil, fmt.Errorf("drive-relative path on volume %s while working on %s", vol, cwdVol)
	}
	comps := append(splitComponents(cwd[len(cwdVol):]), splitComponents(rest)...)
	return cwdVol, comps, nil
}

// resolver is the state of one resolveFull run.
type resolver struct {
	vol string
	// cur is the resolved existing prefix, symlink free by construction.
	cur string
	// curDir records whether cur is known to be a directory, so ".." below a
	// regular file is refused like the OS does.
	curDir  bool
	pending []string
	hops    int
}

// root returns the root directory of the resolver's volume.
func (r *resolver) root() string { return r.vol + string(os.PathSeparator) }

// resolveFull turns path into an absolute path in which every existing
// component is symlink free. It is deliberately not filepath.EvalSymlinks:
// dangling links must be followed to their (non-existent) target, and the
// non-existent tail of a path must be kept while still refusing ".." in it.
//
// Components are processed left to right with a stack: ".." pops the already
// resolved prefix, which gives the OS semantics for "link/..". Every component
// is Lstat-ed; symlinks are replaced by their target components (relative
// targets resolve against the link's directory) up to maxSymlinkHops.
func resolveFull(path string) (string, error) {
	if err := validateInput(path); err != nil {
		return "", err
	}
	vol, comps, err := absComponents(path)
	if err != nil {
		return "", err
	}
	r := &resolver{vol: vol, pending: comps, curDir: true}
	r.cur = r.root()
	for len(r.pending) > 0 {
		comp := r.pending[0]
		r.pending = r.pending[1:]
		tail, err := r.step(comp)
		if err != nil {
			return "", err
		}
		if tail {
			return r.finishWithTail(comp)
		}
	}
	return normalizeExisting(r.cur), nil
}

// step consumes one component. It reports tail=true when the component does
// not exist, in which case it and the remaining pending components form the
// non-existent tail.
func (r *resolver) step(comp string) (tail bool, err error) {
	switch comp {
	case ".":
		return false, nil
	case "..":
		if !r.curDir {
			return false, fmt.Errorf("%q is not a directory, cannot go up from it", r.cur)
		}
		r.cur = filepath.Dir(r.cur)
		return false, nil
	}
	if err := rejectComponent(comp); err != nil {
		return false, err
	}
	next := filepath.Join(r.cur, comp)
	info, err := os.Lstat(next)
	if errors.Is(err, fs.ErrNotExist) {
		// Unix reports ENOTDIR for "file/child", but Windows reports "not
		// found"; a regular file can never have children, so refuse here
		// instead of treating the rest as a creatable tail.
		if !r.curDir {
			return false, fmt.Errorf("%q is not a directory, cannot resolve %q below it", r.cur, comp)
		}
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect %q: %w", next, err)
	}
	if info.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		followed, err := r.follow(next)
		if err != nil {
			return false, err
		}
		if followed {
			return false, nil
		}
	}
	r.cur = next
	r.curDir = info.IsDir()
	return false, nil
}

// follow replaces the symlink at link by its target. It returns false when
// link is not a link after all (Windows reports other reparse points as
// irregular; those are treated as ordinary entries).
func (r *resolver) follow(link string) (bool, error) {
	target, err := os.Readlink(link)
	if err != nil {
		if info, serr := os.Lstat(link); serr == nil && info.Mode()&fs.ModeSymlink == 0 {
			return false, nil
		}
		return false, fmt.Errorf("read link %q: %w", link, err)
	}
	r.hops++
	if r.hops > maxSymlinkHops {
		return false, fmt.Errorf("too many levels of symbolic links at %q", link)
	}
	if err := checkTarget(link, target); err != nil {
		return false, err
	}
	if err := r.splice(link, normalizeExtended(target)); err != nil {
		return false, err
	}
	return true, nil
}

// checkTarget refuses link targets that cannot be interpreted safely.
func checkTarget(link, target string) error {
	if target == "" {
		return fmt.Errorf("symlink %q has an empty target", link)
	}
	if err := checkInput(target); err != nil {
		return fmt.Errorf("symlink %q: %w", link, err)
	}
	return nil
}

// splice queues the components of a link target in front of the pending ones.
// An absolute target restarts resolution at its root; a relative one continues
// in the directory containing the link (r.cur has not advanced onto it).
func (r *resolver) splice(link, target string) error {
	vol := volumeName(target)
	rest := target[len(vol):]
	rooted := rest != "" && isSep(rest[0])
	if vol != "" && !rooted {
		return fmt.Errorf("symlink %q has drive-relative target %q", link, target)
	}
	if rooted {
		if vol == "" {
			vol = r.vol
		}
		r.vol = vol
		r.cur = r.root()
		r.curDir = true
	}
	r.pending = append(splitComponents(rest), r.pending...)
	return nil
}

// finishWithTail appends the non-existent tail (first, then the pending
// components) to the resolved existing prefix. A ".." in the tail is refused:
// the OS would fail on it, while collapsing it lexically could escape.
func (r *resolver) finishWithTail(first string) (string, error) {
	tail := []string{first}
	for _, comp := range r.pending {
		switch comp {
		case ".":
			continue
		case "..":
			return "", fmt.Errorf("%q: \"..\" after a non-existent component", filepath.Join(append([]string{r.cur}, tail...)...))
		}
		if err := rejectComponent(comp); err != nil {
			return "", err
		}
		tail = append(tail, comp)
	}
	return filepath.Join(append([]string{normalizeExisting(r.cur)}, tail...)...), nil
}

// splitParent splits path into the directory part and the final element for
// ResolveParent. Trailing separators are stripped first. A path that ends in
// ".", ".." or has no final element (a root) is refused.
func splitParent(path string) (dir, base string, err error) {
	vol := volumeName(path)
	rest := path[len(vol):]
	for rest != "" && isSep(rest[len(rest)-1]) {
		rest = rest[:len(rest)-1]
	}
	idx := -1
	for i := len(rest) - 1; i >= 0; i-- {
		if isSep(rest[i]) {
			idx = i
			break
		}
	}
	base = rest[idx+1:]
	switch base {
	case "", ".", "..":
		return "", "", fmt.Errorf("final path element %q cannot be resolved as a parent-relative name", base)
	}
	if err := rejectComponent(base); err != nil {
		return "", "", err
	}
	dir = vol + rest[:idx+1]
	if dir == "" {
		dir = "."
	}
	return dir, base, nil
}

// joinName appends one plain file name to a resolved directory.
func joinName(dir, name string) string {
	return filepath.Join(dir, name)
}

// isFilesystemRoot reports whether p, already cleaned and absolute, is the
// root of a filesystem, drive or UNC share.
func isFilesystemRoot(p string) bool {
	return len(splitComponents(p[len(volumeName(p)):])) == 0
}
