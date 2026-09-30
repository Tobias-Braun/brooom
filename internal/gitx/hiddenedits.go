package gitx

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// HiddenEdits lists tracked files of the worktree at dir whose local content
// `git status` cannot see because the index says so: files marked
// skip-worktree (a common way to keep local config overrides) and files
// marked assume-unchanged. Both can hold the only copy of the user's edits,
// and neither `git worktree remove` nor a status check notices them.
//
// The tags come from `git ls-files -v`. A skip-worktree file ('S') counts
// when it exists on disk: sparse-checkout deliberately leaves such files
// absent, and an absent file holds nothing to lose. An assume-unchanged file
// (lowercase tag) counts when it exists and its content differs from the
// index entry, since the flag is often set on files that never changed. When
// the comparison cannot be made the file counts: unknown must never read as
// safe. Paths are slash-separated and relative to dir.
func (r *Repo) HiddenEdits(ctx context.Context, dir string) ([]string, error) {
	out, err := r.Runner.Run(ctx, dir, "--no-optional-locks", "ls-files", "-v", "-s", "-z")
	if err != nil {
		return nil, err
	}
	var hidden []string
	for _, rec := range strings.Split(out, "\x00") {
		tag, oid, rel, ok := parseTaggedEntry(rec)
		if !ok {
			continue
		}
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if _, err := os.Lstat(abs); err != nil {
			continue
		}
		if tag == 'S' || r.differsFromIndex(ctx, dir, rel, abs, oid) {
			hidden = append(hidden, rel)
		}
	}
	return hidden, nil
}

// parseTaggedEntry splits one `ls-files -v -s` record, "<tag> <mode> <oid>
// <stage>\t<path>". It reports ok only for the tags that hide edits: 'S'
// (skip-worktree) and lowercase letters (assume-unchanged, or fsmonitor-valid,
// which is the same trust in the index and treated alike).
func parseTaggedEntry(rec string) (tag byte, oid, path string, ok bool) {
	meta, path, found := strings.Cut(rec, "\t")
	fields := strings.Fields(meta)
	if !found || len(fields) < 4 || len(fields[0]) != 1 {
		return 0, "", "", false
	}
	tag = fields[0][0]
	if tag != 'S' && (tag < 'a' || tag > 'z') {
		return 0, "", "", false
	}
	return tag, fields[2], path, true
}

// differsFromIndex hashes the working file the way git would stage it (clean
// filters and line-ending conversion applied via --path) and compares it with
// the index blob. Anything that is not a regular file, and any failure, counts
// as different.
func (r *Repo) differsFromIndex(ctx context.Context, dir, rel, abs, oid string) bool {
	if fi, err := os.Lstat(abs); err != nil || !fi.Mode().IsRegular() {
		return true
	}
	out, err := r.Runner.Run(ctx, dir, "hash-object", "--path="+rel, "--", abs)
	return err != nil || out != oid
}
