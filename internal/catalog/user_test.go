package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// userFixture exercises every variable, wildcard and wildcard-free patterns,
// OS filters and user protect rules.
const userFixture = `{"schema_version":1,"tools":[
 {"id":"tool","name":"T","category":"logs","entries":[
   {"scope":"user","patterns":["~/.tool/todos"],"kind":"file","confidence":"medium","description":"todos"},
   {"scope":"user","patterns":["$XDG_CACHE_HOME/tool/logs","$XDG_DATA_HOME/tool/state","$XDG_CONFIG_HOME/tool/tmp"],"kind":"file","confidence":"high","description":"xdg"},
   {"scope":"user","patterns":["~/Library/Logs/tool/*.log"],"os":["darwin"],"kind":"file","confidence":"high","description":"mac logs"},
   {"scope":"user","patterns":["%LOCALAPPDATA%/tool/Logs/**/*.log","%APPDATA%/tool/cache"],"os":["windows"],"kind":"file","confidence":"high","description":"win"},
   {"scope":"user","patterns":["~/Library/Caches/lib-only"],"kind":"dir","confidence":"high","description":"library path on any OS"},
   {"scope":"project","patterns":["x"],"kind":"file","confidence":"low","description":"project entry is ignored here"}
 ],"protect":[
   {"scope":"user","patterns":["~/.tool/settings.json","~/.tool/skills","~/.tool/keep-*.md"],"reason":"config"},
   {"scope":"user","patterns":["%APPDATA%/tool/config"],"os":["windows"],"reason":"win config"}
 ]},
 {"id":"other","name":"O","category":"cache","entries":[
   {"scope":"user","patterns":["~/.other/cache"],"kind":"dir","confidence":"high","description":"other"}
 ]}
]}`

// userMachine is a throwaway home and env; every directory the fixture might
// expand to is created so that existence is not what filters results.
type userMachine struct {
	home, xdgCache, xdgData, xdgConfig, localAppData, appData string
}

func newUserMachine(t *testing.T) userMachine {
	t.Helper()
	root := t.TempDir()
	m := userMachine{
		home:         filepath.Join(root, "home"),
		xdgCache:     filepath.Join(root, "xdg-cache"),
		xdgData:      filepath.Join(root, "xdg-data"),
		xdgConfig:    filepath.Join(root, "xdg-config"),
		localAppData: filepath.Join(root, "local"),
		appData:      filepath.Join(root, "roaming"),
	}
	for _, d := range []string{
		filepath.Join(m.home, ".tool", "todos"), filepath.Join(m.home, "Library", "Logs", "tool"),
		filepath.Join(m.home, "Library", "Caches", "lib-only"), filepath.Join(m.home, ".other", "cache"),
		filepath.Join(m.home, ".cache", "tool", "logs"), filepath.Join(m.home, ".local", "share", "tool", "state"),
		filepath.Join(m.home, ".config", "tool", "tmp"),
		filepath.Join(m.xdgCache, "tool", "logs"), filepath.Join(m.xdgData, "tool", "state"), filepath.Join(m.xdgConfig, "tool", "tmp"),
		filepath.Join(m.localAppData, "tool", "Logs"), filepath.Join(m.appData, "tool", "cache"),
	} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func (m userMachine) env(goos string, withXDG bool) PathEnv {
	vars := map[string]string{"LOCALAPPDATA": m.localAppData, "APPDATA": m.appData}
	if withXDG {
		vars["XDG_CACHE_HOME"], vars["XDG_DATA_HOME"], vars["XDG_CONFIG_HOME"] = m.xdgCache, m.xdgData, m.xdgConfig
	}
	return PathEnv{GOOS: goos, Home: m.home, Getenv: func(k string) string { return vars[k] }}
}

func userCatalog(t *testing.T, goos string) *Catalog {
	t.Helper()
	c, err := loadFiles(t, Options{GOOS: goos}, map[string]string{"x.json": userFixture})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// bases returns the Base of each location relative to root, slash separated.
func bases(t *testing.T, locs []UserLocation, root string) []string {
	t.Helper()
	var out []string
	for _, l := range locs {
		rel, err := filepath.Rel(root, l.Base)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, l.ToolID+":"+filepath.ToSlash(rel))
	}
	return out
}

func TestUserLocationsPerOS(t *testing.T) {
	m := newUserMachine(t)
	root := filepath.Dir(m.home)
	tests := []struct {
		name    string
		goos    string
		withXDG bool
		want    []string
	}{
		{"linux with XDG", "linux", true, []string{
			"other:home/.other/cache", "tool:home/.tool/todos", "tool:xdg-cache/tool/logs", "tool:xdg-data/tool/state",
			"tool:xdg-config/tool/tmp", "tool:home/Library/Caches/lib-only"}},
		{"linux XDG fallback", "linux", false, []string{
			"other:home/.other/cache", "tool:home/.tool/todos", "tool:home/.cache/tool/logs", "tool:home/.local/share/tool/state",
			"tool:home/.config/tool/tmp", "tool:home/Library/Caches/lib-only"}},
		{"darwin", "darwin", true, []string{
			"other:home/.other/cache", "tool:home/.tool/todos", "tool:xdg-cache/tool/logs", "tool:xdg-data/tool/state",
			"tool:xdg-config/tool/tmp", "tool:home/Library/Logs/tool", "tool:home/Library/Caches/lib-only"}},
		{"windows", "windows", true, []string{
			"other:home/.other/cache", "tool:home/.tool/todos", "tool:local/tool/Logs", "tool:roaming/tool/cache"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// ~/Library/... is a darwin-only location; the fixture's
			// "library path on any OS" entry proves it is skipped elsewhere.
			locs := userCatalog(t, tt.goos).UserLocations(m.env(tt.goos, tt.withXDG))
			want := append([]string(nil), tt.want...)
			if tt.goos != "darwin" {
				want = removeItem(want, "tool:home/Library/Caches/lib-only")
			}
			got := bases(t, locs, root)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("got  %v\nwant %v", got, want)
			}
		})
	}
}

