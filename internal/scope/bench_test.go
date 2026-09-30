package scope

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkDiscover500Repos measures discovery over 500 fake repositories
// spread over 50 groups, every fifth with a node_modules tree that must not
// be entered. The tree is built outside the timer.
func BenchmarkDiscover500Repos(b *testing.B) {
	root := b.TempDir()
	for i := 0; i < 500; i++ {
		repo := filepath.Join(root, fmt.Sprintf("group%02d", i%50), fmt.Sprintf("repo%03d", i))
		writeBenchFile(b, filepath.Join(repo, ".git", "HEAD"))
		writeBenchFile(b, filepath.Join(repo, "main.go"))
		if i%5 == 0 {
			for j := 0; j < 20; j++ {
				writeBenchFile(b, filepath.Join(repo, "node_modules", fmt.Sprintf("pkg%d", j), "package.json"))
			}
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ts, err := Discover(context.Background(), []string{root}, DiscoverOptions{})
		if err != nil || len(ts) != 500 {
			b.Fatalf("got %d targets, err %v", len(ts), err)
		}
	}
}

func writeBenchFile(b *testing.B, path string) {
	b.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		b.Fatal(err)
	}
}
