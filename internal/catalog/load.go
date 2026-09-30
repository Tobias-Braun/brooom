package catalog

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// dataFS embeds every JSON file under data/. Not all of them use the tool
// format (build_artifacts.json has its own schema and decoder), which is why
// the tools loader reads only the files named in toolFiles.
//
//go:embed data/*.json
var dataFS embed.FS

// toolFiles is the single place that names the files in the tool format.
// Other files under data/ are ignored by the tools loader.
var toolFiles = []string{"ai_tools.json", "dev_tools.json"}

// Options controls how the embedded data is merged with configuration.
type Options struct {
	// GOOS selects entries and case sensitivity; empty means runtime.GOOS.
	GOOS string
	// Extra are custom tools from the config (detectors.*.extra). An extra
	// with the id of an embedded tool appends entries and protect rules to
	// it; the metadata of the embedded tool wins and protect rules can never
	// be removed or weakened.
	Extra []config.CatalogTool
	// Tools enables or disables tools by id; unlisted tools are enabled.
	Tools map[string]bool
	// Categories enables or disables whole categories; unlisted categories
	// are enabled.
	Categories map[string]bool
	// DefaultCategory is the category of extras that declare none: "ai" for
	// the ai-artifacts detector, "logs" for log-and-runtime-files.
	DefaultCategory Category
}

// maxCachedCatalogs bounds the per-process Load memo. Real runs see a handful
// of distinct effective option sets (one per root override); the bound only
// protects a long-lived process from unbounded growth.
const maxCachedCatalogs = 64

// embeddedDecodes counts how often the embedded files were decoded; tests
// assert it stays at one per process.
var embeddedDecodes atomic.Int64

// embeddedTools decodes and validates the embedded tool files once per
// process. The tools slice is shared and must be cloned before any change;
// the problems are the validation errors of the embedded data itself.
var embeddedTools = sync.OnceValues(func() ([]Tool, []string) {
	embeddedDecodes.Add(1)
	sub, err := fs.Sub(dataFS, "data")
	if err != nil {
		return nil, []string{fmt.Sprintf("catalog: embedded data: %v", err)}
	}
	pr := &problemList{}
	tools := decodeToolFiles(pr, sub, toolFiles)
	return tools, pr.list
})

var loadMemo = struct {
	sync.Mutex
	m map[string]*Catalog
}{m: map[string]*Catalog{}}

// Load builds the catalog from the embedded data and the given options.
// Validation problems of the embedded data or of the extras are all reported
// at once as a *ValidationError.
//
// The embedded files are decoded and validated once per process, and the
// result for a given option set is memoized: a workspace scan calls Load once
// per target and detector, which used to dominate its CPU time. A Catalog is
// immutable (its accessors return copies), so sharing it is safe. Failed
// loads are not memoized.
func Load(opts Options) (*Catalog, error) {
	key, keyed := optionsKey(opts)
	if keyed {
		loadMemo.Lock()
		c, ok := loadMemo.m[key]
		loadMemo.Unlock()
		if ok {
			return c, nil
		}
	}
	tools, problems := embeddedTools()
	c, err := buildCatalog(slices.Clone(tools), slices.Clone(problems), opts)
	if err != nil || !keyed {
		return c, err
	}
	loadMemo.Lock()
	defer loadMemo.Unlock()
	if len(loadMemo.m) < maxCachedCatalogs {
		loadMemo.m[key] = c
	}
	return c, nil
}

// optionsKey serializes opts (JSON sorts map keys) for the memo. It reports
// false when the options cannot be serialized, which just disables caching.
func optionsKey(opts Options) (string, bool) {
	data, err := json.Marshal(opts)
	return string(data), err == nil
}

// loadFrom is Load with an injectable file system and file list so that tests
// can prove that unrelated files are ignored. It never uses the memo.
func loadFrom(fsys fs.FS, files []string, opts Options) (*Catalog, error) {
	pr := &problemList{}
	tools := decodeToolFiles(pr, fsys, files)
	return buildCatalog(tools, pr.list, opts)
}

// buildCatalog merges the extras into decoded tools, validates the result and
// applies the toggles. tools is modified and must be owned by the caller.
func buildCatalog(tools []Tool, problems []string, opts Options) (*Catalog, error) {
	pr := &problemList{list: problems}
	tools = mergeExtras(pr, tools, opts)
	for _, t := range tools {
		checkProtectConsistency(pr, fmt.Sprintf("tool %q", t.ID), t)
	}
	if err := pr.err(); err != nil {
		return nil, err
	}
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	tools = applyToggles(tools, opts)
	slices.SortFunc(tools, func(a, b Tool) int { return strings.Compare(a.ID, b.ID) })
	return &Catalog{goos: goos, tools: tools}, nil
}