func removeItem(in []string, item string) []string {
	var out []string
	for _, s := range in {
		if s != item {
			out = append(out, s)
		}
	}
	return out
}

func TestUserLocationBaseAndRel(t *testing.T) {
	m := newUserMachine(t)
	byTool := func(goos, pattern string) UserLocation {
		t.Helper()
		for _, l := range userCatalog(t, goos).UserLocations(m.env(goos, true)) {
			if strings.HasSuffix(filepath.ToSlash(l.Pattern), pattern) {
				return l
			}
		}
		t.Fatalf("no location for %s on %s", pattern, goos)
		return UserLocation{}
	}
	free := byTool("linux", ".tool/todos")
	if free.Base != filepath.Join(m.home, ".tool", "todos") || free.Rel != "" || free.Pattern != free.Base {
		t.Errorf("wildcard-free location: %+v", free)
	}
	wild := byTool("darwin", "Library/Logs/tool/*.log")
	if wild.Base != filepath.Join(m.home, "Library", "Logs", "tool") || wild.Rel != "*.log" {
		t.Errorf("wildcard location: %+v", wild)
	}
	deep := byTool("windows", "tool/Logs/**/*.log")
	if deep.Base != filepath.Join(m.localAppData, "tool", "Logs") || deep.Rel != "**/*.log" {
		t.Errorf("windows location: %+v", deep)
	}
}

func TestUserLocationMatch(t *testing.T) {
	m := newUserMachine(t)
	locOf := func(goos, suffix string) UserLocation {
		for _, l := range userCatalog(t, goos).UserLocations(m.env(goos, true)) {
			if strings.HasSuffix(filepath.ToSlash(l.Pattern), suffix) {
				return l
			}
		}
		t.Fatalf("no location %s", suffix)
		return UserLocation{}
	}
	todos := locOf("linux", ".tool/todos")
	tests := []struct {
		name  string
		loc   UserLocation
		abs   string
		isDir bool
		want  bool
	}{
		{"base itself never", todos, todos.Base, false, false},
		{"direct child file", todos, filepath.Join(todos.Base, "a.json"), false, true},
		{"direct child dir rejected by kind", todos, filepath.Join(todos.Base, "sub"), true, false},
		{"grandchild not reported", todos, filepath.Join(todos.Base, "sub", "a.json"), false, false},
		{"sibling of base", todos, filepath.Join(m.home, ".tool", "other"), false, false},
		{"prefix sibling", todos, todos.Base + "-x/a", false, false},
	}
	logs := locOf("darwin", "Library/Logs/tool/*.log")
	tests = append(tests, []struct {
		name  string
		loc   UserLocation
		abs   string
		isDir bool
		want  bool
	}{
		{"wildcard match", logs, filepath.Join(logs.Base, "a.log"), false, true},
		{"wildcard folds case on darwin", logs, filepath.Join(logs.Base, "A.LOG"), false, true},
		{"wildcard other extension", logs, filepath.Join(logs.Base, "a.txt"), false, false},
		{"wildcard does not cross dirs", logs, filepath.Join(logs.Base, "s", "a.log"), false, false},
		{"base folds case on darwin", logs, strings.ToUpper(logs.Base) + "/a.log", false, true},
	}...)
	deep := locOf("windows", "tool/Logs/**/*.log")
	name := func(n int) string { return strings.Repeat("d"+string(filepath.Separator), n) + "a.log" }
	tests = append(tests, []struct {
		name  string
		loc   UserLocation
		abs   string
		isDir bool
		want  bool
	}{
		{"double star zero dirs", deep, filepath.Join(deep.Base, "a.log"), false, true},
		{"double star seven dirs", deep, filepath.Join(deep.Base, name(7)), false, true},
		{"double star depth bound", deep, filepath.Join(deep.Base, name(8)), false, false},
	}...)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.loc.Match(tt.abs, tt.isDir); got != tt.want {
				t.Fatalf("Match(%q, dir=%v) = %v, want %v", tt.abs, tt.isDir, got, tt.want)
			}
		})
	}
}

