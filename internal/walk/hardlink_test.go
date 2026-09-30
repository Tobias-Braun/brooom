package walk

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

func TestEntryHardLinkID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows reports no file identity, so HardLinkID is always false there")
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "one", "f"), 10)
	writeFile(t, filepath.Join(root, "single"), 10)
	if err := os.Link(filepath.Join(root, "one", "f"), filepath.Join(root, "linked")); err != nil {
		t.Skipf("cannot create hard links here: %v", err)
	}

	var mu sync.Mutex
	ids := map[string]string{}
	err := Walk(context.Background(), root, Options{}, func(e Entry) Decision {
		id, ok := e.HardLinkID()
		mu.Lock()
		defer mu.Unlock()
		if ok {
			ids[e.Rel] = id
		}
		return Continue
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids["one/f"] == "" || ids["one/f"] != ids["linked"] {
		t.Fatalf("hard link ids = %v, want one/f and linked sharing an id and nothing else", ids)
	}
}
