// Package catalog holds the data driven knowledge of where AI tools and dev
// tooling leave clutter: embedded JSON (data/*.json), its strict loader and
// validator, the merge with user configuration, a small "**" glob matcher and
// the expansion of user-level locations per OS.
//
// The package is pure in the sense of docs/ARCHITECTURE.md principle 3: it
// never modifies anything and touches the filesystem only to check which user
// locations exist. Walking projects and emitting findings is the job of the
// detectors, which honour the contracts documented on ProjectMatcher and
// UserLocation. The format is documented in docs/catalog.md.
package catalog

import (
	"errors"
	"slices"
)

// SchemaVersion is the only catalog file format version understood.
const SchemaVersion = 1

// Category groups tools so that whole groups can be toggled in the config
// (detectors.log-and-runtime-files.categories).
type Category string

// Known categories. CategoryCrash is separate from CategoryLogs because crash
// dumps are diagnostics some users want to keep.
const (
	CategoryAI     Category = "ai"
	CategoryLogs   Category = "logs"
	CategoryCache  Category = "cache"
	CategoryOSJunk Category = "os-junk"
	CategoryCrash  Category = "crash"
	CategoryBuild  Category = "build"
)

// Categories lists every valid category.
func Categories() []Category {
	return []Category{CategoryAI, CategoryLogs, CategoryCache, CategoryOSJunk, CategoryCrash, CategoryBuild}
}

// Scope tells what a pattern is relative to.
type Scope string

// Scopes.
const (
	// ScopeProject patterns are relative to a project root, forward slashes.
	ScopeProject Scope = "project"
	// ScopeUser patterns start with ~ or a per-OS variable.
	ScopeUser Scope = "user"
)

// Kind restricts which filesystem objects an entry matches.
type Kind string

// Kinds. KindAny is only accepted from config extras (the shorthand form),
// never in embedded data, where the author must know what is matched.
const (
	KindFile Kind = "file"
	KindDir  Kind = "dir"
	KindAny  Kind = "any"
)

// Confidence is how sure the catalog is that a match is clutter.
type Confidence string

// Confidence levels.
const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Entry is one group of clutter locations of a tool.
type Entry struct {
	Scope    Scope    `json:"scope"`
	Patterns []string `json:"patterns"`
	// OS restricts the entry to the listed GOOS values; empty means all.
	OS         []string   `json:"os,omitempty"`
	Kind       Kind       `json:"kind"`
	Confidence Confidence `json:"confidence"`
	// MinAgeDays is a pointer so that an explicit 0 ("no age requirement")
	// is distinguishable from unset ("use the detector default").
	MinAgeDays  *int   `json:"min_age_days,omitempty"`
	Description string `json:"description"`
	Source      string `json:"source,omitempty"`
	// Verify optionally names a content check (VerifyCoreDump,
	// VerifyMinidump) that a file match must pass before it is reported:
	// the pattern alone only proves the name, which other files share.
	Verify string `json:"verify,omitempty"`
}

// Protect lists paths that are never clutter, typically the tool's
// configuration (settings, instructions, skills, commands). Protect always
// wins over entries.
type Protect struct {
	Scope    Scope    `json:"scope"`
	Patterns []string `json:"patterns"`
	OS       []string `json:"os,omitempty"`
	Reason   string   `json:"reason"`
}

// Tool is one tool (or one family of files, e.g. "npm") in the catalog.
type Tool struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Category Category  `json:"category"`
	Homepage string    `json:"homepage,omitempty"`
	Entries  []Entry   `json:"entries"`
	Protect  []Protect `json:"protect,omitempty"`
}

// file is the on-disk format of ai_tools.json and dev_tools.json.
type file struct {
	SchemaVersion int    `json:"schema_version"`
	Tools         []Tool `json:"tools"`
}

// Catalog is the merged, validated and toggled set of tools for one OS.
type Catalog struct {
	goos  string
	tools []Tool
}

// GOOS returns the OS the catalog was loaded for; it selects the entries and
// the case sensitivity of matching.
func (c *Catalog) GOOS() string { return c.goos }

// Tools returns the tools sorted by id. The result is a copy.
func (c *Catalog) Tools() []Tool {
	out := make([]Tool, len(c.tools))
	for i, t := range c.tools {
		out[i] = cloneTool(t)
	}
	return out
}

// Tool returns the tool with the given id.
func (c *Catalog) Tool(id string) (Tool, bool) {
	for _, t := range c.tools {
		if t.ID == id {
			return cloneTool(t), true
		}
	}
	return Tool{}, false
}

// selected returns the tools whose category is in cats (all when empty).
func (c *Catalog) selected(cats []Category) []Tool {
	if len(cats) == 0 {
		return c.tools
	}
	var out []Tool
	for _, t := range c.tools {
		if slices.Contains(cats, t.Category) {
			out = append(out, t)
		}
	}
	return out
}

// appliesTo reports whether an os filter admits goos.
func appliesTo(osFilter []string, goos string) bool {
	return len(osFilter) == 0 || slices.Contains(osFilter, goos)
}

// foldsCase reports whether paths on goos compare case-insensitively
// (default Windows and macOS filesystems).
func foldsCase(goos string) bool { return goos == "windows" || goos == "darwin" }

func cloneTool(t Tool) Tool {
	t.Entries = slices.Clone(t.Entries)
	for i, e := range t.Entries {
		e.Patterns, e.OS = slices.Clone(e.Patterns), slices.Clone(e.OS)
		if e.MinAgeDays != nil {
			v := *e.MinAgeDays
			e.MinAgeDays = &v
		}
		t.Entries[i] = e
	}
	t.Protect = slices.Clone(t.Protect)
	for i, p := range t.Protect {
		p.Patterns, p.OS = slices.Clone(p.Patterns), slices.Clone(p.OS)
		t.Protect[i] = p
	}
	return t
}

// ErrInvalid is matched by errors.Is for every validation failure of catalog
// data or of config extras.
var ErrInvalid = errors.New("invalid catalog")
