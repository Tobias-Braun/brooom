package gitx

import (
	"context"
	"strings"
)

// Limits for one `git ls-files` call of TrackedUnder. The byte budget stays
// far below the 32 KiB command line limit of Windows, the count keeps the
// pathspec matching of git cheap.
const (
	trackedChunkPaths = 400
	trackedChunkBytes = 16 * 1024
)

// TrackedUnder answers, for every entry of rels, whether git tracks the entry
// itself or anything below it. dir is the repository root and each rel a
// normalised slash-separated path relative to it: no leading or trailing
// slash, no "./" or ".." elements ("." stands for the whole repository).
// Callers pass what filepath.Rel produced; the matching does not clean rels,
// so an unnormalised entry would be reported untracked. The result is keyed by
// the rel exactly as passed.
//
// It replaces one `git ls-files` per candidate, which cost one process per
// finding and scaled with the size of the index each time: the candidates go
// to git as literal pathspecs in a few chunks, and the tracked files git
// reports are matched back to the candidates in Go. Two cases cannot be
// matched safely in Go and are handled otherwise:
//
//   - Case: `core.ignorecase` is read once, and only when it is set are names
//     compared ignoring case (an over-reported entry only blocks a removal,
//     whereas a missed one would allow it). On case-sensitive repositories
//     names are compared exactly.
//   - Non-ASCII names: git normalises pathspecs (core.precomposeunicode on
//     macOS), so an NFD name found on disk still matches the NFC index entry.
//     A byte-wise comparison in Go would miss that and fail open, so every
//     candidate containing a non-ASCII byte is asked with its own
//     `git ls-files` and git does the matching.
//
// Any git failure returns the error and no map, so callers treat every entry
// as unknown (never as untracked).
func TrackedUnder(ctx context.Context, r Runner, dir string, rels []string) (map[string]bool, error) {
	tracked := make(map[string]bool, len(rels))
	var batch []string
	var unicode []string
	for _, rel := range rels {
		tracked[rel] = false
		if isASCII(rel) {
			batch = append(batch, rel)
		} else {
			unicode = append(unicode, rel)
		}
	}
	for _, rel := range unicode {
		out, err := r.Run(ctx, dir, "ls-files", "-z", "--", ":(literal)"+rel)
		if err != nil {
			return nil, err
		}
		tracked[rel] = out != ""
	}
	if len(batch) == 0 {
		return tracked, nil
	}
	fold := ignoresCase(ctx, r, dir)
	byKey := make(map[string][]string, len(batch))
	for _, rel := range batch {
		k := rel
		if fold {
			k = strings.ToLower(rel)
		}
		byKey[k] = append(byKey[k], rel)
	}
	for _, chunk := range trackedChunks(batch) {
		args := []string{"ls-files", "-z", "--"}
		for _, rel := range chunk {
			args = append(args, ":(literal)"+rel)
		}
		out, err := r.Run(ctx, dir, args...)
		if err != nil {
			return nil, err
		}
		attributeTracked(out, byKey, tracked, fold)
	}
	return tracked, nil
}

// isASCII reports whether s consists of ASCII bytes only.
func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// ignoresCase reads core.ignorecase once. Unset means false (--default); any
// failure or unexpected value folds case, the safe side.
func ignoresCase(ctx context.Context, r Runner, dir string) bool {
	out, err := r.Run(ctx, dir, "config", "--type=bool", "--default", "false", "--get", "core.ignorecase")
	if err != nil {
		return true
	}
	return strings.TrimSpace(out) != "false"
}

// trackedChunks splits rels into groups within the pathspec limits.
func trackedChunks(rels []string) [][]string {
	var chunks [][]string
	var cur []string
	size := 0
	for _, rel := range rels {
		if len(cur) > 0 && (len(cur) >= trackedChunkPaths || size+len(rel) > trackedChunkBytes) {
			chunks = append(chunks, cur)
			cur, size = nil, 0
		}
		cur = append(cur, rel)
		size += len(rel) + len(":(literal)") + 1
	}
	if len(cur) > 0 {
		chunks = append(chunks, cur)
	}
	return chunks
}

// attributeTracked credits every listed file to the candidates that equal it
// or contain it, by looking up the file's path and each of its ancestors.
func attributeTracked(out string, byKey map[string][]string, tracked map[string]bool, fold bool) {
	flag := func(k string) {
		for _, rel := range byKey[k] {
			tracked[rel] = true
		}
	}
	for _, file := range strings.Split(out, "\x00") {
		if file == "" {
			continue
		}
		if fold {
			file = strings.ToLower(file)
		}
		flag(file)
		flag(".")
		for i := 0; i < len(file); i++ {
			if file[i] == '/' {
				flag(file[:i])
			}
		}
	}
}
