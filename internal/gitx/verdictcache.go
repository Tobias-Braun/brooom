package gitx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// verdictVersion invalidates every stored verdict when the detection changes
// in a way the key does not capture (new patch-id rules, other semantics).
const verdictVersion = "1"

// verdictStore is the optional on-disk memo of squash/rebase verdicts. The
// verdict is a pure function of the two commits (the object graph below a
// commit cannot change), so it is keyed by the resolved base and tip shas plus
// everything else the answer depends on: the diff flags and both safety caps.
// It only ever holds definite answers; truncated and failed checks are unknown
// and are recomputed every time.
//
// It is read by scan handles only (Cache.SetVerdictDir). Actions use uncached
// handles without a store, so a damaged or forged file can at worst change what
// a scan reports, never what a deletion is verified against.
type verdictStore struct {
	dir string
}

// verdictFile is the stored form of one verdict.
type verdictFile struct {
	Version string `json:"version"`
	Merged  bool   `json:"merged"`
	Method  string `json:"method,omitempty"`
}

// verdictKey derives the file name for one (base, tip) pair. The diff cap is
// part of it because a lower cap can turn the same pair into a truncated
// (uncacheable) answer.
func verdictKey(base, tip string, diffLimit int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\x00%d\x00%s", verdictVersion,
		strings.Join(diffFlags, " "), base, tip, maxBaseCommits, diffLimit, "squash-rebase")
	return hex.EncodeToString(h.Sum(nil))[:32] + ".json"
}

// get returns a stored verdict. Anything unreadable, foreign or inconsistent
// counts as a miss.
func (s *verdictStore) get(key string) (MergeResult, bool) {
	data, err := os.ReadFile(filepath.Join(s.dir, key))
	if err != nil || len(data) > 1024 {
		return MergeResult{}, false
	}
	var v verdictFile
	if json.Unmarshal(data, &v) != nil || v.Version != verdictVersion {
		return MergeResult{}, false
	}
	switch {
	case !v.Merged && v.Method == "":
		return MergeResult{}, true
	case v.Merged && (v.Method == MethodSquash || v.Method == MethodRebase):
		return MergeResult{Merged: true, Method: v.Method}, true
	}
	return MergeResult{}, false
}

// put stores a definite verdict, best effort: the cache only speeds up later
// scans, so a read-only or full disk must never fail a scan. The write goes
// through a temporary file and a rename so a concurrent reader never sees a
// partial file.
func (s *verdictStore) put(key string, res MergeResult) {
	data, err := json.Marshal(verdictFile{Version: verdictVersion, Merged: res.Merged, Method: res.Method})
	if err != nil || os.MkdirAll(s.dir, 0o700) != nil {
		return
	}
	tmp, err := os.CreateTemp(s.dir, "tmp-*")
	if err != nil {
		return
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(s.dir, key)) != nil {
		_ = os.Remove(tmp.Name())
	}
}

// SetVerdictDir enables the on-disk squash verdict cache in dir for all
// handles this Cache creates afterwards. It must be called before the first
// Repo call; an empty dir keeps the cache off.
func (c *Cache) SetVerdictDir(dir string) {
	if dir == "" {
		return
	}
	c.verdicts = &verdictStore{dir: dir}
}

// behindCounts returns, per full ref, how many commits of base the ref lacks
// (the "behind" half of ahead/behind), for every local and remote-tracking
// branch with one `for-each-ref` instead of one process per branch. It needs
// git 2.41 (%(ahead-behind:)); on older git or any failure it answers nil,
// which callers treat as "unknown" and fall back to the per-branch queries.
func (r *Repo) behindCounts(ctx context.Context, baseSHA string) map[string]int {
	counts, _ := cached(r, &r.behind, baseSHA, func() (map[string]int, error) {
		out, err := r.run(ctx, "for-each-ref", "--format=%(refname)%00%(ahead-behind:"+baseSHA+")",
			"refs/heads/", "refs/remotes/")
		if err != nil {
			return nil, nil //nolint:nilerr // an unavailable batch means unknown, callers fall back
		}
		return parseBehind(out), nil
	})
	return counts
}

// parseBehind reads "refname NUL ahead behind" records; a record that does not
// parse is left out, which only makes that ref fall back to the slow path.
func parseBehind(out string) map[string]int {
	counts := make(map[string]int)
	for _, line := range Lines(out) {
		name, rest, ok := strings.Cut(line, fieldSep)
		fields := strings.Fields(rest)
		if !ok || len(fields) != 2 {
			continue
		}
		if n, err := strconv.Atoi(fields[1]); err == nil && n >= 0 {
			counts[name] = n
		}
	}
	return counts
}
