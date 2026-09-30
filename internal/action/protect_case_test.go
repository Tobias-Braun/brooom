package action

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTrashProtectionCaseVariants pins the case rule of the catalog protect
// matching per OS: Windows and macOS filesystems are case-insensitive, so
// ".ENV" is the protected .env there, while on Linux it is an unrelated
// name and must not be over-blocked. CI runs it on all three systems.
func TestTrashProtectionCaseVariants(t *testing.T) {
	tests := []struct{ name, rel string }{
		{"upper case env file", ".ENV"},
		{"mixed case nested env file", "app/.Env"},
		{"mixed case local settings", ".Claude/Settings.Local.json"},
		{"upper case memory file", "claude.local.MD"},
		{"upper case mcp config", ".MCP.JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newTrashFixture(t)
			fx.env.Force = true // protection is never overridable
			p := fx.write("proj/"+tt.rel, "secret")
			_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(p))
			if foldPaths() {
				wantSkip(t, err, "protect")
			} else if err != nil {
				t.Fatalf("case-sensitive filesystem must not treat %s as protected: %v", tt.rel, err)
			}
		})
	}
}

// TestTrashProtectionCaseVariantDirectory covers the directory form: a
// directory whose spelling differs from the pattern still counts as
// containing the protected file on case-insensitive systems.
func TestTrashProtectionCaseVariantDirectory(t *testing.T) {
	fx := newTrashFixture(t)
	fx.env.Force = true
	fx.write("proj/.Claude/Settings.Local.json", "{}")
	fx.write("proj/.Claude/cache/x.log", "x")
	_, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(fx.path("proj/.Claude")))
	if foldPaths() {
		wantSkip(t, err, "protect")
	} else if err != nil {
		t.Fatalf("Plan: %v", err)
	}
}

// TestCoversAndRelsCaseVariants checks the lexical helpers the protection is
// built on: covers folds case exactly on the OSes with case-insensitive
// filesystems, and rels lines the levels up when the limit is spelled in
// another case than the root.
func TestCoversAndRelsCaseVariants(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "Work", "Proj")
	if runtime.GOOS == "windows" {
		root = `C:\Work\Proj`
	}
	lower := strings.ToLower(root)
	inner := filepath.Join(root, "Sub", ".ENV")

	if got := covers(lower, inner); got != foldPaths() {
		t.Errorf("covers(%q, %q) = %v, want %v", lower, inner, got, foldPaths())
	}
	if !covers(root, inner) {
		t.Errorf("covers(%q, %q) = false for identical case", root, inner)
	}

	p := &protection{root: root}
	rels := p.rels(inner, filepath.Join(lower, "sub"))
	wantLevels := 1
	if foldPaths() {
		wantLevels = 2 // relative to the root and to the root's "Sub"
	}
	if len(rels) != wantLevels {
		t.Errorf("rels = %v, want %d levels", rels, wantLevels)
	}
}
