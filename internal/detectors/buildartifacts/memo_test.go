package buildartifacts

import (
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func TestNewMatcherCompilesRulesOncePerDirs(t *testing.T) {
	cfg := config.BuildArtifacts{Dirs: []string{"memo-once-dir"}}
	if _, err := newMatcher(t.TempDir(), cfg); err != nil {
		t.Fatal(err)
	}
	before := compileCalls.Load()
	for range 5 {
		if _, err := newMatcher(t.TempDir(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	if got := compileCalls.Load(); got != before {
		t.Errorf("rules compiled %d more times for identical dirs, want 0", got-before)
	}
	other := config.BuildArtifacts{Dirs: []string{"memo-other-dir"}}
	if _, err := newMatcher(t.TempDir(), other); err != nil {
		t.Fatal(err)
	}
	if got := compileCalls.Load(); got != before+1 {
		t.Errorf("different dirs must compile separately, calls = %d", got-before)
	}
}

// TestNewMatcherMemoKeyKeepsEmptyEntriesDistinct pins the key: an invalid
// empty extra dir must not hit the entry of the empty configuration.
func TestNewMatcherMemoKeyKeepsEmptyEntriesDistinct(t *testing.T) {
	if _, err := newMatcher(t.TempDir(), config.BuildArtifacts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := newMatcher(t.TempDir(), config.BuildArtifacts{ExtraDirs: []string{""}}); err == nil {
		t.Error("empty extra dir must stay an error")
	}
}

func TestNewMatcherInvalidDirsAreNotCached(t *testing.T) {
	bad := config.BuildArtifacts{ExtraDirs: []string{":"}}
	for range 2 {
		if _, err := newMatcher(t.TempDir(), bad); err == nil {
			t.Fatal("want an error for an invalid extra dir")
		}
	}
}
