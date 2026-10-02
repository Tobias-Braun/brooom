package buildartifacts

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Tobias-Braun/brooom/internal/catalog"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// foldCase makes directory and marker names compare case-insensitively, as
// the default Windows and macOS filesystems do (Package.json still marks a
// node project there). It is a variable so tests can exercise the folding on
// any OS.
var foldCase = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// strayInstallDir is the one catalog directory that is still reported
// without its marker: a node_modules without a package.json next to it is a
// stray install, which is worth a medium confidence finding. Every other
// name (dist, build, target, ...) is far too common to report blindly.
const strayInstallDir = "node_modules"

// rule is a catalog entry or a configured directory in matching form.
type rule struct {
	id          string
	ecosystem   string
	dir         string
	markers     []string
	all         bool
	limit       findings.Confidence
	requireFile string
	description string
	// stray marks the catalog's node_modules rule (see strayInstallDir).
	stray bool
	// configured is true for entries that come from the user's config.
	configured bool
}

// match is the result of a successful match.
type match struct {
	rule *rule
	// marker is the name of the sibling file that satisfied the marker
	// condition; empty when the rule needs none.
	marker string
	// missing reports a stray install: the directory matched but its marker
	// is absent.
	missing bool
	// parentRel is the directory that holds the artifact (and its markers),
	// relative to the scan root with forward slashes, "" for the root.
	parentRel string
}

// matcher decides which directories are build artifacts. It is the single
// implementation behind both the detector and Claims, so the two cannot
// diverge. It only reads directory listings and never modifies anything.
type matcher struct {
	root  string
	rules []rule

	mu       sync.Mutex
	listings map[string][]string
}

// rulesMemo caches compiled rules per effective dirs/extra_dirs, since a
// workspace scan builds one matcher per target and the rules are read-only
// once compiled. Failed compilations are not cached.
var rulesMemo = struct {
	sync.Mutex
	m map[string][]rule
}{m: map[string][]rule{}}

// compileCalls counts real compilations; tests assert the memo works.
var compileCalls atomic.Int64

// maxMemoRules bounds rulesMemo for long-lived processes.
const maxMemoRules = 64

// newMatcher compiles the embedded catalog and the config lists into a
// matcher for the tree at root.
func newMatcher(root string, cfg config.BuildArtifacts) (*matcher, error) {
	rules, err := cachedRules(cfg)
	if err != nil {
		return nil, err
	}
	return &matcher{root: root, rules: rules, listings: map[string][]string{}}, nil
}

// cachedRules returns the compiled rules for cfg, compiling them once per
// distinct dirs/extra_dirs pair. The returned slice must not be modified.
func cachedRules(cfg config.BuildArtifacts) ([]rule, error) {
	// JSON keeps empty entries and list boundaries distinct, which a plain
	// join would conflate (extra_dirs [""] versus no extra_dirs).
	keyBytes, _ := json.Marshal([2][]string{cfg.Dirs, cfg.ExtraDirs})
	key := string(keyBytes)
	rulesMemo.Lock()
	rules, ok := rulesMemo.m[key]
	rulesMemo.Unlock()
	if ok {
		return rules, nil
	}
	entries, err := catalog.BuildArtifacts()
	if err != nil {
		return nil, err
	}
	compileCalls.Add(1)
	rules, err = compileRules(entries, cfg)
	if err != nil {
		return nil, err
	}
	rulesMemo.Lock()
	defer rulesMemo.Unlock()
	if len(rulesMemo.m) < maxMemoRules {
		rulesMemo.m[key] = rules
	}
	return rules, nil
}

// compileRules applies the config to the catalog. A non-empty Dirs replaces
// the default list: a bare name keeps the catalog's marker rules for that
// name (or, for an unknown name, means "no marker required"), and
// "name:markers" defines a marker-gated rule of its own. ExtraDirs are
// appended and follow the same syntax, except that a bare name is always
// marker-less, because the user wrote it out explicitly.
func compileRules(entries []catalog.BuildArtifactEntry, cfg config.BuildArtifacts) ([]rule, error) {
	var rules []rule
	if len(cfg.Dirs) == 0 {
		for _, e := range entries {
			rules = append(rules, catalogRule(e))
		}
	}
	for _, spec := range cfg.Dirs {
		r, err := rulesForDir(entries, spec)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r...)
	}
	for _, spec := range cfg.ExtraDirs {
		p, err := config.ParseBuildDir(spec)
		if err != nil {
			return nil, fmt.Errorf("build-artifacts: extra_dirs: %w", err)
		}
		rules = append(rules, configuredRule(p))
	}
	return rules, nil
}

