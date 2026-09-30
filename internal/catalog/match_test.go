package catalog

import "testing"

// matchFixture has entries in an order that would let an entry win over a
// protect rule if protect were merely "first match".
const matchFixture = `{"schema_version":1,"tools":[
 {"id":"tool","name":"T","category":"ai","entries":[
   {"scope":"project","patterns":[".tool/*.log"],"kind":"file","confidence":"high","description":"logs"},
   {"scope":"project","patterns":[".tool/**"],"kind":"file","confidence":"low","description":"everything"},
   {"scope":"project","patterns":["**/tmp-*"],"kind":"dir","confidence":"medium","description":"tmp dirs"},
   {"scope":"project","patterns":["Thumbs.db"],"os":["windows"],"kind":"file","confidence":"high","description":"win junk"}
 ],"protect":[
   {"scope":"project","patterns":[".tool/settings.json",".tool/skills","NOTES.md"],"reason":"config"}
 ]},
 {"id":"crashy","name":"C","category":"crash","entries":[
   {"scope":"project","patterns":["core"],"kind":"file","confidence":"medium","description":"core dump"},
   {"scope":"project","patterns":["core.*"],"kind":"file","confidence":"medium","description":"core dump"}
 ]}
]}`

func matchCatalog(t *testing.T, goos string) *Catalog {
	t.Helper()
	c, err := loadFiles(t, Options{GOOS: goos}, map[string]string{"x.json": matchFixture})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProjectMatcher(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		cats   []Category
		rel    string
		isDir  bool
		want   bool
		tool   string
		reason string
	}{
		{name: "anchored pattern", goos: "linux", rel: ".tool/run.log", want: true, tool: "tool"},
		{name: "anchored not at depth", goos: "linux", rel: "sub/.tool/run.log", want: false},
		{name: "double star deep", goos: "linux", rel: ".tool/a/b/c.txt", want: true, tool: "tool"},
		{name: "protect beats entries", goos: "linux", rel: ".tool/settings.json", want: false},
		{name: "protected dir", goos: "linux", rel: ".tool/skills", isDir: false, want: false},
		{name: "below protected dir", goos: "linux", rel: ".tool/skills/x/run.log", want: false},
		{name: "protect base name any depth", goos: "linux", rel: "a/b/NOTES.md", want: false},
		{name: "protect vs kind irrelevant", goos: "linux", rel: "NOTES.md", isDir: true, want: false},
		{name: "any depth base name", goos: "linux", rel: "a/b/tmp-1", isDir: true, want: true, tool: "tool"},
		{name: "dir entry does not match file", goos: "linux", rel: "a/tmp-1", isDir: false, want: false},
		{name: "file entry does not match dir", goos: "linux", rel: "core", isDir: true, want: false},
		{name: "core file", goos: "linux", rel: "src/core", want: true, tool: "crashy"},
		{name: "core dot pid", goos: "linux", rel: "core.1234", want: true, tool: "crashy"},
		{name: "os filter excludes", goos: "linux", rel: "Thumbs.db", want: false},
		{name: "os filter includes", goos: "windows", rel: "sub/Thumbs.db", want: true, tool: "tool"},
		{name: "case folding windows", goos: "windows", rel: "SUB/THUMBS.DB", want: true, tool: "tool"},
		{name: "case folding darwin", goos: "darwin", rel: ".TOOL/run.log", want: true, tool: "tool"},
		{name: "case sensitive linux", goos: "linux", rel: ".TOOL/run.log", want: false},
		{name: "protect folds on darwin", goos: "darwin", rel: ".tool/SETTINGS.JSON", want: false},
		{name: "category filter hit", goos: "linux", cats: []Category{CategoryCrash}, rel: "core", want: true, tool: "crashy"},
		{name: "category filter miss", goos: "linux", cats: []Category{CategoryCrash}, rel: ".tool/run.log", want: false},
		{name: "protect applies across categories", goos: "linux", cats: []Category{CategoryCrash}, rel: "NOTES.md", want: false},
		{name: "project root itself", goos: "linux", rel: ".", isDir: true, want: false},
		{name: "escaping path", goos: "linux", rel: "../core", want: false},
		{name: "absolute path", goos: "linux", rel: "/core", want: false},
		{name: "unclean path", goos: "linux", rel: "./a//core", want: true, tool: "crashy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := matchCatalog(t, tt.goos).ProjectMatcher(tt.cats...)
			got, ok := m.Match(tt.rel, tt.isDir)
			if ok != tt.want {
				t.Fatalf("Match(%q, dir=%v) ok = %v, want %v (%+v)", tt.rel, tt.isDir, ok, tt.want, got)
			}
			if ok && got.ToolID != tt.tool {
				t.Errorf("matched tool %q, want %q", got.ToolID, tt.tool)
			}
		})
	}
}

func TestMatchReportsFirstEntry(t *testing.T) {
	got, ok := matchCatalog(t, "linux").ProjectMatcher().Match(".tool/run.log", false)
	if !ok || got.Entry.Confidence != ConfidenceHigh || got.Category != CategoryAI {
		t.Fatalf("got %+v", got)
	}
}

func TestProtected(t *testing.T) {
	m := matchCatalog(t, "linux").ProjectMatcher()
	tests := map[string]bool{
		".tool/settings.json":    true,
		".tool/skills":           true,
		".tool/skills/a/b":       true,
		"deep/NOTES.md":          true,
		"deep/NOTES.md/child":    true,
		".tool/run.log":          false,
		"other":                  false,
		"":                       false,
		"../.tool/settings.json": false,
		".tool/skills-not/x":     false,
	}
	for rel, want := range tests {
		if got := m.Protected(rel); got != want {
			t.Errorf("Protected(%q) = %v, want %v", rel, got, want)
		}
	}
}