func TestUserLocationsSkipsMissingAndUnreadable(t *testing.T) {
	home := t.TempDir()
	c := userCatalog(t, "linux")
	env := PathEnv{GOOS: "linux", Home: home, Getenv: func(string) string { return "" }}
	if got := c.UserLocations(env); len(got) != 0 {
		t.Fatalf("nothing exists yet, got %v", got)
	}
	dir := filepath.Join(home, ".other", "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := c.UserLocations(env); len(got) != 1 || got[0].ToolID != "other" {
		t.Fatalf("got %+v", got)
	}
	if err := os.Chmod(dir, 0); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o755) }()
	if f, err := os.Open(dir); err == nil {
		_ = f.Close()
		t.Skip("directory permissions are not enforced here (running as root or on Windows)")
	}
	if got := c.UserLocations(env); len(got) != 0 {
		t.Fatalf("unreadable location must be skipped, got %+v", got)
	}
}

func TestUserLocationsNoHomeAndNilGetenv(t *testing.T) {
	c := userCatalog(t, "linux")
	if got := c.UserLocations(PathEnv{GOOS: "linux"}); len(got) != 0 {
		t.Fatalf("no home: got %v", got)
	}
	if got := c.UserLocations(PathEnv{GOOS: "windows", Home: t.TempDir()}); len(got) != 0 {
		t.Fatalf("undefined windows variables must be skipped silently: %v", got)
	}
}

func TestUserLocationsRelativeXDGFallsBack(t *testing.T) {
	m := newUserMachine(t)
	env := PathEnv{GOOS: "linux", Home: m.home, Getenv: func(k string) string {
		if k == "XDG_CACHE_HOME" {
			return "relative/cache"
		}
		return ""
	}}
	got := bases(t, userCatalog(t, "linux").UserLocations(env, CategoryLogs), filepath.Dir(m.home))
	if !containsStr(got, "tool:home/.cache/tool/logs") {
		t.Fatalf("relative XDG value must fall back to ~/.cache: %v", got)
	}
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestUserLocationsCategoryFilter(t *testing.T) {
	m := newUserMachine(t)
	c := userCatalog(t, "linux")
	got := bases(t, c.UserLocations(m.env("linux", true), CategoryCache), filepath.Dir(m.home))
	if len(got) != 1 || got[0] != "other:home/.other/cache" {
		t.Fatalf("got %v", got)
	}
}

func TestHostEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "xc"))
	env := HostEnv()
	if env.GOOS == "" || env.Home != home || env.Getenv("XDG_CACHE_HOME") != filepath.Join(home, "xc") {
		t.Fatalf("HostEnv = %+v", env)
	}
}

func TestUserProtection(t *testing.T) {
	m := newUserMachine(t)
	c := userCatalog(t, "linux")
	p := c.UserProtection(m.env("linux", true))
	tests := []struct {
		abs  string
		want bool
	}{
		{filepath.Join(m.home, ".tool", "settings.json"), true},
		{filepath.Join(m.home, ".tool", "skills"), true},
		{filepath.Join(m.home, ".tool", "skills", "a", "b.md"), true},
		{filepath.Join(m.home, ".tool", "keep-me.md"), true},
		{filepath.Join(m.home, ".tool", "keep-me.md", "x"), true},
		{filepath.Join(m.home, ".tool", "todos", "a.json"), false},
		{filepath.Join(m.home, ".tool", "skills-x"), false},
		{filepath.Join(m.home, ".tool"), false},
		{filepath.Join(m.appData, "tool", "config"), false},
	}
	for _, tt := range tests {
		if got := p.Protected(tt.abs); got != tt.want {
			t.Errorf("Protected(%q) = %v, want %v", tt.abs, got, tt.want)
		}
	}
	if pats := p.Patterns(); len(pats) != 3 || pats[0] != filepath.Join(m.home, ".tool", "settings.json") {
		t.Errorf("Patterns = %v", pats)
	}

	// Windows adds its own protect rule and compares case-insensitively.
	w := userCatalog(t, "windows").UserProtection(m.env("windows", false))
	if !w.Protected(strings.ToUpper(filepath.Join(m.appData, "tool", "config", "x"))) {
		t.Error("windows protect rule must apply case-insensitively")
	}
	if len(w.Patterns()) != 4 {
		t.Errorf("windows Patterns = %v", w.Patterns())
	}
}

// TestSeedDataUserProtection ties the embedded seed data to the user contract:
// claude-code's todos are found while its settings stay protected.
func TestSeedDataUserProtection(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude", "todos"), 0o755); err != nil {
		t.Fatal(err)
	}
	c, err := Load(Options{GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	env := PathEnv{GOOS: "linux", Home: home, Getenv: func(string) string { return "" }}
	locs := c.UserLocations(env, CategoryAI)
	if len(locs) != 1 || locs[0].Base != filepath.Join(home, ".claude", "todos") {
		t.Fatalf("locs = %+v", locs)
	}
	if !c.UserProtection(env).Protected(filepath.Join(home, ".claude", "settings.json")) {
		t.Error("settings.json must be protected")
	}
	if c.UserProtection(env).Protected(filepath.Join(home, ".claude", "todos", "a.json")) {
		t.Error("todos must not be protected")
	}
}
