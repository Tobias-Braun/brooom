package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// rootsEnv isolates a test from the real home: BROOOM_HOME, HOME and
// USERPROFILE all point into temp dirs. It returns the config file path, a
// scratch workspace dir and the fake home.
func rootsEnv(t *testing.T) (cfgPath, work, home string) {
	t.Helper()
	brooom := t.TempDir()
	home = t.TempDir()
	work = t.TempDir()
	t.Setenv(config.HomeEnv, brooom)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(brooom, config.ConfigFileName), work, home
}

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loadRoots(t *testing.T, cfgPath string) []string {
	t.Helper()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range cfg.Roots {
		out = append(out, r.Path)
	}
	return out
}

func TestRootsAddBasicAndDedupe(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	a := mkdir(t, work, "a")
	b := mkdir(t, work, "b")

	code, out, errOut := run(t, "roots", "add", a, b)
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(out, "added root "+a) {
		t.Errorf("out = %q", out)
	}
	if got := loadRoots(t, cfg); len(got) != 2 || got[0] != a || got[1] != b {
		t.Fatalf("roots = %v", got)
	}

	code, out, _ = run(t, "roots", "add", a, filepath.Join(a, "..", "a"))
	if code != ExitOK || !strings.Contains(out, "already a workspace root") {
		t.Fatalf("dedupe: code=%d out=%q", code, out)
	}
	if got := loadRoots(t, cfg); len(got) != 2 {
		t.Fatalf("roots after dedupe = %v", got)
	}
}

func TestRootsAddSymlinkAlias(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	real := mkdir(t, work, "real")
	link := filepath.Join(work, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if code, _, e := run(t, "roots", "add", real); code != ExitOK {
		t.Fatal(e)
	}
	code, out, _ := run(t, "roots", "add", link)
	if code != ExitOK || !strings.Contains(out, "already a workspace root") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if got := loadRoots(t, cfg); len(got) != 1 {
		t.Fatalf("roots = %v", got)
	}
}

func TestRootsAddCaseAlias(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skip("case-insensitive comparison only applies to Windows and macOS")
	}
	cfg, work, _ := rootsEnv(t)
	dir := mkdir(t, work, "Proj")
	if code, _, e := run(t, "roots", "add", dir); code != ExitOK {
		t.Fatal(e)
	}
	code, out, _ := run(t, "roots", "add", strings.ToUpper(dir))
	if code != ExitOK || !strings.Contains(out, "already a workspace root") {
		t.Fatalf("code=%d out=%q", code, out)
	}
	if got := loadRoots(t, cfg); len(got) != 1 {
		t.Fatalf("roots = %v", got)
	}
}

func TestRootsAddInvalidIsAllOrNothing(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	good := mkdir(t, work, "good")
	file := filepath.Join(work, "file.txt")
	writeFile(t, file, "x")
	missing := filepath.Join(work, "nope")

	code, _, errOut := run(t, "roots", "add", good, missing, file)
	if code != ExitError {
		t.Fatalf("code = %d", code)
	}
	for _, want := range []string{missing + " does not exist", file + " is not a directory", "nothing was added"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr %q lacks %q", errOut, want)
		}
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatalf("config must not be written, stat err = %v", err)
	}
}

