package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// Editor helper: the test binary re-invokes itself as a fake editor so no
// shell script is needed (works the same on Windows). The last argument is
// the file to edit.
const (
	helperEnv     = "BROOOM_TEST_EDITOR"
	helperWrite   = "BROOOM_TEST_EDITOR_WRITE"
	helperExit    = "BROOOM_TEST_EDITOR_EXIT"
	helperArgsOut = "BROOOM_TEST_EDITOR_ARGS"
)

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		os.Exit(fakeEditor())
	}
	os.Exit(m.Run())
}

func fakeEditor() int {
	file := os.Args[len(os.Args)-1]
	if out := os.Getenv(helperArgsOut); out != "" {
		_ = os.WriteFile(out, []byte(strings.Join(os.Args[1:len(os.Args)-1], "\n")), 0o600)
	}
	if w, ok := os.LookupEnv(helperWrite); ok {
		if err := os.WriteFile(file, []byte(w), 0o600); err != nil {
			return 3
		}
	}
	code, _ := strconv.Atoi(os.Getenv(helperExit))
	return code
}

// useFakeEditor makes $VISUAL start the test binary as the editor.
func useFakeEditor(t *testing.T, content *string, exit int) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(helperEnv, "1")
	t.Setenv("EDITOR", "")
	t.Setenv("VISUAL", `"`+exe+`"`)
	t.Setenv(helperExit, strconv.Itoa(exit))
	if content != nil {
		t.Setenv(helperWrite, *content)
	}
}

func TestConfigPath(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	code, out, _ := run(t, "config", "path")
	if code != ExitOK || strings.TrimSpace(out) != cfg {
		t.Fatalf("path = %q, want %q", out, cfg)
	}
	if _, err := os.Stat(cfg); !os.IsNotExist(err) {
		t.Error("config path must not create the file")
	}
	code, out, _ = run(t, "--config", "/x/y.json", "config", "path")
	if code != ExitOK || strings.TrimSpace(out) != "/x/y.json" {
		t.Fatalf("override path = %q", out)
	}
}

func TestConfigInit(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	code, out, errOut := run(t, "config", "init")
	if code != ExitOK || !strings.Contains(out, cfg) {
		t.Fatalf("code=%d out=%q err=%q", code, out, errOut)
	}
	// The full defaults document, not just {"version":1}.
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFile(t, cfg)), &doc); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "roots", "thresholds", "git", "detectors", "trash", "output", "scan"} {
		if _, ok := doc[k]; !ok {
			t.Errorf("written file lacks %q", k)
		}
	}

	writeFile(t, cfg, `{"version":1,"update_check":true}`)
	code, _, errOut = run(t, "config", "init")
	if code != ExitUsage || !strings.Contains(errOut, "--force") {
		t.Fatalf("second init: code=%d err=%q", code, errOut)
	}
	if !strings.Contains(readFile(t, cfg), "update_check") {
		t.Error("existing file must not be overwritten without --force")
	}
	if code, _, e := run(t, "config", "init", "--force"); code != ExitOK {
		t.Fatalf("force: %s", e)
	}
	if strings.Contains(readFile(t, cfg), `"update_check": true`) || !strings.Contains(readFile(t, cfg), `"thresholds"`) {
		t.Error("--force must rewrite the defaults")
	}
}

func TestConfigInitCreatesDirAndHonoursConfigFlag(t *testing.T) {
	rootsEnv(t)
	target := filepath.Join(t.TempDir(), "a", "b", "c.json")
	if code, _, e := run(t, "--config", target, "config", "init"); code != ExitOK {
		t.Fatal(e)
	}
	if _, err := config.Load(target); err != nil {
		t.Fatal(err)
	}
}

