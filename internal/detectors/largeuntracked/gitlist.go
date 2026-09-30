package largeuntracked

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// entry is one path git reported below the repository root.
type entry struct {
	// rel is the repository-relative path with forward slashes and without
	// the trailing slash git prints for directories.
	rel string
	// dir is true for the "dir/" entries of git: collapsed ignored
	// directories and, in the untracked listing, nested repositories.
	dir bool
	// ignored is true for entries of the --ignored listing.
	ignored bool
}

// abs joins the entry to the repository root using OS separators. Git always
// prints forward slashes, whatever the platform.
func (e entry) abs(root string) string {
	return filepath.Join(root, filepath.FromSlash(e.rel))
}

// listOthers asks git for the untracked files of the repository at root
// (ignored=false) or for the ignored ones (ignored=true). It is one git
// process per mode, whatever the number of files.
//
// The ignored listing uses --directory so that a fully ignored directory such
// as node_modules is a single "dir/" entry instead of thousands of files.
// The untracked listing has no --directory: git then prints every untracked
// file individually, and the only "dir/" entries left are nested repositories
// (and linked worktrees), which are separate targets and are dropped by the
// caller.
//
// -z makes the output NUL separated and unquoted, so spaces, quotes,
// non-ASCII characters and newlines in names survive, independent of
// core.quotepath. gitx.Runner trims trailing CR/LF only, and the last record
// ends in a NUL, so a name ending in a newline is not damaged.
func listOthers(ctx context.Context, runner gitx.Runner, root string, ignored bool) ([]entry, error) {
	args := []string{"ls-files", "-z", "--others", "--exclude-standard"}
	if ignored {
		args = []string{"ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory"}
	}
	out, err := runner.Run(ctx, root, args...)
	if err != nil {
		return nil, fmt.Errorf("largeuntracked: list files of %s: %w", root, err)
	}
	return parseEntries(out, ignored), nil
}

// parseEntries splits NUL separated git output into entries. Records that
// are empty or that would leave the repository (absolute or ".." paths, which
// git never prints) are dropped.
func parseEntries(out string, ignored bool) []entry {
	var res []entry
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		isDir := strings.HasSuffix(rec, "/")
		rel := path.Clean(strings.TrimSuffix(rec, "/"))
		if !safeRel(rel) {
			continue
		}
		res = append(res, entry{rel: rel, dir: isDir, ignored: ignored})
	}
	return res
}

// safeRel reports whether rel is a clean relative path that stays inside the
// repository.
func safeRel(rel string) bool {
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") && !path.IsAbs(rel) && !filepath.IsAbs(filepath.FromSlash(rel))
}
