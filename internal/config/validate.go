package config

import (
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// ErrInvalid is matched (errors.Is) by every *ValidationError.
var ErrInvalid = errors.New("invalid configuration")

// Problem is one validation finding: Field is the config key path (for
// example "roots[1].path") and Message says what is wrong with it.
type Problem struct {
	Field   string
	Message string
}

// ValidationError lists every problem found by Config.Validate, in a
// deterministic order (section order, then index/key order), so a user can fix
// a configuration in one pass.
type ValidationError struct {
	Problems []Problem
}

// Error renders one line per problem. Field names and messages can quote
// user-controlled text (a root path, a config value), so control characters
// are escaped here: the message is deliberately multi-line, and a newline in
// a quoted value must not be able to start a forged line of its own.
func (e *ValidationError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid configuration (%d problem", len(e.Problems))
	if len(e.Problems) != 1 {
		b.WriteString("s")
	}
	b.WriteString(")")
	for _, p := range e.Problems {
		fmt.Fprintf(&b, "\n  %s: %s", escapeControls(p.Field), escapeControls(p.Message))
	}
	return b.String()
}

// escapeControls makes s safe to embed in one line of terminal output. It is
// a small local copy of the idea behind output.Sanitize, which this package
// cannot import (output depends on config).
func escapeControls(s string) string {
	if !strings.ContainsFunc(s, unicode.IsControl) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Is makes errors.Is(err, ErrInvalid) true for validation errors.
func (e *ValidationError) Is(target error) bool { return target == ErrInvalid }

// problemList accumulates problems while validating.
type problemList []Problem

func (p *problemList) add(field, format string, args ...any) {
	*p = append(*p, Problem{Field: field, Message: fmt.Sprintf(format, args...)})
}

// nonNeg records a problem when v is negative.
func (p *problemList) nonNeg(field string, v int64) {
	if v < 0 {
		p.add(field, "must not be negative, got %d", v)
	}
}

// oneOf records a problem when v is not in allowed.
func (p *problemList) oneOf(field, v string, allowed []string) {
	if !slices.Contains(allowed, v) {
		p.add(field, "invalid value %q, expected one of: %s", v, strings.Join(allowed, ", "))
	}
}

// Allowed values. The output formats are defined here rather than imported
// from internal/output to avoid an import cycle; a test pins the list to the
// formats documented in docs/SPEC.md.
var (
	outputFormats = []string{"table", "tree", "json", "ndjson", "plain", "summary"}
	outputColors  = []string{"auto", "always", "never"}
	mergeModes    = []string{string(MergeAncestor), string(MergeAncestorSquash)}
)

var kebabID = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Validate checks cfg for invalid or dangerous values and returns a
// *ValidationError listing every problem, or nil. Nothing is corrected or
// guessed: an invalid configuration is refused.
func (c *Config) Validate() error {
	var p problemList
	if c.Version < 1 || c.Version > CurrentVersion {
		p.add("version", "%s", versionMessage(c.Version, CurrentVersion))
	}
	validateThresholds(&p, "thresholds", c.Thresholds)
	validateGit(&p, c.Git)
	validateDetectors(&p, &c.Detectors)
	validateOutput(&p, c.Output)
	validateScan(&p, c.Scan)
	// Legacy names still load (sweep says which preset runs instead); the
	// error only advertises the current ones.
	if !slices.Contains(LegacyPresetNames(), c.Sweep.Preset) {
		p.oneOf("sweep.preset", c.Sweep.Preset, PresetNames())
	}
	if len(p) == 0 {
		return nil
	}
	return &ValidationError{Problems: p}
}

// versionMessage explains a rejected format version.
func versionMessage(v, current int) string {
	if v > current {
		return fmt.Sprintf("version %d is newer than this Brooom supports (%d); please upgrade Brooom", v, current)
	}
	return fmt.Sprintf("version %d is invalid; the minimum is 1", v)
}

// validGlob checks the syntax of a glob pattern as understood by path.Match
// (a segment "**" is accepted) and rejects empty patterns and control
// characters, which have no legitimate use in patterns or branch names.
func validGlob(pattern string) error {
	if pattern == "" {
		return errors.New("must not be empty")
	}
	if hasControlChars(pattern) {
		return errors.New("must not contain control characters")
	}
	// Exclude matching splits on "/" only and treats a backslash as a glob
	// escape, so `vendor\legacy` would silently match "vendorlegacy" and never
	// the directory the user meant (typically on Windows).
	if strings.Contains(pattern, `\`) {
		return errors.New("use '/' as the path separator (backslash is a glob escape)")
	}
	if _, err := path.Match(pattern, ""); err != nil {
		return fmt.Errorf("invalid glob %q: %w", pattern, err)
	}
	return nil
}

func hasControlChars(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

func validateThresholds(p *problemList, field string, t Thresholds) {
	p.nonNeg(field+".min_age_days", int64(t.MinAgeDays))
	p.nonNeg(field+".min_size_bytes", t.MinSizeBytes)
	p.nonNeg(field+".recent_days", int64(t.RecentDays))
}

func validateGit(p *problemList, g Git) {
	validateGlobList(p, "git.protected_branches", g.ProtectedBranches)
	validateGlobList(p, "git.base_branches", g.BaseBranches)
}

// validateGlobList requires a non-empty list of valid glob strings.
func validateGlobList(p *problemList, field string, list []string) {
	if len(list) == 0 {
		p.add(field, "must not be empty")
		return
	}
	for i, g := range list {
		if err := validGlob(g); err != nil {
			p.add(fmt.Sprintf("%s[%d]", field, i), "%v", err)
		}
	}
}

func validateDetectors(p *problemList, d *Detectors) {
	p.nonNeg("detectors.stale-branch.min_age_days", int64(d.StaleBranch.MinAgeDays))
	p.oneOf("detectors.merged-branch.mode", string(d.MergedBranch.Mode), mergeModes)
	p.nonNeg("detectors.worktrees.min_age_days", int64(d.Worktrees.MinAgeDays))
	validateGitBloat(p, d.GitBloat)
	validateOptionalAge(p, "detectors.ai-artifacts.min_age_days", d.AIArtifacts.MinAgeDays)
	validateCatalog(p, "detectors.ai-artifacts.extra", d.AIArtifacts.Extra)
	validateOptionalAge(p, "detectors.log-and-runtime-files.min_age_days", d.Logs.MinAgeDays)
	validateCatalog(p, "detectors.log-and-runtime-files.extra", d.Logs.Extra)
	p.nonNeg("detectors.build-artifacts.inactive_days", int64(d.BuildArtifacts.InactiveDays))
	validateBuildDirs(p, "detectors.build-artifacts.dirs", d.BuildArtifacts.Dirs)
	validateBuildDirs(p, "detectors.build-artifacts.extra_dirs", d.BuildArtifacts.ExtraDirs)
}

func validateOptionalAge(p *problemList, field string, v *int) {
	if v != nil {
		p.nonNeg(field, int64(*v))
	}
}

func validateGitBloat(p *problemList, g GitBloat) {
	p.nonNeg("detectors.git-bloat.loose_objects_threshold", int64(g.LooseObjectsThreshold))
	p.nonNeg("detectors.git-bloat.pack_count_threshold", int64(g.PackCountThreshold))
	p.nonNeg("detectors.git-bloat.reflog_threshold_bytes", g.ReflogThresholdBytes)
	p.nonNeg("detectors.git-bloat.large_blob_bytes", g.LargeBlobBytes)
	validateGitExpiry(p, "detectors.git-bloat.reflog_expire", g.ReflogExpire)
	validateGitExpiry(p, "detectors.git-bloat.prune_expire", g.PruneExpire)
}

// validateGitExpiry guards values that are passed to git as option values
// (--expire=<v>): a leading "-" or whitespace/control characters could smuggle
// in additional options.
func validateGitExpiry(p *problemList, field, v string) {
	switch {
	case v == "":
		p.add(field, "must not be empty")
	case strings.HasPrefix(v, "-"):
		p.add(field, "%q must not start with \"-\" (it is passed to git as an option value)", v)
	case strings.ContainsFunc(v, func(r rune) bool { return r <= ' ' || r == 0x7f }):
		p.add(field, "%q must not contain whitespace or control characters", v)
	}
}

// validateCatalog checks custom catalog entries.
func validateCatalog(p *problemList, field string, tools []CatalogTool) {
	seen := map[string]bool{}
	for i, t := range tools {
		f := fmt.Sprintf("%s[%d]", field, i)
		switch {
		case !kebabID.MatchString(t.ID):
			p.add(f+".id", "%q must be a kebab-case identifier (a-z, 0-9, single dashes)", t.ID)
		case seen[t.ID]:
			p.add(f+".id", "duplicate id %q", t.ID)
		}
		seen[t.ID] = true
		if strings.TrimSpace(t.Name) == "" {
			p.add(f+".name", "must not be empty")
		}
		if len(t.Project) == 0 && len(t.User) == 0 && len(t.Entries) == 0 {
			p.add(f, "needs at least one location in \"project\", \"user\" or \"entries\"")
		}
		for j, loc := range t.Project {
			if err := validProjectLocation(loc); err != nil {
				p.add(fmt.Sprintf("%s.project[%d]", f, j), "%v", err)
			}
		}
	}
}

// validProjectLocation requires project-relative locations that cannot leave
// the project directory.
func validProjectLocation(loc string) error {
	if loc == "" {
		return errors.New("must not be empty")
	}
	if strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, `\`) || filepath.IsAbs(loc) || filepath.VolumeName(loc) != "" {
		return fmt.Errorf("%q must be relative to the project", loc)
	}
	for _, part := range strings.FieldsFunc(loc, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return fmt.Errorf("%q must not contain \"..\"", loc)
		}
	}
	return nil
}

func validateOutput(p *problemList, o Output) {
	p.oneOf("output.format", o.Format, outputFormats)
	p.oneOf("output.color", o.Color, outputColors)
}

func validateScan(p *problemList, s Scan) {
	p.nonNeg("scan.concurrency", int64(s.Concurrency))
	p.nonNeg("scan.max_depth", int64(s.MaxDepth))
	for i, d := range s.SkipDirs {
		if d == "" || d == "." || d == ".." || strings.ContainsAny(d, `/\`) {
			p.add(fmt.Sprintf("scan.skip_dirs[%d]", i), "%q must be a plain directory name (no path separators, not \".\" or \"..\")", d)
		}
	}
}