func TestConfigShow(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	writeFile(t, cfg, `{"version":1,"detectors":{"stale-branch":{"min_age_days":45}},"git":{"protected_branches":["main","rel/*"]}}`)

	for _, args := range [][]string{{"config", "show"}, {"config", "show", "--format", "json"}} {
		code, out, errOut := run(t, args...)
		if code != ExitOK {
			t.Fatalf("%v: %s", args, errOut)
		}
		var got config.Config
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
		if got.Detectors.StaleBranch.MinAgeDays != 45 || got.Thresholds != config.Default().Thresholds {
			t.Errorf("%v: not merged effective config: %+v", args, got.Detectors.StaleBranch)
		}
		if !strings.Contains(out, "\n  ") {
			t.Errorf("%v: output not indented", args)
		}
	}

	code, out, _ := run(t, "config", "show", "--format", "table")
	if code != ExitOK {
		t.Fatal("table failed")
	}
	rows := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.Fields(line)
		rows[f[0]] = strings.Join(f[1:], " ")
	}
	if rows["detectors.stale-branch.min_age_days"] != "45" || rows["git.protected_branches"] != `["main","rel/*"]` || rows["version"] != "1" {
		t.Errorf("table rows wrong: %v", rows)
	}
	if code, _, e := run(t, "config", "show", "--format", "plain"); code != ExitUsage || !strings.Contains(e, "plain") {
		t.Errorf("plain: code=%d err=%q", code, e)
	}
}

func TestConfigShowInvalid(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	writeFile(t, cfg, `{"version":1,"scan":{"max_depth":-1},"trash":{"strategy":"shred"}}`)
	code, _, errOut := run(t, "config", "show")
	if code != ExitError || !strings.Contains(errOut, "scan.max_depth") || !strings.Contains(errOut, "trash.strategy") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
}

func TestConfigValidate(t *testing.T) {
	cfg, _, _ := rootsEnv(t)

	code, out, _ := run(t, "config", "validate")
	if code != ExitOK || !strings.Contains(out, "ok") || !strings.Contains(out, "defaults apply") {
		t.Fatalf("missing file: code=%d out=%q", code, out)
	}

	writeFile(t, cfg, `{"version":1}`)
	code, out, _ = run(t, "config", "validate")
	if code != ExitOK || strings.TrimSpace(out) != "ok" {
		t.Fatalf("valid: code=%d out=%q", code, out)
	}

	writeFile(t, cfg, `{"version":1,"scan":{"max_depth":-1},"trash":{"strategy":"shred"},"roots":[{"path":"relative"}]}`)
	code, _, errOut := run(t, "config", "validate")
	if code != ExitError {
		t.Fatalf("code = %d", code)
	}
	lines := 0
	for _, want := range []string{"scan.max_depth", "trash.strategy", "roots[0].path"} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr lacks %q:\n%s", want, errOut)
		}
	}
	for _, l := range strings.Split(errOut, "\n") {
		if strings.Contains(l, cfg+": ") {
			lines++
		}
	}
	if lines != 3 {
		t.Errorf("want 3 problem lines, got %d:\n%s", lines, errOut)
	}

	writeFile(t, cfg, `{`)
	if code, _, _ := run(t, "config", "validate"); code != ExitError {
		t.Errorf("syntax error: code=%d", code)
	}
}

