package catalog

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

// ValidationError lists every problem found in catalog data or config extras,
// each naming the file (or config extra), the tool id and the entry index.
type ValidationError struct {
	Problems []string
}

// Error renders one problem per line.
func (e *ValidationError) Error() string {
	return "invalid catalog:\n  - " + strings.Join(e.Problems, "\n  - ")
}

// Is makes errors.Is(err, ErrInvalid) hold.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// problemList collects problems so that a load reports all of them at once.
type problemList struct{ list []string }

func (p *problemList) add(prefix, format string, args ...any) {
	p.list = append(p.list, prefix+": "+fmt.Sprintf(format, args...))
}

func (p *problemList) err() error {
	if len(p.list) == 0 {
		return nil
	}
	return &ValidationError{Problems: p.list}
}

var (
	kebabID   = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	volumeRef = regexp.MustCompile(`^[A-Za-z]:`)

	knownOS          = []string{"linux", "darwin", "windows"}
	knownConfidences = []Confidence{ConfidenceHigh, ConfidenceMedium, ConfidenceLow}

	// userPrefixes are the only allowed starts of a user pattern; anything
	// else could point outside the well-known user locations.
	userPrefixes = []string{"~", "$XDG_CACHE_HOME", "$XDG_DATA_HOME", "$XDG_CONFIG_HOME", "%LOCALAPPDATA%", "%APPDATA%"}
)

// validateOptions tunes validation for the two sources of tools.
type validateOptions struct {
	// allowAny accepts kind "any" (config extras only).
	allowAny bool
	// requireEntries demands at least one entry; false for extras that only
	// append protect rules to an embedded tool.
	requireEntries bool
}

// validateTool checks one tool; label prefixes every problem.
func validateTool(pr *problemList, label string, t Tool, o validateOptions) {
	if !kebabID.MatchString(t.ID) {
		pr.add(label, "id %q must be a kebab-case identifier (a-z, 0-9, single dashes)", t.ID)
	}
	if strings.TrimSpace(t.Name) == "" {
		pr.add(label, "name must not be empty")
	}
	if !slices.Contains(Categories(), t.Category) {
		pr.add(label, "unknown category %q (want one of %v)", t.Category, Categories())
	}
	if t.Homepage != "" {
		if err := validateURL(t.Homepage); err != nil {
			pr.add(label, "homepage: %v", err)
		}
	}
	if o.requireEntries && len(t.Entries) == 0 {
		pr.add(label, "needs at least one entry")
	}
	if t.RepoKey != "" {
		if _, ok := repoEncoders[t.RepoKey]; !ok {
			pr.add(label, "unknown repo_key %q (want one of %v)", t.RepoKey, knownRepoKeys())
		}
	}
	for i, e := range t.Entries {
		validateEntry(pr, fmt.Sprintf("%s: entries[%d]", label, i), e, o)
		for j, pat := range e.Patterns {
			if err := validateRepoPattern(pat, t.RepoKey); err != nil {
				pr.add(fmt.Sprintf("%s: entries[%d]: patterns[%d]", label, i, j), "%v", err)
			}
		}
	}
	for i, p := range t.Protect {
		validateProtect(pr, fmt.Sprintf("%s: protect[%d]", label, i), p)
	}
}

func validateEntry(pr *problemList, label string, e Entry, o validateOptions) {
	validateScopeAndPatterns(pr, label, e.Scope, e.Patterns)
	validateOS(pr, label, e.OS)
	validateKind(pr, label, e.Kind, o.allowAny)
	if !slices.Contains(knownConfidences, e.Confidence) {
		pr.add(label, "unknown confidence %q (want high, medium or low)", e.Confidence)
	}
	if e.MinAgeDays != nil && *e.MinAgeDays < 0 {
		pr.add(label, "min_age_days must not be negative, got %d", *e.MinAgeDays)
	}
	if e.Verify != "" && !slices.Contains(knownVerifiers, e.Verify) {
		pr.add(label, "unknown verify %q (want %s)", e.Verify, strings.Join(knownVerifiers, " or "))
	}
	if strings.TrimSpace(e.Description) == "" {
		pr.add(label, "description must not be empty")
	}
	if e.Source != "" {
		if err := validateURL(e.Source); err != nil {
			pr.add(label, "source: %v", err)
		}
	}
}

func validateKind(pr *problemList, label string, k Kind, allowAny bool) {
	switch k {
	case KindFile, KindDir:
	case KindAny:
		if !allowAny {
			pr.add(label, "kind \"any\" is not allowed in embedded data; use \"file\" or \"dir\"")
		}
	default:
		pr.add(label, "unknown kind %q (want file or dir)", k)
	}
}

func validateProtect(pr *problemList, label string, p Protect) {
	validateScopeAndPatterns(pr, label, p.Scope, p.Patterns)
	validateOS(pr, label, p.OS)
	if strings.TrimSpace(p.Reason) == "" {
		pr.add(label, "reason must not be empty")
	}
}

