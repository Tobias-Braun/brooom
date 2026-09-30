package catalog

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// validEntry is a minimal embedded-data entry that passes validation.
const validEntry = `{"scope":"project","patterns":["*.log"],"kind":"file","confidence":"high","description":"d"}`

func toolJSON(id, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"id":"` + id + `","name":"N","category":"logs","entries":[` + validEntry + `]` + extra + `}`
}

func fileJSON(tools ...string) string {
	return `{"schema_version":1,"tools":[` + strings.Join(tools, ",") + `]}`
}

func loadFiles(t *testing.T, opts Options, files map[string]string) (*Catalog, error) {
	t.Helper()
	fsys := fstest.MapFS{}
	var names []string
	for n, c := range files {
		fsys[n] = &fstest.MapFile{Data: []byte(c)}
		names = append(names, n)
	}
	return loadFrom(fsys, names, opts)
}

// TestEmbeddedDataLoads is the CI gate for every data PR: the embedded files
// must decode strictly, validate, and carry a source URL for every entry.
func TestEmbeddedDataLoads(t *testing.T) {
	c, err := Load(Options{GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tools()) == 0 {
		t.Fatal("embedded catalog is empty")
	}
	for _, tool := range c.Tools() {
		for i, e := range tool.Entries {
			if e.Source == "" {
				t.Errorf("tool %q entries[%d]: embedded entries must cite a source URL", tool.ID, i)
			}
		}
		if len(tool.Protect) == 0 && tool.Category == CategoryAI {
			t.Errorf("AI tool %q must protect its configuration files", tool.ID)
		}
	}
	for _, id := range []string{"claude-code", "npm", "macos"} {
		if _, ok := c.Tool(id); !ok {
			t.Errorf("seed tool %q missing", id)
		}
	}
	if _, ok := c.Tool("nope"); ok {
		t.Error("unknown tool found")
	}
}

func TestLoadDefaultsGOOS(t *testing.T) {
	c, err := Load(Options{})
	if err != nil || c.GOOS() == "" {
		t.Fatalf("GOOS = %q, err = %v", c.GOOS(), err)
	}
}

func TestLoaderIgnoresUnrelatedFiles(t *testing.T) {
	fsys := fstest.MapFS{
		"ai_tools.json":        {Data: []byte(fileJSON(toolJSON("a", "")))},
		"build_artifacts.json": {Data: []byte(`{"schema_version": 7, "artifacts": [{"dir": "node_modules"}]}`)},
	}
	c, err := loadFrom(fsys, []string{"ai_tools.json"}, Options{GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Tools(); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("tools = %+v", got)
	}
}

func TestLoadRejects(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"unknown field", map[string]string{"x.json": `{"schema_version":1,"tools":[],"typo":1}`}, []string{"x.json", "typo"}},
		{"unknown entry field", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"kind"`, `"knd":"file","kind"`, 1)}, []string{"knd"}},
		{"trailing data", map[string]string{"x.json": fileJSON() + `{}`}, []string{"unexpected data"}},
		{"bad json", map[string]string{"x.json": `{`}, []string{"x.json", "decode"}},
		{"schema version", map[string]string{"x.json": `{"schema_version":2,"tools":[]}`}, []string{"schema_version 2"}},
		{"duplicate ids across files", map[string]string{
			"a.json": fileJSON(toolJSON("dup", "")),
			"b.json": fileJSON(toolJSON("dup", "")),
		}, []string{`tool "dup"`, "duplicate tool id"}},
		{"bad id", map[string]string{"x.json": fileJSON(toolJSON("Bad_ID", ""))}, []string{"kebab-case"}},
		{"unknown category", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"logs"`, `"junk"`, 1)}, []string{`unknown category "junk"`}},
		{"unknown scope", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"project"`, `"global"`, 1)}, []string{"entries[0]", `unknown scope "global"`}},
		{"unknown kind", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"file"`, `"socket"`, 1)}, []string{`unknown kind "socket"`}},
		{"kind any", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"file"`, `"any"`, 1)}, []string{`kind "any" is not allowed`}},
		{"unknown confidence", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"high"`, `"certain"`, 1)}, []string{`unknown confidence "certain"`}},
		{"unknown os", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"kind"`, `"os":["beos"],"kind"`, 1)}, []string{`unknown os "beos"`}},
		{"empty patterns", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `["*.log"]`, `[]`, 1)}, []string{"patterns must not be empty"}},
		{"empty pattern", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"*.log"`, `""`, 1)}, []string{"patterns[0]", "must not be empty"}},
		{"invalid glob", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"*.log"`, `"[a"`, 1)}, []string{"unbalanced"}},
		{"negative age", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"kind"`, `"min_age_days":-1,"kind"`, 1)}, []string{"min_age_days must not be negative"}},
		{"absolute project", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"*.log"`, `"/etc/x"`, 1)}, []string{"relative to the project"}},
		{"drive project", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"*.log"`, `"C:/x"`, 1)}, []string{"relative to the project"}},
		{"dotdot project", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"*.log"`, `"a/../b"`, 1)}, []string{`".."`}},
		{"backslash project", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"*.log"`, `"a\\b"`, 1)}, []string{"forward slashes"}},
		{"user without prefix", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"project","patterns":["*.log"]`, `"user","patterns":["/var/log/x"]`, 1)}, []string{"must start with"}},
		{"user tilde user", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"project","patterns":["*.log"]`, `"user","patterns":["~root/x"]`, 1)}, []string{"must start with"}},
		{"user bare prefix", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"project","patterns":["*.log"]`, `"user","patterns":["~"]`, 1)}, []string{"needs a path below"}},
		{"user dotdot", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"project","patterns":["*.log"]`, `"user","patterns":["~/x/../y"]`, 1)}, []string{`".."`}},
		{"user wildcard base", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"project","patterns":["*.log"]`, `"user","patterns":["~/*/x"]`, 1)}, []string{"must not contain wildcards"}},
		{"missing description", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"description":"d"`, `"description":" "`, 1)}, []string{"description must not be empty"}},
		{"bad source", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"description"`, `"source":"ftp://x","description"`, 1)}, []string{"source"}},
		{"bad homepage", map[string]string{"x.json": strings.Replace(fileJSON(toolJSON("a", "")), `"name"`, `"homepage":"nope","name"`, 1)}, []string{"homepage"}},
		{"no entries", map[string]string{"x.json": `{"schema_version":1,"tools":[{"id":"a","name":"N","category":"ai","entries":[]}]}`}, []string{"at least one entry"}},
		{"protect without reason", map[string]string{"x.json": fileJSON(toolJSON("a", `"protect":[{"scope":"project","patterns":["x"],"reason":""}]`))}, []string{"protect[0]", "reason must not be empty"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadFiles(t, Options{GOOS: "linux"}, tt.files)
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("errors.Is(err, ErrInvalid) must hold: %v", err)
			}
			for _, w := range tt.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q lacks %q", err, w)
				}
			}
		})
	}
}

