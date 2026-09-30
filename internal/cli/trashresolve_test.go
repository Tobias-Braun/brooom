package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/action"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func resolverFixture(t *testing.T, cfg *config.Config, flag config.TrashStrategy) (*trasherResolver, *bytes.Buffer, config.Dirs) {
	t.Helper()
	home := testutil.ResolvedTempDir(t)
	dirs := config.Dirs{Home: home, Quarantine: filepath.Join(home, "quarantine")}
	var stderr bytes.Buffer
	return newTrasherResolver(cfg, flag, dirs, "20260101-000000-abcd", &stderr), &stderr, dirs
}

func TestStrategyPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		flag     config.TrashStrategy
		global   config.TrashStrategy
		perDet   config.TrashStrategy
		detector string
		want     config.TrashStrategy
	}{
		{"default is trash", "", "", "", "d", config.StrategyTrash},
		{"global", "", config.StrategyQuarantine, "", "d", config.StrategyQuarantine},
		{"per detector beats global", "", config.StrategyTrash, config.StrategyQuarantine, "d", config.StrategyQuarantine},
		{"per detector of another detector ignored", "", config.StrategyQuarantine, config.StrategyTrash, "other", config.StrategyQuarantine},
		{"flag beats all", config.StrategyTrash, config.StrategyQuarantine, config.StrategyQuarantine, "d", config.StrategyTrash},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Trash.Strategy = tt.global
			if tt.perDet != "" {
				cfg.Trash.PerDetector = map[string]config.TrashStrategy{"d": tt.perDet}
			}
			r, _, _ := resolverFixture(t, cfg, tt.flag)
			if got := r.strategyFor(tt.detector); got != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

func TestParseTrashStrategy(t *testing.T) {
	for _, ok := range []string{"", "trash", "quarantine", "delete", " Quarantine "} {
		if _, err := parseTrashStrategy(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	_, err := parseTrashStrategy("shred")
	var ue usageError
	if !errors.As(err, &ue) || !strings.Contains(err.Error(), "trash, quarantine, delete") {
		t.Errorf("got %v", err)
	}
}

func TestTrasherIsBuiltOncePerStrategy(t *testing.T) {
	r, _, _ := resolverFixture(t, config.Default(), config.StrategyQuarantine)
	a, err := r.forDetector("x")
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.forDetector("y")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Error("a second trasher was built for the same strategy")
	}
	c, err := r.forStrategy(config.StrategyQuarantine)
	if err != nil || c != a {
		t.Errorf("forStrategy did not reuse the trasher: %v", err)
	}
}

func TestDeleteGate(t *testing.T) {
	tests := []struct {
		name       string
		flag       config.TrashStrategy
		global     config.TrashStrategy
		allow      bool
		wantErr    bool
		wantWarned bool
	}{
		{"config delete without allow_delete", "", config.StrategyDelete, false, true, false},
		{"config delete with allow_delete", "", config.StrategyDelete, true, false, true},
		{"flag delete needs no config opt-in", config.StrategyDelete, "", false, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Default()
			cfg.Trash.Strategy = tt.global
			cfg.Trash.AllowDelete = tt.allow
			r, stderr, dirs := resolverFixture(t, cfg, tt.flag)
			_, err := r.forDetector("d")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				for _, want := range []string{"allow_delete", "--trash-strategy delete"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error does not mention %q: %v", want, err)
					}
				}
			}
			if got := strings.Contains(stderr.String(), deleteWarning); got != tt.wantWarned {
				t.Errorf("warned = %v, want %v (%q)", got, tt.wantWarned, stderr.String())
			}
			_, statErr := os.Stat(filepath.Join(dirs.Home, deleteWarnedMarker))
			if (statErr == nil) != tt.wantWarned {
				t.Errorf("marker exists = %v, want %v", statErr == nil, tt.wantWarned)
			}
		})
	}
}

func TestDeleteWarningShownOnce(t *testing.T) {
	r, stderr, dirs := resolverFixture(t, config.Default(), config.StrategyDelete)
	if _, err := r.forDetector("d"); err != nil {
		t.Fatal(err)
	}
	// A second run (fresh resolver, same home) must not warn again.
	var second bytes.Buffer
	r2 := newTrasherResolver(config.Default(), config.StrategyDelete, dirs, "id", &second)
	if _, err := r2.forDetector("d"); err != nil {
		t.Fatal(err)
	}
	if strings.Count(stderr.String(), deleteWarning) != 1 || second.Len() != 0 {
		t.Errorf("first %q second %q", stderr.String(), second.String())
	}
}

func TestDeleteMarkerFailureNeverBlocks(t *testing.T) {
	r, stderr, dirs := resolverFixture(t, config.Default(), config.StrategyDelete)
	// Replace the home with a file so the marker cannot be created.
	if err := os.RemoveAll(dirs.Home); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dirs.Home, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.forDetector("d"); err != nil {
		t.Fatalf("marker failure blocked the run: %v", err)
	}
	if !strings.Contains(stderr.String(), deleteWarning) {
		t.Errorf("no warning: %q", stderr.String())
	}
}

func TestUndoResolverHasNoDeleteWarning(t *testing.T) {
	r, stderr, _ := resolverFixture(t, config.Default(), "")
	if _, err := r.forStrategy(config.StrategyDelete); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Errorf("warned on undo: %q", stderr.String())
	}
}

func TestMapExecutorError(t *testing.T) {
	failed := &action.Result{Failed: 2}
	tests := []struct {
		name    string
		res     *action.Result
		err     error
		applied bool
		wantErr bool
		usage   bool
	}{
		{"clean", &action.Result{}, nil, true, false, false},
		{"nil result", nil, nil, true, false, false},
		{"confirmation required", nil, action.ErrConfirmationRequired, true, true, true},
		{"interrupted", &action.Result{}, action.ErrInterrupted, true, true, false},
		{"failed steps", failed, nil, true, true, false},
		{"plan failures in a dry run", failed, nil, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := mapExecutorError(tt.res, tt.err, tt.applied)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v", err)
			}
			var ue usageError
			if errors.As(err, &ue) != tt.usage {
				t.Errorf("usage = %v, want %v", !tt.usage, tt.usage)
			}
		})
	}
}

func TestApplyHint(t *testing.T) {
	root := &cobra.Command{Use: "brooom"}
	branches := &cobra.Command{Use: "branches"}
	branches.Flags().Bool("apply", false, "")
	scan := &cobra.Command{Use: "scan"}
	root.AddCommand(branches, scan)
	if got, want := applyHint(branches), "nothing was changed; run `brooom branches --apply` or `brooom sweep`"; got != want {
		t.Errorf("got %q", got)
	}
	if got := applyHint(scan); !strings.Contains(got, "nothing was changed") || !strings.Contains(got, "brooom sweep --apply") {
		t.Errorf("got %q", got)
	}
	if got := rerunHint(branches); got != "re-run 'brooom branches --apply'" {
		t.Errorf("got %q", got)
	}
}