func TestConfigEdit(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	valid := `{"version":1,"update_check":true}`
	useFakeEditor(t, &valid, 0)
	code, _, errOut := run(t, "config", "edit")
	if code != ExitOK {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if readFile(t, cfg) != valid {
		t.Errorf("file = %q", readFile(t, cfg))
	}
}

func TestConfigEditCreatesDefaultsFirst(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	useFakeEditor(t, nil, 0) // editor leaves the file as it finds it
	if code, _, e := run(t, "config", "edit"); code != ExitOK {
		t.Fatal(e)
	}
	if !strings.Contains(readFile(t, cfg), `"thresholds"`) {
		t.Error("file must be created with the full defaults before the editor starts")
	}
}

func TestConfigEditInvalidKeepsFile(t *testing.T) {
	cfg, _, _ := rootsEnv(t)
	bad := `{"version":1,"scan":{"max_depth":-5},"trash":{"strategy":"shred"}}`
	useFakeEditor(t, &bad, 0)
	code, _, errOut := run(t, "config", "edit")
	if code != ExitError || !strings.Contains(errOut, "scan.max_depth") || !strings.Contains(errOut, "trash.strategy") {
		t.Fatalf("code=%d err=%q", code, errOut)
	}
	if readFile(t, cfg) != bad {
		t.Error("edited file must be left as the user saved it")
	}
}

func TestConfigEditEditorFailures(t *testing.T) {
	rootsEnv(t)
	useFakeEditor(t, nil, 7)
	code, _, errOut := run(t, "config", "edit")
	if code != ExitError || !strings.Contains(errOut, "editor") || !strings.Contains(errOut, "exit status 7") {
		t.Fatalf("nonzero exit: code=%d err=%q", code, errOut)
	}
	t.Setenv("VISUAL", filepath.Join(t.TempDir(), "no-such-editor"))
	code, _, errOut = run(t, "config", "edit")
	if code != ExitError || !strings.Contains(errOut, "no-such-editor") {
		t.Fatalf("missing editor: code=%d err=%q", code, errOut)
	}
}

func TestConfigEditPassesEditorArguments(t *testing.T) {
	rootsEnv(t)
	useFakeEditor(t, nil, 0)
	exe, _ := os.Executable()
	argsFile := filepath.Join(t.TempDir(), "args")
	t.Setenv(helperArgsOut, argsFile)
	t.Setenv("VISUAL", `"`+exe+`" --wait "two words"`)
	if code, _, e := run(t, "config", "edit"); code != ExitOK {
		t.Fatal(e)
	}
	if got := readFile(t, argsFile); got != "--wait\ntwo words" {
		t.Errorf("editor args = %q", got)
	}
}

func TestConfigEditConfigFlag(t *testing.T) {
	def, _, _ := rootsEnv(t)
	target := filepath.Join(t.TempDir(), "other.json")
	valid := `{"version":1}`
	useFakeEditor(t, &valid, 0)
	if code, _, e := run(t, "--config", target, "config", "edit"); code != ExitOK {
		t.Fatal(e)
	}
	if readFile(t, target) != valid {
		t.Error("--config file not edited")
	}
	if _, err := os.Stat(def); !os.IsNotExist(err) {
		t.Error("default config must not be touched")
	}
}

func TestEditorCommand(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		goos string
		want []string
	}{
		{"visual wins", map[string]string{"VISUAL": "code --wait", "EDITOR": "nano"}, "linux", []string{"code", "--wait"}},
		{"editor", map[string]string{"EDITOR": "nano"}, "linux", []string{"nano"}},
		{"blank visual falls through", map[string]string{"VISUAL": "  ", "EDITOR": "nano"}, "darwin", []string{"nano"}},
		{"default unix", nil, "linux", []string{"vi"}},
		{"default darwin", nil, "darwin", []string{"vi"}},
		{"default windows", nil, "windows", []string{"notepad"}},
		{"double quotes", map[string]string{"EDITOR": `"/opt/my ed/ed" -w`}, "linux", []string{"/opt/my ed/ed", "-w"}},
		{"single quotes", map[string]string{"EDITOR": `'/opt/my ed/ed' 'a b'`}, "linux", []string{"/opt/my ed/ed", "a b"}},
		{"backslash escape", map[string]string{"EDITOR": `my\ ed -x`}, "linux", []string{"my ed", "-x"}},
		{"windows keeps backslashes", map[string]string{"EDITOR": `"C:\Program Files\Ed\ed.exe" --wait`}, "windows", []string{`C:\Program Files\Ed\ed.exe`, "--wait"}},
		{"empty quoted arg", map[string]string{"EDITOR": `ed ""`}, "linux", []string{"ed", ""}},
		{"unterminated quote", map[string]string{"EDITOR": `ed "a b`}, "linux", []string{"ed", "a b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := editorCommand(func(k string) string { return tt.env[k] }, tt.goos)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("editorCommand = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFlattenRendersLeaves(t *testing.T) {
	var rows [][2]string
	flatten("", map[string]any{"a": map[string]any{"b": json.Number("1"), "c": map[string]any{}}, "s": "x y"}, &rows)
	got := fmt.Sprint(len(rows))
	if got != "3" {
		t.Fatalf("rows = %v", rows)
	}
}
