package scope

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestNewGuardRefusals(t *testing.T) {
	tr := newTree(t)
	file := touch(t, tr.root, "plainfile")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"no location", nil, "no allowed location"},
		{"empty location", []string{tr.allowed, ""}, "empty"},
		{"NUL byte", []string{tr.allowed + "\x00x"}, "NUL"},
		{"missing", []string{filepath.Join(tr.root, "missing")}, "missing"},
		{"file", []string{file}, "not a directory"},
		{"filesystem root", []string{volumeRoot(t)}, "filesystem root"},
		{"root with dots", []string{filepath.Join(tr.allowed, strings.Repeat(".."+string(os.PathSeparator), 64))}, "filesystem root"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g, err := NewGuard(tc.args...)
			if err == nil {
				t.Fatalf("NewGuard(%q) = %v, want error", tc.args, g.Allowed())
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestNewGuardNamesMissingLocation(t *testing.T) {
	missing := filepath.Join(testutil.ResolvedTempDir(t), "nope")
	_, err := NewGuard(missing)
	if err == nil || !strings.Contains(err.Error(), missing) || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error %v should name %q and wrap fs.ErrNotExist", err, missing)
	}
}

func TestNewGuardCleansDedupesAndKeepsNested(t *testing.T) {
	tr := newTree(t)
	messy := tr.allowed + string(os.PathSeparator) + "." + string(os.PathSeparator) + string(os.PathSeparator)
	g, err := NewGuard(tr.allowed, messy, tr.in, tr.allowed)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{tr.allowed, tr.in}
	got := g.Allowed()
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Allowed() = %q, want %q", got, want)
	}
	got[0] = "mutated"
	if g.Allowed()[0] != tr.allowed {
		t.Error("Allowed() must return a copy")
	}
}

func TestNewGuardRelativeLocation(t *testing.T) {
	tr := newTree(t)
	t.Chdir(tr.root)
	g, err := NewGuard("allowed")
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Allowed(); len(got) != 1 || got[0] != tr.allowed {
		t.Fatalf("Allowed() = %q, want [%q]", got, tr.allowed)
	}
}

