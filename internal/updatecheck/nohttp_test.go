package updatecheck

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestOnlyUpdatecheckImportsNetHTTP enforces the no-telemetry promise: the
// update check is the single place allowed to reach the network, so no other
// non-test source file in the module may import net/http (or its
// subpackages) or the raw net package.
func TestOnlyUpdatecheckImportsNetHTTP(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	allowedDir := filepath.Join(root, "internal", "updatecheck")
	fset := token.NewFileSet()
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return skipDir(d.Name())
		}
		if !isProductionGoFile(path) || filepath.Dir(path) == allowedDir {
			return nil
		}
		checked++
		for _, p := range importsOf(t, fset, path) {
			if p == "net" || p == "net/http" || strings.HasPrefix(p, "net/http/") {
				t.Errorf("%s imports %s; only internal/updatecheck may touch the network", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Fatal("no Go files scanned; repository root detection is broken")
	}
}

func skipDir(name string) error {
	switch name {
	case ".git", "node_modules", "site", ".claude":
		return filepath.SkipDir
	}
	return nil
}

func isProductionGoFile(path string) bool {
	return strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go")
}

func importsOf(t *testing.T, fset *token.FileSet, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
	if err != nil {
		t.Errorf("parse %s: %v", path, err)
		return nil
	}
	var out []string
	for _, imp := range f.Imports {
		out = append(out, strings.Trim(imp.Path.Value, `"`))
	}
	return out
}
