package scope

import (
	"runtime"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func TestDefaultMaxDepthMatchesConfig(t *testing.T) {
	if got := config.Default().Scan.MaxDepth; got != DefaultMaxDepth {
		t.Fatalf("config default MaxDepth = %d, scope.DefaultMaxDepth = %d", got, DefaultMaxDepth)
	}
}

func TestIsProjectMarker(t *testing.T) {
	folds := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	tests := []struct {
		name string
		want bool
	}{
		{"package.json", true}, {"go.mod", true}, {"Cargo.toml", true},
		{"pyproject.toml", true}, {"setup.py", true}, {"requirements.txt", true},
		{"Pipfile", true}, {"Gemfile", true}, {"composer.json", true},
		{"pom.xml", true}, {"build.gradle", true}, {"build.gradle.kts", true},
		{"settings.gradle", true}, {"settings.gradle.kts", true},
		{"App.csproj", true}, {"All.sln", true}, {"mix.exs", true},
		{"pubspec.yaml", true}, {"Package.swift", true}, {"deno.json", true},
		{"deno.jsonc", true}, {"CMakeLists.txt", true}, {"Makefile", true},
		{"README.md", false}, {"go.sum", false}, {"csproj", false},
		{"x.csproj.bak", false}, {"package.json.bak", false}, {"", false},
		{"GO.MOD", folds}, {"App.CSPROJ", folds}, {"makefile", folds},
	}
	for _, tt := range tests {
		if got := IsProjectMarker(tt.name); got != tt.want {
			t.Errorf("IsProjectMarker(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestHasProjectMarker(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  bool
	}{
		{"empty", nil, false},
		{"makefile alone", []string{"Makefile"}, false},
		{"makefile with readme", []string{"Makefile", "README.md"}, false},
		{"makefile with go.mod", []string{"Makefile", "go.mod"}, true},
		{"go.mod alone", []string{"go.mod"}, true},
		{"solution", []string{"a.sln"}, true},
	}
	for _, tt := range tests {
		if got := HasProjectMarker(tt.names); got != tt.want {
			t.Errorf("%s: HasProjectMarker(%v) = %v, want %v", tt.name, tt.names, got, tt.want)
		}
	}
}

func TestProjectMarkersReturnsCopy(t *testing.T) {
	m := ProjectMarkers()
	m[0] = "changed"
	if ProjectMarkers()[0] == "changed" {
		t.Fatal("ProjectMarkers exposes the internal table")
	}
}