// decodeToolFiles decodes and validates the listed files and rejects tool ids
// that occur more than once across them.
func decodeToolFiles(pr *problemList, fsys fs.FS, files []string) []Tool {
	var tools []Tool
	seen := map[string]string{}
	for _, name := range files {
		f, err := decodeFile(fsys, name)
		if err != nil {
			pr.add(name, "%v", err)
			continue
		}
		for i, t := range f.Tools {
			label := fmt.Sprintf("%s: tool %q", name, t.ID)
			if t.ID == "" {
				label = fmt.Sprintf("%s: tools[%d]", name, i)
			}
			if first, dup := seen[t.ID]; dup {
				pr.add(label, "duplicate tool id (first defined in %s)", first)
			}
			seen[t.ID] = name
			validateTool(pr, label, t, validateOptions{requireEntries: true})
			tools = append(tools, t)
		}
	}
	return tools
}

// decodeFile strictly decodes one tool file: unknown fields and trailing data
// are errors so that typos in contributed data fail CI.
func decodeFile(fsys fs.FS, name string) (file, error) {
	var f file
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return f, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return f, fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return f, errors.New("decode: unexpected data after the top-level object")
	}
	if f.SchemaVersion != SchemaVersion {
		return f, fmt.Errorf("unsupported schema_version %d (want %d)", f.SchemaVersion, SchemaVersion)
	}
	return f, nil
}

// mergeExtras adds config extras to tools: new ids become new tools, known
// ids get entries and protect rules appended.
func mergeExtras(pr *problemList, tools []Tool, opts Options) []Tool {
	seen := map[string]bool{}
	for i, x := range opts.Extra {
		label := fmt.Sprintf("config extra[%d]: tool %q", i, x.ID)
		if seen[x.ID] {
			pr.add(label, "duplicate tool id")
		}
		seen[x.ID] = true
		idx := slices.IndexFunc(tools, func(t Tool) bool { return t.ID == x.ID })
		extra := extraToTool(x, opts.DefaultCategory)
		if idx < 0 {
			validateTool(pr, label, extra, validateOptions{allowAny: true, requireEntries: true})
			tools = append(tools, extra)
			continue
		}
		// The embedded metadata wins; validating against it keeps a stale
		// category or name in the extra from producing noise.
		extra.Name, extra.Category, extra.Homepage = tools[idx].Name, tools[idx].Category, tools[idx].Homepage
		validateTool(pr, label, extra, validateOptions{allowAny: true})
		merged := tools[idx]
		merged.Entries = append(slices.Clone(merged.Entries), extra.Entries...)
		merged.Protect = append(slices.Clone(merged.Protect), extra.Protect...)
		tools[idx] = merged
	}
	return tools
}

// extraToTool converts a config extra to a catalog tool. The shorthand
// project/user lists become entries of kind "any", and entries without kind or
// confidence get the documented defaults ("any" and "medium").
func extraToTool(x config.CatalogTool, defaultCat Category) Tool {
	t := Tool{ID: x.ID, Name: x.Name, Category: Category(x.Category), Homepage: x.Homepage}
	if t.Category == "" {
		t.Category = defaultCat
	}
	desc := x.Description
	if desc == "" {
		desc = x.Name
	}
	for _, sh := range []struct {
		scope    Scope
		patterns []string
	}{{ScopeProject, x.Project}, {ScopeUser, x.User}} {
		if len(sh.patterns) > 0 {
			t.Entries = append(t.Entries, Entry{
				Scope: sh.scope, Patterns: slices.Clone(sh.patterns),
				Kind: KindAny, Confidence: ConfidenceMedium, Description: desc,
			})
		}
	}
	for _, e := range x.Entries {
		t.Entries = append(t.Entries, entryFromConfig(e))
	}
	for _, p := range x.Protect {
		t.Protect = append(t.Protect, Protect{
			Scope: Scope(p.Scope), Patterns: slices.Clone(p.Patterns), OS: slices.Clone(p.OS), Reason: p.Reason,
		})
	}
	return t
}

func entryFromConfig(e config.CatalogEntry) Entry {
	out := Entry{
		Scope: Scope(e.Scope), Patterns: slices.Clone(e.Patterns), OS: slices.Clone(e.OS),
		Kind: Kind(e.Kind), Confidence: Confidence(e.Confidence),
		Description: e.Description, Source: e.Source,
	}
	if e.MinAgeDays != nil {
		v := *e.MinAgeDays
		out.MinAgeDays = &v
	}
	if out.Kind == "" {
		out.Kind = KindAny
	}
	if out.Confidence == "" {
		out.Confidence = ConfidenceMedium
	}
	return out
}

// applyToggles drops tools switched off by id or by category.
func applyToggles(tools []Tool, opts Options) []Tool {
	var out []Tool
	for _, t := range tools {
		if enabled, ok := opts.Tools[t.ID]; ok && !enabled {
			continue
		}
		if enabled, ok := opts.Categories[string(t.Category)]; ok && !enabled {
			continue
		}
		out = append(out, t)
	}
	return out
}