func TestRootsAddRefusesFilesystemRoot(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	root := string(filepath.Separator)
	if runtime.GOOS == "windows" {
		root = filepath.VolumeName(work) + `\`
	}
	code, _, errOut := run(t, "roots", "add", root)
	if code != ExitError || !strings.Contains(errOut, "filesystem root") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Fatal("config must not be written")
	}
}

func TestRootsAddHomeWarns(t *testing.T) {
	cfg, _, home := rootsEnv(t)
	code, _, errOut := run(t, "roots", "add", "~")
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if !strings.Contains(errOut, "is your home directory") || !strings.Contains(errOut, "~/dev") {
		t.Errorf("warning text = %q", errOut)
	}
	if got := loadRoots(t, cfg); len(got) != 1 || got[0] != "~" {
		t.Fatalf("roots = %v (home %s)", got, home)
	}
}

func TestRootsAddStoredForm(t *testing.T) {
	cfg, work, home := rootsEnv(t)
	mkdir(t, home, "dev")
	sub := mkdir(t, work, "sub")
	t.Chdir(work)

	if code, _, e := run(t, "roots", "add", "~/dev", "sub", "./sub/../sub/"); code != ExitOK {
		t.Fatal(e)
	}
	got := loadRoots(t, cfg)
	if len(got) != 2 || got[0] != "~/dev" {
		t.Fatalf("roots = %v", got)
	}
	// A relative argument is stored absolute (compare resolved because the
	// temp dir may sit behind a symlink such as macOS /var).
	if !filepath.IsAbs(got[1]) {
		t.Fatalf("relative root stored as %q", got[1])
	}
	a, _ := filepath.EvalSymlinks(got[1])
	b, _ := filepath.EvalSymlinks(sub)
	if a != b {
		t.Errorf("stored %q does not point to %q", got[1], sub)
	}
}

const pinnedConfig = `{
  "version": 1,
  "detectors": {"stale-branch": {"min_age_days": 90}},
  "roots": [{"path": "%s", "exclude": ["vendor"], "thresholds": {"min_age_days": 5}, "detectors": {"build-artifacts": false}}],
  "update_check": true
}
`

// TestRootsEditPreservesPinnedKeys covers the JSON-level rewrite: keys the
// user set to a default value (min_age_days 90) and per-root settings of
// retained roots must survive add and remove, and the key order must not
// change.
func TestRootsEditPreservesPinnedKeys(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	keep := mkdir(t, work, "keep")
	other := mkdir(t, work, "other")
	writeFile(t, cfg, strings.Replace(pinnedConfig, "%s", strings.ReplaceAll(keep, `\`, `\\`), 1))

	if code, _, e := run(t, "roots", "add", other); code != ExitOK {
		t.Fatal(e)
	}
	checkPinned(t, cfg, keep, 2)

	if code, _, e := run(t, "roots", "remove", other); code != ExitOK {
		t.Fatal(e)
	}
	checkPinned(t, cfg, keep, 1)
}

func checkPinned(t *testing.T, cfg, keep string, nRoots int) {
	t.Helper()
	text := readFile(t, cfg)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `"min_age_days": 90`) {
		t.Errorf("pinned default value lost:\n%s", text)
	}
	iDet, iRoots, iUpd := strings.Index(text, `"detectors"`), strings.Index(text, `"roots"`), strings.Index(text, `"update_check"`)
	if iDet >= iRoots || iRoots >= iUpd {
		t.Errorf("key order changed:\n%s", text)
	}
	loaded, err := config.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Roots) != nRoots {
		t.Fatalf("roots = %d, want %d", len(loaded.Roots), nRoots)
	}
	r := loaded.Roots[0]
	if r.Path != keep || len(r.Exclude) != 1 || r.Thresholds == nil || *r.Thresholds.MinAgeDays != 5 || r.Detectors["build-artifacts"] {
		t.Errorf("per-root settings not preserved: %+v", r)
	}
	if !loaded.UpdateCheck {
		t.Error("update_check lost")
	}
}

func TestRootsAddRejectsBrokenConfig(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	dir := mkdir(t, work, "d")
	broken := `{"version": 1, "bogus": true}`
	writeFile(t, cfg, broken)
	code, _, errOut := run(t, "roots", "add", dir)
	if code != ExitError || !strings.Contains(errOut, "bogus") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if readFile(t, cfg) != broken {
		t.Error("broken config must be left untouched")
	}
}

func TestRootsRemoveModes(t *testing.T) {
	cfg, work, home := rootsEnv(t)
	byStored := mkdir(t, work, "stored")
	byExpanded := mkdir(t, home, "dev")
	real := mkdir(t, work, "real")
	link := filepath.Join(work, "link")
	haveLink := os.Symlink(real, link) == nil
	gone := filepath.Join(work, "gone")
	mkdir(t, gone)

	args := []string{"roots", "add", byStored, "~/dev", gone}
	if haveLink {
		args = append(args, link)
	}
	if code, _, e := run(t, args...); code != ExitOK {
		t.Fatal(e)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	// stored string, expanded path, and (a directory that no longer exists)
	// stored string only.
	if code, _, e := run(t, "roots", "remove", byStored, byExpanded, gone); code != ExitOK {
		t.Fatalf("remove: %s", e)
	}
	if got := loadRoots(t, cfg); haveLink && (len(got) != 1 || got[0] != link) || !haveLink && len(got) != 0 {
		t.Fatalf("roots = %v", got)
	}
	if haveLink {
		// resolved path of the symlink root
		if code, _, e := run(t, "roots", "remove", real); code != ExitOK {
			t.Fatalf("remove by resolved path: %s", e)
		}
		if got := loadRoots(t, cfg); len(got) != 0 {
			t.Fatalf("roots = %v", got)
		}
	}
}

func TestRootsRemoveUnknownListsRoots(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	a := mkdir(t, work, "a")
	if code, _, e := run(t, "roots", "add", a); code != ExitOK {
		t.Fatal(e)
	}
	before := readFile(t, cfg)
	code, _, errOut := run(t, "roots", "remove", a, filepath.Join(work, "zzz"))
	if code != ExitError || !strings.Contains(errOut, "not a configured root") || !strings.Contains(errOut, a) {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if readFile(t, cfg) != before {
		t.Error("nothing may be removed when an argument is unknown")
	}
}

func TestRootsList(t *testing.T) {
	rootsEnv(t)

	code, out, _ := run(t, "roots", "list")
	if code != ExitOK || !strings.Contains(out, "brooom roots add") {
		t.Fatalf("empty hint: code=%d out=%q", code, out)
	}
	code, out, _ = run(t, "roots", "list", "--format", "json")
	if code != ExitOK || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty json = %q", out)
	}

}

func TestRootsListStatuses(t *testing.T) {
	cfg, work, _ := rootsEnv(t)
	dir := mkdir(t, work, "dir")
	file := filepath.Join(work, "f")
	writeFile(t, file, "x")
	missing := filepath.Join(work, "missing")
	body, _ := json.Marshal(map[string]any{"version": 1, "roots": []map[string]string{{"path": dir}, {"path": file}, {"path": missing}}})
	writeFile(t, cfg, string(body))

	_, out, _ := run(t, "roots", "list")
	for _, want := range []string{"exists", "missing", "not a directory"} {
		if !strings.Contains(out, want) {
			t.Errorf("table lacks %q:\n%s", want, out)
		}
	}
	_, out, _ = run(t, "roots", "list", "-f", "plain")
	if strings.TrimSpace(out) != strings.Join([]string{dir, file, missing}, "\n") {
		t.Errorf("plain = %q", out)
	}
	_, out, _ = run(t, "roots", "list", "--format", "json")
	var got []struct {
		Path, Resolved string
		Exists         bool
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil || len(got) != 3 {
		t.Fatalf("json %q: %v", out, err)
	}
	if !got[0].Exists || got[1].Exists || got[2].Exists || got[0].Resolved != filepath.Clean(dir) {
		t.Errorf("json entries = %+v", got)
	}
	code, _, errOut := run(t, "roots", "list", "--format", "tree")
	if code != ExitUsage || !strings.Contains(errOut, "tree") {
		t.Errorf("tree: code=%d err=%q", code, errOut)
	}
}

func TestRootsConfigFlagOverride(t *testing.T) {
	defCfg, work, _ := rootsEnv(t)
	dir := mkdir(t, work, "d")
	custom := filepath.Join(t.TempDir(), "sub", "custom.json")
	if code, _, e := run(t, "--config", custom, "roots", "add", dir); code != ExitOK {
		t.Fatal(e)
	}
	if got := loadRoots(t, custom); len(got) != 1 {
		t.Fatalf("custom roots = %v", got)
	}
	if _, err := os.Stat(defCfg); !os.IsNotExist(err) {
		t.Error("default config must not be touched with --config")
	}
}

func TestConfiguredRootStrings(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	writeFile(t, cfg, `{"version":1,"roots":[{"path":"~/a"},{"path":"~/b"}]}`)
	a := &app{}
	got := a.configuredRootStrings()
	if len(got) != 2 || got[0] != "~/a" || got[1] != "~/b" {
		t.Fatalf("got %v", got)
	}
	writeFile(t, cfg, `{`)
	if got := a.configuredRootStrings(); got != nil {
		t.Fatalf("broken config must yield nothing, got %v", got)
	}
}