// rulesForDir resolves one entry of the dirs list.
func rulesForDir(entries []catalog.BuildArtifactEntry, spec string) ([]rule, error) {
	p, err := config.ParseBuildDir(spec)
	if err != nil {
		return nil, fmt.Errorf("build-artifacts: dirs: %w", err)
	}
	if len(p.Markers) > 0 {
		return []rule{configuredRule(p)}, nil
	}
	var out []rule
	for _, e := range entries {
		if e.Dir == p.Name {
			out = append(out, catalogRule(e))
		}
	}
	if len(out) == 0 {
		out = append(out, configuredRule(p))
	}
	return out, nil
}

func catalogRule(e catalog.BuildArtifactEntry) rule {
	return rule{
		id: e.ID, ecosystem: e.Ecosystem, dir: e.Dir, markers: e.Markers,
		all: e.MarkerMode == catalog.MarkerModeAll, limit: findings.Confidence(e.ConfidenceCap),
		requireFile: e.RequireFileInside, description: e.Description,
		stray: e.Dir == strayInstallDir,
	}
}

func configuredRule(p config.BuildDirSpec) rule {
	return rule{
		id: "custom-" + p.Name, ecosystem: "custom", dir: p.Name, markers: p.Markers,
		limit:       findings.ConfidenceHigh,
		description: "directory configured in detectors.build-artifacts",
		configured:  true,
	}
}

// match reports whether the directory at rel (relative to the root, forward
// slashes) is a build artifact. The first rule whose directory name and
// markers fit wins, so catalog order puts specific rules before generic
// ones. Non-directories never match.
func (m *matcher) match(rel string, isDir bool) (match, bool) {
	if !isDir || rel == "" {
		return match{}, false
	}
	var stray *match
	for i := range m.rules {
		got, ok, isStray := m.try(&m.rules[i], rel)
		if ok {
			return got, true
		}
		if isStray && stray == nil {
			stray = &got
		}
	}
	if stray != nil {
		return *stray, true
	}
	return match{}, false
}

// try checks one rule. It returns the match and whether the rule fully
// matched; when only the marker is missing and the rule allows a stray
// install, the third result is true and the match has missing set.
func (m *matcher) try(r *rule, rel string) (got match, ok, stray bool) {
	parent, ok := anchor(r.dir, rel)
	if !ok || (r.requireFile != "" && !m.hasFileInside(rel, r.requireFile)) {
		return match{}, false, false
	}
	marker, ok := m.markerIn(r, parent)
	if ok {
		return match{rule: r, marker: marker, parentRel: parent}, true, false
	}
	return match{rule: r, missing: true, parentRel: parent}, false, r.stray
}

// anchor matches the rule's directory (one or two segments) against the end
// of rel and returns the directory the first matched segment sits in.
func anchor(dir, rel string) (string, bool) {
	want := strings.Split(dir, "/")
	have := strings.Split(rel, "/")
	if len(have) < len(want) {
		return "", false
	}
	cut := len(have) - len(want)
	for i, w := range want {
		if !globMatch(w, have[cut+i]) {
			return "", false
		}
	}
	return strings.Join(have[:cut], "/"), true
}

// globMatch is path.Match on base names, folded where the filesystem folds.
func globMatch(pattern, name string) bool {
	if foldCase {
		pattern, name = strings.ToLower(pattern), strings.ToLower(name)
	}
	ok, err := path.Match(pattern, name)
	return err == nil && ok
}

// markerIn checks the marker condition of r against the files in the parent
// directory and returns the first matching file name.
func (m *matcher) markerIn(r *rule, parent string) (string, bool) {
	if len(r.markers) == 0 {
		return "", true
	}
	names := m.list(parent)
	first := ""
	for _, marker := range r.markers {
		found := firstMatch(marker, names)
		if found == "" {
			if r.all {
				return "", false
			}
			continue
		}
		if !r.all {
			return found, true
		}
		if first == "" {
			first = found
		}
	}
	return first, first != ""
}

func firstMatch(pattern string, names []string) string {
	for _, n := range names {
		if globMatch(pattern, n) {
			return n
		}
	}
	return ""
}

// list returns the names of the non-directory entries of a directory below
// the root. Listings are cached because several rules (and several
// candidates) look at the same parent; an unreadable directory has no
// markers.
func (m *matcher) list(rel string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if names, ok := m.listings[rel]; ok {
		return names
	}
	var names []string
	dir := filepath.Join(m.root, filepath.FromSlash(rel))
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if !walk.IsDirEntry(dir, e) {
				names = append(names, e.Name())
			}
		}
	}
	m.listings[rel] = names
	return names
}

// hasFileInside reports whether a regular file (not a link) of that name
// exists directly inside the candidate directory.
func (m *matcher) hasFileInside(rel, name string) bool {
	fi, err := os.Lstat(filepath.Join(m.root, filepath.FromSlash(rel), name))
	return err == nil && fi.Mode().IsRegular()
}