func TestLoadMissingListedFile(t *testing.T) {
	_, err := loadFrom(fstest.MapFS{}, []string{"ai_tools.json"}, Options{GOOS: "linux"})
	if err == nil || !strings.Contains(err.Error(), "ai_tools.json") {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadReportsAllProblemsAtOnce(t *testing.T) {
	bad := strings.NewReplacer(`"logs"`, `"junk"`, `"file"`, `"socket"`).Replace(fileJSON(toolJSON("a", ""), toolJSON("b", "")))
	_, err := loadFiles(t, Options{GOOS: "linux"}, map[string]string{"x.json": bad})
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	if len(verr.Problems) != 4 {
		t.Fatalf("want 4 problems (2 tools x category+kind), got %d: %v", len(verr.Problems), verr.Problems)
	}
	for _, p := range verr.Problems {
		if !strings.HasPrefix(p, "x.json: tool ") {
			t.Errorf("problem does not name file and tool: %q", p)
		}
	}
}

func TestSelfConsistencyCheck(t *testing.T) {
	tests := []struct {
		name    string
		entry   string
		protect string
		scope   string
		bad     bool
	}{
		{"ancestor dir", `~/.claude`, `~/.claude/settings.json`, "user", true},
		{"equal", `~/.claude/skills`, `~/.claude/skills`, "user", true},
		{"sibling", `~/.claude/todos`, `~/.claude/settings.json`, "user", false},
		{"project ancestor", `.claude`, `.claude/settings.json`, "project", true},
		{"slashless entry vs anchored protect", `.claude`, `.claude/commands`, "project", true},
		{"slashless protect any depth", `sub/CLAUDE.md`, `CLAUDE.md`, "project", true},
		{"protect wildcard segment", `.tool/a`, `.tool/*`, "project", true},
		{"protect double star", `.tool`, `.tool/**`, "project", true},
		{"wildcard entry skipped", `.claude/*.log`, `.claude/settings.json`, "project", false},
		{"deeper entry", `.claude/todos/x`, `.claude/todos`, "project", false},
		{"case-insensitive", `.Claude`, `.claude/settings.json`, "project", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := `{"id":"a","name":"N","category":"ai","entries":[{"scope":"` + tt.scope + `","patterns":["` + tt.entry +
				`"],"kind":"dir","confidence":"low","description":"d"}],"protect":[{"scope":"` + tt.scope + `","patterns":["` + tt.protect + `"],"reason":"r"}]}`
			_, err := loadFiles(t, Options{GOOS: "linux"}, map[string]string{"x.json": fileJSON(tool)})
			if tt.bad != (err != nil) {
				t.Fatalf("bad=%v, err=%v", tt.bad, err)
			}
			if tt.bad && !strings.Contains(err.Error(), "protected path") {
				t.Errorf("error should explain the conflict: %v", err)
			}
		})
	}
	// Different scopes never conflict.
	tool := `{"id":"a","name":"N","category":"ai","entries":[{"scope":"project","patterns":["x"],"kind":"dir","confidence":"low","description":"d"}],` +
		`"protect":[{"scope":"user","patterns":["~/x"],"reason":"r"}]}`
	if _, err := loadFiles(t, Options{GOOS: "linux"}, map[string]string{"x.json": fileJSON(tool)}); err != nil {
		t.Fatalf("cross-scope conflict reported: %v", err)
	}
}

func TestToggles(t *testing.T) {
	files := map[string]string{"x.json": fileJSON(toolJSON("a", ""), strings.Replace(toolJSON("b", ""), `"logs"`, `"cache"`, 1))}
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{"defaults enabled", Options{}, []string{"a", "b"}},
		{"tool off", Options{Tools: map[string]bool{"a": false}}, []string{"b"}},
		{"tool explicitly on", Options{Tools: map[string]bool{"a": true}}, []string{"a", "b"}},
		{"category off", Options{Categories: map[string]bool{"cache": false}}, []string{"a"}},
		{"both off", Options{Tools: map[string]bool{"a": false}, Categories: map[string]bool{"cache": false}}, nil},
		{"unknown ids ignored", Options{Tools: map[string]bool{"zzz": false}, Categories: map[string]bool{"zzz": false}}, []string{"a", "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.opts.GOOS = "linux"
			c, err := loadFiles(t, tt.opts, files)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, tool := range c.Tools() {
				got = append(got, tool.ID)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("tools = %v, want %v", got, tt.want)
			}
		})
	}
}

