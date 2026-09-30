package detect_test

import (
	"context"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/procs"
)

// TestEnvOpenFilesPrefersSnapshot: with a snapshot the fallback is never
// asked, and N queries cost one listing; without one the fallback answers.
func TestEnvOpenFilesPrefersSnapshot(t *testing.T) {
	fallbackCalls := 0
	fallback := func(context.Context, []string) (map[string]bool, error) {
		fallbackCalls++
		return map[string]bool{}, nil
	}

	loads := 0
	env := &detect.Env{Open: procs.NewSnapshotFrom(func(context.Context) ([]string, error) {
		loads++
		return nil, nil
	})}
	for range 5 {
		if _, err := env.OpenFiles(context.Background(), []string{t.TempDir()}, fallback); err != nil {
			t.Fatal(err)
		}
	}
	if loads != 1 || fallbackCalls != 0 {
		t.Errorf("loads = %d, fallback calls = %d; want 1 and 0", loads, fallbackCalls)
	}

	plain := &detect.Env{}
	if _, err := plain.OpenFiles(context.Background(), []string{"/x"}, fallback); err != nil || fallbackCalls != 1 {
		t.Errorf("without a snapshot the fallback must answer once (calls %d, err %v)", fallbackCalls, err)
	}
}