func TestResolveContainment(t *testing.T) {
	tr := newTree(t)
	sep := string(os.PathSeparator)
	tests := []struct {
		name string
		path string
		// want is the expected result; empty means the path must be refused
		// with ErrOutsideScope.
		want string
	}{
		{"allowed root itself", tr.allowed, tr.allowed},
		{"child", filepath.Join(tr.in, "sub", "file"), filepath.Join(tr.in, "sub", "file")},
		{"trailing separator", tr.in + sep, tr.in},
		{"repeated separators", tr.allowed + sep + sep + "in" + sep + sep + "sub", filepath.Join(tr.in, "sub")},
		{"dot components", tr.allowed + sep + "." + sep + "in" + sep + "." + sep + "sub", filepath.Join(tr.in, "sub")},
		{"dotdot staying inside", filepath.Join(tr.in, "sub") + sep + ".." + sep + "sub", filepath.Join(tr.in, "sub")},
		{"prefix sibling", filepath.Join(tr.root, "allowedc"), ""},
		{"prefix sibling child", filepath.Join(tr.root, "allowedc", "x"), ""},
		{"dotted sibling", filepath.Join(tr.root, "allowed.evil"), ""},
		{"dotdot into prefix sibling", tr.allowed + sep + ".." + sep + "allowedc", ""},
		{"dotdot out of root", tr.allowed + sep + ".." + sep + "outside" + sep + "secret", ""},
		{"deep dotdot escape", tr.in + sep + ".." + sep + ".." + sep + ".." + sep + "outside", ""},
		{"parent of root", tr.root, ""},
		{"outside file", filepath.Join(tr.outside, "secret"), ""},
		{"volume root", volumeRoot(t), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tr.guard.Resolve(tc.path)
			if tc.want == "" {
				if !errors.Is(err, ErrOutsideScope) {
					t.Fatalf("Resolve(%q) = %q, %v; want ErrOutsideScope", tc.path, got, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestResolveErrorNamesInputAndResolvedPath(t *testing.T) {
	tr := newTree(t)
	in := tr.allowed + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "outside"
	_, err := tr.guard.Resolve(in)
	if !errors.Is(err, ErrOutsideScope) {
		t.Fatalf("err = %v, want ErrOutsideScope", err)
	}
	// The error quotes paths with %q, which doubles Windows backslashes.
	for _, want := range []string{in, tr.outside} {
		if !strings.Contains(err.Error(), fmt.Sprintf("%q", want)) {
			t.Errorf("error %q should name %q", err, want)
		}
	}
}

func TestResolveInvalidInput(t *testing.T) {
	tr := newTree(t)
	tests := []struct {
		name string
		path string
	}{
		{"empty", ""},
		{"NUL", tr.allowed + "\x00"},
		{"NUL leading", "\x00"},
		{"over long", tr.allowed + string(os.PathSeparator) + strings.Repeat("a", maxPathLen+1)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tr.guard.Resolve(tc.path)
			if err == nil {
				t.Fatalf("Resolve(%q) = %q, want error", tc.path, got)
			}
			if errors.Is(err, ErrOutsideScope) {
				t.Errorf("invalid input should not be reported as scope violation: %v", err)
			}
		})
	}
}

func TestResolveEmptyIsNotCwd(t *testing.T) {
	tr := newTree(t)
	t.Chdir(tr.allowed)
	if got, err := tr.guard.Resolve(""); err == nil {
		t.Fatalf("Resolve(\"\") inside the allowed cwd = %q, want error", got)
	}
}

func TestResolveRelativeUsesCwd(t *testing.T) {
	tr := newTree(t)
	t.Chdir(tr.in)
	tests := []struct {
		path string
		want string
	}{
		{"sub", filepath.Join(tr.in, "sub")},
		{".", tr.in},
		{filepath.Join("sub", "file"), filepath.Join(tr.in, "sub", "file")},
		{filepath.Join("..", "in"), tr.in},
		{filepath.Join("..", "..", "outside"), ""},
	}
	for _, tc := range tests {
		got, err := tr.guard.Resolve(tc.path)
		if tc.want == "" {
			if !errors.Is(err, ErrOutsideScope) {
				t.Errorf("Resolve(%q) = %q, %v; want ErrOutsideScope", tc.path, got, err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("Resolve(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
		}
	}
}

func TestResolveNonExistent(t *testing.T) {
	tr := newTree(t)
	sep := string(os.PathSeparator)
	tests := []struct {
		name    string
		path    string
		want    string
		wantErr bool
	}{
		{"tail inside", filepath.Join(tr.in, "new", "deeper", "file"), filepath.Join(tr.in, "new", "deeper", "file"), false},
		{"tail with dot", tr.in + sep + "new" + sep + "." + sep + "x", filepath.Join(tr.in, "new", "x"), false},
		{"dotdot in tail", tr.in + sep + "new" + sep + ".." + sep + "sub", "", true},
		{"dotdot in tail escaping", tr.in + sep + "new" + sep + ".." + sep + ".." + sep + ".." + sep + "outside", "", true},
		{"dotdot after existing then tail", tr.in + sep + ".." + sep + "new", filepath.Join(tr.allowed, "new"), false},
		{"tail outside", filepath.Join(tr.outside, "new"), "", true},
		{"through a file", filepath.Join(tr.in, "sub", "file", "child"), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tr.guard.Resolve(tc.path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Resolve(%q) = %q, want error", tc.path, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Resolve(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestResolveDotDotBelowFileIsRefused(t *testing.T) {
	tr := newTree(t)
	path := filepath.Join(tr.in, "sub", "file") + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "file"
	if got, err := tr.guard.Resolve(path); err == nil {
		t.Fatalf("Resolve(%q) = %q; the OS fails with ENOTDIR, so must we", path, got)
	}
}

func TestIsAllowedRoot(t *testing.T) {
	tr := newTree(t)
	nested, err := NewGuard(tr.allowed, tr.in)
	if err != nil {
		t.Fatal(err)
	}
	sep := string(os.PathSeparator)
	tests := []struct {
		name  string
		guard *Guard
		path  string
		want  bool
	}{
		{"root", tr.guard, tr.allowed, true},
		{"root with trailing separator", tr.guard, tr.allowed + sep, true},
		{"root via dotdot", tr.guard, tr.in + sep + "..", true},
		{"above root via dotdot", tr.guard, tr.in + sep + ".." + sep + "..", false},
		{"child", tr.guard, tr.in, false},
		{"missing child", tr.guard, filepath.Join(tr.allowed, "missing"), false},
		{"outside", tr.guard, tr.outside, false},
		{"prefix sibling", tr.guard, filepath.Join(tr.root, "allowedc"), false},
		{"empty", tr.guard, "", false},
		{"NUL", tr.guard, "\x00", false},
		{"nested outer root", nested, tr.allowed, true},
		{"nested inner root", nested, tr.in, true},
		{"nested inner child", nested, filepath.Join(tr.in, "sub"), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.guard.IsAllowedRoot(tc.path); got != tc.want {
				t.Errorf("IsAllowedRoot(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}

func TestNestedLocationsResolveInsideEither(t *testing.T) {
	tr := newTree(t)
	inner := mkdir(t, tr.root, "elsewhere", "inner")
	g, err := NewGuard(tr.in, inner)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(tr.in, "sub"), filepath.Join(inner, "x")} {
		if _, err := g.Resolve(p); err != nil {
			t.Errorf("Resolve(%q): %v", p, err)
		}
	}
	if _, err := g.Resolve(tr.allowed); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("parent of a nested location must be refused, got %v", err)
	}
}

func TestResolveParent(t *testing.T) {
	tr := newTree(t)
	sep := string(os.PathSeparator)
	file := filepath.Join(tr.in, "sub", "file")
	tests := []struct {
		name string
		path string
		// want empty means an error is expected.
		want string
	}{
		{"file", file, file},
		{"trailing separator stripped", file + sep + sep, file},
		{"missing final element", filepath.Join(tr.in, "missing"), filepath.Join(tr.in, "missing")},
		{"missing parent", filepath.Join(tr.in, "missing", "x"), filepath.Join(tr.in, "missing", "x")},
		{"allowed root itself (its parent is outside)", tr.allowed, ""},
		{"final dot", tr.in + sep + ".", ""},
		{"final dotdot", tr.in + sep + "..", ""},
		{"final dotdot with separator", tr.in + sep + ".." + sep, ""},
		{"volume root", volumeRoot(t), ""},
		{"empty", "", ""},
		{"NUL", file + "\x00", ""},
		{"parent outside", filepath.Join(tr.outside, "secret"), ""},
		{"dotdot to outside", tr.allowed + sep + ".." + sep + "outside" + sep + "x", ""},
		{"prefix sibling", filepath.Join(tr.root, "allowedc", "x"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tr.guard.ResolveParent(tc.path)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("ResolveParent(%q) = %q, want error", tc.path, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ResolveParent(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestResolveParentRelative(t *testing.T) {
	tr := newTree(t)
	t.Chdir(tr.in)
	got, err := tr.guard.ResolveParent("newname")
	if want := filepath.Join(tr.in, "newname"); err != nil || got != want {
		t.Fatalf("ResolveParent(newname) = %q, %v; want %q", got, err, want)
	}
	if got, err := tr.guard.ResolveParent(filepath.Join("..", "..", "outside", "x")); err == nil {
		t.Fatalf("ResolveParent escaping via dotdot = %q, want error", got)
	}
}

func TestResolveParentOfAllowedRootChildKeepsName(t *testing.T) {
	tr := newTree(t)
	got, err := tr.guard.ResolveParent(filepath.Join(tr.allowed, "child"))
	if want := filepath.Join(tr.allowed, "child"); err != nil || got != want {
		t.Fatalf("ResolveParent = %q, %v; want %q", got, err, want)
	}
}

// forcedFold returns a guard whose only location is treated as case
// insensitive regardless of the filesystem the test runs on, so the folding
// logic is exercised on every platform.
func forcedFold(t *testing.T, dir string, fold bool) *Guard {
	t.Helper()
	g, err := NewGuard(dir)
	if err != nil {
		t.Fatal(err)
	}
	g.locations[0].fold = fold
	return g
}

func TestResolveCaseFolding(t *testing.T) {
	tr := newTree(t)
	upper := strings.ToUpper(tr.allowed)
	if upper == tr.allowed {
		t.Skip("temp dir has no cased letters")
	}
	t.Run("folded location accepts and respells", func(t *testing.T) {
		g := forcedFold(t, tr.allowed, true)
		got, err := g.Resolve(filepath.Join(upper, "IN", "New"))
		// Real case-insensitive filesystems may report the remainder in
		// their stored spelling, so only the prefix must match exactly.
		want := filepath.Join(tr.allowed, "IN", "New")
		if err != nil || !strings.EqualFold(got, want) || !strings.HasPrefix(got, tr.allowed) {
			t.Fatalf("Resolve = %q, %v; want %q (prefix respelled to %q)", got, err, want, tr.allowed)
		}
		if !g.IsAllowedRoot(upper) {
			t.Error("IsAllowedRoot must fold case on a case-insensitive location")
		}
	})
	t.Run("case-sensitive location refuses", func(t *testing.T) {
		g := forcedFold(t, tr.allowed, false)
		if runtime.GOOS == "windows" {
			t.Skip("Windows compares the volume case-insensitively; the rest of the path is exercised elsewhere")
		}
		if got, err := g.Resolve(filepath.Join(upper, "IN")); !errors.Is(err, ErrOutsideScope) {
			t.Fatalf("Resolve = %q, %v; want ErrOutsideScope", got, err)
		}
		if g.IsAllowedRoot(upper) {
			t.Error("IsAllowedRoot must not fold case on a case-sensitive location")
		}
	})
	t.Run("folding never lets a sibling in", func(t *testing.T) {
		g := forcedFold(t, tr.allowed, true)
		if got, err := g.Resolve(strings.ToUpper(filepath.Join(tr.root, "allowedc"))); !errors.Is(err, ErrOutsideScope) {
			t.Fatalf("Resolve = %q, %v; want ErrOutsideScope", got, err)
		}
	})
}

func TestCaseSensitiveLocationsKeepCaseVariantsApart(t *testing.T) {
	tr := newTree(t)
	upper := filepath.Join(tr.allowed, "Repo")
	lower := filepath.Join(tr.allowed, "repo")
	if err := os.Mkdir(upper, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(lower, 0o755); err != nil {
		t.Skip("filesystem is case-insensitive: " + err.Error())
	}
	g, err := NewGuard(lower)
	if err != nil {
		t.Fatal(err)
	}
	if g.locations[0].fold {
		t.Fatal("a filesystem holding both Repo and repo must probe as case-sensitive")
	}
	if got, err := g.Resolve(filepath.Join(lower, "x")); err != nil {
		t.Errorf("Resolve(lower child) = %q, %v", got, err)
	}
	if got, err := g.Resolve(filepath.Join(upper, "x")); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("Resolve(Repo child) = %q, %v; want ErrOutsideScope", got, err)
	}
	if g.IsAllowedRoot(upper) {
		t.Error("Repo must not be the allowed root repo")
	}
}

func TestProbeCaseInsensitiveMatchesTheFilesystem(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	dir := mkdir(t, root, "CaseProbeDir")
	before, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	// Independent oracle: does the lower-case spelling reach the same dir?
	want := false
	if a, err := os.Lstat(dir); err == nil {
		if b, err := os.Lstat(filepath.Join(root, "caseprobedir")); err == nil {
			want = os.SameFile(a, b)
		}
	}
	if got := probeCaseInsensitive(dir); got != want {
		t.Errorf("probeCaseInsensitive = %v, oracle says %v", got, want)
	}
	after, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Errorf("the probe must not write: %d entries before, %d after", len(before), len(after))
	}
}

func TestProbeCaseInsensitiveUndecidableIsCaseSensitive(t *testing.T) {
	if probeCaseInsensitive(filepath.Join(volumeRoot(t), "definitely-missing-dir-x1")) {
		t.Error("a missing directory must probe as case-sensitive")
	}
	if probeCaseInsensitive(volumeRoot(t)) {
		t.Error("a root without cased components must probe as case-sensitive")
	}
}

func TestFlipCase(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{"abc", "ABC", true},
		{"ABC", "abc", true},
		{"Abc", "ABC", true},
		{"123", "", false},
		{"日本", "", false},
		{"a1", "A1", true},
	}
	for _, tc := range tests {
		got, ok := flipCase(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("flipCase(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

// TestSameFileFallbackNormalization checks the NFC/NFD case of macOS: the
// candidate spells the location's accented name in the other Unicode form.
func TestSameFileFallbackNormalization(t *testing.T) {
	root := testutil.ResolvedTempDir(t)
	nfc := "café"
	nfd := "café"
	loc := mkdir(t, root, nfc)
	if _, err := os.Lstat(filepath.Join(root, nfd)); err != nil {
		t.Skip("filesystem does not treat NFC and NFD names as one: " + err.Error())
	}
	g := forcedFold(t, loc, true)
	got, err := g.Resolve(filepath.Join(root, nfd, "child"))
	if want := filepath.Join(loc, "child"); err != nil || got != want {
		t.Fatalf("Resolve = %q, %v; want %q (respelled with the location's own form)", got, err, want)
	}
}

func TestLocationContains(t *testing.T) {
	vol := filepath.VolumeName(os.TempDir())
	abs := func(parts ...string) string {
		return vol + string(os.PathSeparator) + filepath.Join(parts...)
	}
	base := abs("a", "b")
	tests := []struct {
		name     string
		fold     bool
		cand     string
		want     string
		wantRest int
		ok       bool
	}{
		{"equal", false, abs("a", "b"), base, 0, true},
		{"child", false, abs("a", "b", "c"), abs("a", "b", "c"), 1, true},
		{"grandchild", false, abs("a", "b", "c", "d"), abs("a", "b", "c", "d"), 2, true},
		{"prefix sibling", false, abs("a", "bc"), "", 0, false},
		{"suffix sibling", false, abs("a", "b.evil"), "", 0, false},
		{"parent", false, abs("a"), "", 0, false},
		{"other tree", false, abs("x", "b"), "", 0, false},
		{"case differs, sensitive", false, abs("a", "B"), "", 0, false},
		{"case differs, folded", true, abs("A", "B", "Child"), abs("a", "b", "Child"), 1, true},
		{"folded sibling", true, abs("A", "BC"), "", 0, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, rest, ok := location{path: base, fold: tc.fold}.contains(tc.cand)
			if ok != tc.ok || got != tc.want || rest != tc.wantRest {
				t.Errorf("contains(%q) = %q, %d, %v; want %q, %d, %v", tc.cand, got, rest, ok, tc.want, tc.wantRest, tc.ok)
			}
		})
	}
}

func TestSplitComponents(t *testing.T) {
	sep := string(os.PathSeparator)
	got := splitComponents(sep + sep + "a" + sep + "." + sep + sep + ".." + sep + "b" + sep)
	want := []string{"a", ".", "..", "b"}
	if len(got) != len(want) {
		t.Fatalf("splitComponents = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("splitComponents = %q, want %q", got, want)
		}
	}
	if len(splitComponents("")) != 0 || len(splitComponents(sep)) != 0 {
		t.Error("empty and root-only strings have no components")
	}
}

func TestSplitParent(t *testing.T) {
	sep := string(os.PathSeparator)
	tests := []struct {
		in, dir, base string
		wantErr       bool
	}{
		{sep + "a" + sep + "b", sep + "a" + sep, "b", false},
		{sep + "a" + sep + "b" + sep + sep, sep + "a" + sep, "b", false},
		{sep + "a", sep, "a", false},
		{"a", ".", "a", false},
		{"a" + sep + "b", "a" + sep, "b", false},
		{sep, "", "", true},
		{".", "", "", true},
		{"a" + sep + "..", "", "", true},
		{"a" + sep + ".", "", "", true},
	}
	for _, tc := range tests {
		dir, base, err := splitParent(tc.in)
		if (err != nil) != tc.wantErr || dir != tc.dir || base != tc.base {
			t.Errorf("splitParent(%q) = %q, %q, %v; want %q, %q, err=%v", tc.in, dir, base, err, tc.dir, tc.base, tc.wantErr)
		}
	}
}

func TestGuardConcurrentResolve(t *testing.T) {
	tr := newTree(t)
	want, err := tr.guard.Resolve(filepath.Join(tr.in, "sub", "file"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				got, err := tr.guard.Resolve(filepath.Join(tr.in, "sub", "file"))
				if err != nil || got != want {
					errs <- errors.New("concurrent Resolve returned " + got)
					return
				}
				if _, err := tr.guard.Resolve(tr.outside); !errors.Is(err, ErrOutsideScope) {
					errs <- errors.New("concurrent Resolve allowed the outside")
					return
				}
				_ = tr.guard.IsAllowedRoot(tr.allowed)
				_ = tr.guard.Allowed()
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}