func intp(v int) *int { return &v }

func TestExtras(t *testing.T) {
	base := map[string]string{"x.json": fileJSON(toolJSON("a", `"protect":[{"scope":"project","patterns":["keep.txt"],"reason":"r"}]`))}
	t.Run("shorthand new tool", func(t *testing.T) {
		c, err := loadFiles(t, Options{
			GOOS: "linux", DefaultCategory: CategoryAI,
			Extra: []config.CatalogTool{{ID: "mine", Name: "Mine", Project: []string{".mine/cache"}, User: []string{"~/.mine"}}},
		}, base)
		if err != nil {
			t.Fatal(err)
		}
		tool, ok := c.Tool("mine")
		if !ok || tool.Category != CategoryAI || len(tool.Entries) != 2 {
			t.Fatalf("tool = %+v", tool)
		}
		for _, e := range tool.Entries {
			if e.Kind != KindAny || e.Confidence != ConfidenceMedium || e.Description != "Mine" {
				t.Errorf("shorthand defaults wrong: %+v", e)
			}
		}
	})
	t.Run("entries new tool with own category", func(t *testing.T) {
		c, err := loadFiles(t, Options{
			GOOS: "linux", DefaultCategory: CategoryLogs,
			Extra: []config.CatalogTool{{
				ID: "mine", Name: "Mine", Category: "cache", Homepage: "https://example.com",
				Entries: []config.CatalogEntry{{Scope: "project", Patterns: []string{"*.tmp"}, Description: "tmp", MinAgeDays: intp(0)}},
			}},
		}, base)
		if err != nil {
			t.Fatal(err)
		}
		tool, _ := c.Tool("mine")
		if tool.Category != CategoryCache || tool.Entries[0].Kind != KindAny || *tool.Entries[0].MinAgeDays != 0 {
			t.Fatalf("tool = %+v", tool)
		}
	})
	t.Run("append to embedded tool", func(t *testing.T) {
		c, err := loadFiles(t, Options{
			GOOS: "linux", DefaultCategory: CategoryAI,
			Extra: []config.CatalogTool{{
				ID: "a", Name: "Ignored", Category: "crash", Project: []string{"extra.log"},
				Protect: []config.CatalogProtect{{Scope: "project", Patterns: []string{"more.txt"}, Reason: "r"}},
			}},
		}, base)
		if err != nil {
			t.Fatal(err)
		}
		tool, _ := c.Tool("a")
		if tool.Name != "N" || tool.Category != CategoryLogs {
			t.Errorf("embedded metadata must win: %+v", tool)
		}
		if len(tool.Entries) != 2 || len(tool.Protect) != 2 || tool.Protect[0].Patterns[0] != "keep.txt" {
			t.Errorf("entries/protect not appended with embedded protect first: %+v", tool)
		}
		if _, ok := c.ProjectMatcher().Match("more.txt", false); ok {
			t.Error("appended protect rule must apply")
		}
	})
	t.Run("extra cannot weaken protect", func(t *testing.T) {
		c, err := loadFiles(t, Options{
			GOOS: "linux", DefaultCategory: CategoryAI,
			Extra: []config.CatalogTool{{ID: "a", Name: "a", Project: []string{"keep.*"}}},
		}, base)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := c.ProjectMatcher().Match("keep.txt", false); ok {
			t.Error("an extra entry must not override an embedded protect rule")
		}
	})
	t.Run("extra conflicting with protect fails", func(t *testing.T) {
		_, err := loadFiles(t, Options{
			GOOS: "linux", DefaultCategory: CategoryAI,
			Extra: []config.CatalogTool{{
				ID: "a", Name: "a",
				Entries: []config.CatalogEntry{{Scope: "project", Patterns: []string{"keep.txt"}, Kind: "file", Description: "d"}},
			}},
		}, base)
		if err == nil || !strings.Contains(err.Error(), "protected path") {
			t.Fatalf("err = %v", err)
		}
	})
	failures := []struct {
		name  string
		extra config.CatalogTool
		want  string
	}{
		{"unknown category", config.CatalogTool{ID: "n", Name: "n", Category: "zzz", Project: []string{"a"}}, "unknown category"},
		{"no default category", config.CatalogTool{ID: "n", Name: "n", Project: []string{"a"}}, "unknown category"},
		{"bad entry scope", config.CatalogTool{ID: "n", Name: "n", Category: "ai", Entries: []config.CatalogEntry{{Scope: "x", Patterns: []string{"a"}, Description: "d"}}}, "unknown scope"},
		{"bad glob", config.CatalogTool{ID: "n", Name: "n", Category: "ai", Project: []string{"a**b"}}, "whole path segment"},
		{"entry without description", config.CatalogTool{ID: "n", Name: "n", Category: "ai", Entries: []config.CatalogEntry{{Scope: "project", Patterns: []string{"a"}}}}, "description"},
		{"bad protect", config.CatalogTool{ID: "n", Name: "n", Category: "ai", Project: []string{"a"}, Protect: []config.CatalogProtect{{Scope: "project", Patterns: []string{"a"}}}}, "reason"},
		{"unknown confidence", config.CatalogTool{ID: "n", Name: "n", Category: "ai", Entries: []config.CatalogEntry{{Scope: "project", Patterns: []string{"a"}, Confidence: "sure", Description: "d"}}}, "confidence"},
	}
	for _, tt := range failures {
		t.Run(tt.name, func(t *testing.T) {
			opts := Options{GOOS: "linux", Extra: []config.CatalogTool{tt.extra}}
			if tt.name != "no default category" {
				opts.DefaultCategory = CategoryAI
			}
			_, err := loadFiles(t, opts, base)
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "config extra[0]") {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
	t.Run("duplicate extras", func(t *testing.T) {
		x := config.CatalogTool{ID: "n", Name: "n", Project: []string{"a"}}
		_, err := loadFiles(t, Options{GOOS: "linux", DefaultCategory: CategoryAI, Extra: []config.CatalogTool{x, x}}, base)
		if err == nil || !strings.Contains(err.Error(), "extra[1]") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestExtraJSONRoundTrip checks that both extra shapes survive the config's
// JSON form and load through the catalog.
func TestExtraJSONRoundTrip(t *testing.T) {
	raw := `[{"id":"short","name":"S","project":["a/b"]},
	{"id":"full","name":"F","category":"crash","entries":[{"scope":"user","patterns":["~/.full"],"kind":"dir","confidence":"low","min_age_days":3,"description":"d"}]}]`
	var extras []config.CatalogTool
	if err := json.Unmarshal([]byte(raw), &extras); err != nil {
		t.Fatal(err)
	}
	c, err := loadFiles(t, Options{GOOS: "linux", DefaultCategory: CategoryLogs, Extra: extras}, map[string]string{"x.json": fileJSON(toolJSON("a", ""))})
	if err != nil {
		t.Fatal(err)
	}
	if tool, _ := c.Tool("full"); tool.Category != CategoryCrash || *tool.Entries[0].MinAgeDays != 3 {
		t.Errorf("full = %+v", tool)
	}
}

func TestToolsReturnsCopies(t *testing.T) {
	c, err := loadFiles(t, Options{GOOS: "linux"}, map[string]string{"x.json": fileJSON(toolJSON("a", `"protect":[{"scope":"project","patterns":["k"],"reason":"r"}]`))})
	if err != nil {
		t.Fatal(err)
	}
	tools := c.Tools()
	tools[0].Entries[0].Patterns[0] = "changed"
	tools[0].Protect[0].Patterns[0] = "changed"
	again, _ := c.Tool("a")
	if again.Entries[0].Patterns[0] != "*.log" || again.Protect[0].Patterns[0] != "k" {
		t.Fatal("mutating a returned tool changed the catalog")
	}
}