func validateScopeAndPatterns(pr *problemList, label string, scope Scope, patterns []string) {
	if scope != ScopeProject && scope != ScopeUser {
		pr.add(label, "unknown scope %q (want project or user)", scope)
	}
	if len(patterns) == 0 {
		pr.add(label, "patterns must not be empty")
	}
	for j, pat := range patterns {
		var err error
		switch scope {
		case ScopeProject:
			err = validateProjectPattern(pat)
		case ScopeUser:
			err = validateUserPattern(pat)
		default:
			err = ValidateGlob(pat)
		}
		if err != nil {
			pr.add(fmt.Sprintf("%s: patterns[%d]", label, j), "%v", err)
		}
	}
}

func validateOS(pr *problemList, label string, oses []string) {
	for _, o := range oses {
		if !slices.Contains(knownOS, o) {
			pr.add(label, "unknown os %q (want one of %v)", o, knownOS)
		}
	}
}

func validateURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("%q must be an http(s) URL", s)
	}
	return nil
}

// validateProjectPattern requires a well-formed glob that is relative and
// cannot leave the project directory.
func validateProjectPattern(pat string) error {
	if strings.HasPrefix(pat, "/") || volumeRef.MatchString(pat) {
		return fmt.Errorf("pattern %q must be relative to the project", pat)
	}
	if err := ValidateGlob(pat); err != nil {
		return err
	}
	if strings.Contains(pat, `\`) {
		return fmt.Errorf("pattern %q must use forward slashes", pat)
	}
	if slices.Contains(strings.Split(pat, "/"), "..") {
		return fmt.Errorf("pattern %q must not contain \"..\"", pat)
	}
	return nil
}

// splitUserPrefix splits a user pattern into its leading variable and the
// remainder below it.
func splitUserPrefix(pat string) (prefix, rest string, ok bool) {
	for _, p := range userPrefixes {
		if pat == p {
			return p, "", true
		}
		if strings.HasPrefix(pat, p+"/") {
			return p, pat[len(p)+1:], true
		}
	}
	return "", "", false
}

// validateUserPattern requires a known leading variable and a literal first
// directory below it. The literal directory becomes the Base that is added to
// the scope guard; without it the whole home directory (or %APPDATA%) would
// become an allowed location.
func validateUserPattern(pat string) error {
	if strings.Contains(pat, `\`) {
		return fmt.Errorf("pattern %q must use forward slashes", pat)
	}
	prefix, rest, ok := splitUserPrefix(pat)
	if !ok {
		return fmt.Errorf("pattern %q must start with one of %s", pat, strings.Join(userPrefixes, ", "))
	}
	if rest == "" {
		return fmt.Errorf("pattern %q needs a path below %s", pat, prefix)
	}
	if err := ValidateGlob(rest); err != nil {
		return err
	}
	segs := strings.Split(rest, "/")
	if slices.Contains(segs, "..") {
		return fmt.Errorf("pattern %q must not contain \"..\"", pat)
	}
	if hasWildcard(segs[0]) {
		return fmt.Errorf("pattern %q: the first directory below %s must not contain wildcards", pat, prefix)
	}
	return nil
}

// checkProtectConsistency enforces that no entry pattern is (or contains) a
// protected path of the same tool and scope. Wildcard entries cannot be
// judged statically; for them Protect wins at match time.
func checkProtectConsistency(pr *problemList, label string, t Tool) {
	if t.RepoKey != "" {
		if _, ok := repoEncoders[t.RepoKey]; !ok {
			pr.add(label, "unknown repo_key %q (want one of %v)", t.RepoKey, knownRepoKeys())
		}
	}
	for i, e := range t.Entries {
		for j, p := range t.Protect {
			if e.Scope != p.Scope {
				continue
			}
			for _, ep := range e.Patterns {
				for _, pp := range p.Patterns {
					if entryCoversProtected(ep, pp) {
						pr.add(fmt.Sprintf("%s: entries[%d]", label, i),
							"pattern %q equals or contains protected path %q (protect[%d]); use narrower patterns", ep, pp, j)
					}
				}
			}
		}
	}
}

// entryCoversProtected reports whether the wildcard-free entry pattern equals
// or is an ancestor directory of the protect pattern. A slash-less protect
// pattern matches by base name at any depth, so the entry's last segment is
// compared as well. Comparison ignores case to stay conservative on
// case-insensitive filesystems.
func entryCoversProtected(entry, protect string) bool {
	if hasWildcard(entry) {
		return false
	}
	es, ps := strings.Split(foldCase(entry), "/"), strings.Split(foldCase(protect), "/")
	if len(ps) == 1 && segmentMatches(ps[0], es[len(es)-1]) {
		return true
	}
	if len(es) > len(ps) {
		return false
	}
	for i, e := range es {
		if ps[i] == "**" {
			return true
		}
		if !segmentMatches(ps[i], e) {
			return false
		}
	}
	return true
}

func segmentMatches(pattern, name string) bool {
	return matchSegment([]rune(pattern), []rune(name))
}
