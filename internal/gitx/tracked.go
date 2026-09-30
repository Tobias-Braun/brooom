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
// slash-separated path relative to it ("." stands for the whole repository).
// The result is keyed by the rel exactly as passed.
//
// It replaces one `git ls-files` per candidate, which cost one process per
// finding and scaled with the size of the index each time: the candidates go
// to git as literal pathspecs in a few chunks, and the tracked files git
// reports are matched back to the candidates in Go. Matching ignores case:
// with `core.ignorecase` git matches pathspecs that way, and an over-reported
// entry only blocks a removal, whereas a missed one would allow it.
//
// Any git failure returns the error and no map, so callers treat every entry
// as unknown (never as untracked).
func TrackedUnder(ctx context.Context, r Runner, dir string, rels []string) (map[string]bool, error) {
	tracked := make(map[string]bool, len(rels))
	byFold := make(map[string][]string, len(rels))
	for _, rel := range rels {
		tracked[rel] = false
		k := strings.ToLower(rel)
		byFold[k] = append(byFold[k], rel)
	}
	for _, chunk := range trackedChunks(rels) {
		args := []string{"ls-files", "-z", "--"}
		for _, rel := range chunk {
			args = append(args, ":(literal)"+rel)
		}
		out, err := r.Run(ctx, dir, args...)
		if err != nil {
			return nil, err
		}
		attributeTracked(out, byFold, tracked)
	}
	return tracked, nil
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
func attributeTracked(out string, byFold map[string][]string, tracked map[string]bool) {
	flag := func(k string) {
		for _, rel := range byFold[k] {
			tracked[rel] = true
		}
	}
	for _, file := range strings.Split(out, "\x00") {
		if file == "" {
			continue
		}
		file = strings.ToLower(file)
		flag(file)
		flag(".")
		for i := 0; i < len(file); i++ {
			if file[i] == '/' {
				flag(file[:i])
			}
		}
	}
}
