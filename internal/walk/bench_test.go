package walk

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

// buildLargeTree generates 100k empty files in 10 x 100 x 100 directories.
// Empty files keep generation fast; the walker cost is dominated by the
// directory reads and Info calls anyway.
func buildLargeTree(b *testing.B) string {
	b.Helper()
	root := b.TempDir()
	for i := 0; i < 10; i++ {
		for j := 0; j < 100; j++ {
			dir := filepath.Join(root, fmt.Sprintf("d%d", i), fmt.Sprintf("s%d", j))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				b.Fatal(err)
			}
			for k := 0; k < 100; k++ {
				f, err := os.Create(filepath.Join(dir, fmt.Sprintf("f%d", k)))
				if err != nil {
					b.Fatal(err)
				}
				f.Close()
			}
		}
	}
	return root
}

func BenchmarkWalk100k(b *testing.B) {
	root := buildLargeTree(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var n atomic.Int64
		err := Walk(context.Background(), root, Options{}, func(Entry) Decision {
			n.Add(1)
			return Continue
		}, nil)
		if err != nil || n.Load() < 100_000 {
			b.Fatalf("n=%d err=%v", n.Load(), err)
		}
	}
}

func BenchmarkDirSizeCold100k(b *testing.B) {
	root := buildLargeTree(b)
	ageTree(b, root)
	cache := b.TempDir()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Fresh ignores the cache, so every iteration is a cold read.
		if _, err := DirSize(context.Background(), root, Options{CacheDir: cache, Fresh: true}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkDirSizeWarm100k(b *testing.B) {
	root := buildLargeTree(b)
	ageTree(b, root)
	opts := Options{CacheDir: b.TempDir()}
	if _, err := DirSize(context.Background(), root, opts); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := DirSize(context.Background(), root, opts); err != nil {
			b.Fatal(err)
		}
	}
}
