package walk

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestSparseFileIsSizedByAllocation: a 2 GiB sparse file occupies almost
// nothing, so the single-file rule (LeafSize, used by the detectors) and the
// directory rule (DirSize) must both report the allocation and agree.
// Sizing by fi.Size() reported 2 GiB per file and inflated "reclaimable".
func TestSparseFileIsSizedByAllocation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows sizes are logical; the allocation is not available cheaply")
	}
	const logical = 2 << 30
	root := t.TempDir()
	p := filepath.Join(root, "sparse.bin")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(logical); err != nil {
		f.Close()
		t.Skipf("cannot create a sparse file here: %v", err)
	}
	f.Close()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if AllocatedSize(fi) >= logical/2 {
		t.Skip("filesystem does not support sparse files")
	}
	if got := LeafSize(fi); got != AllocatedSize(fi) || got >= logical/2 {
		t.Errorf("LeafSize = %d, want the allocation (logical size %d)", got, int64(logical))
	}
	sum := mustSize(t, root, Options{})
	rootInfo, _ := Stat(root)
	if want := AllocatedSize(fi) + rootInfo.Allocated; sum.SizeBytes != want {
		t.Errorf("DirSize = %d, want %d (file allocation plus directory blocks)", sum.SizeBytes, want)
	}
}

// TestLeafSizeSymlinkIsLogical: a symlink is never followed and counts its
// own length, whatever its target occupies.
func TestLeafSizeSymlinkIsLogical(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "t.bin")
	writeFile(t, target, 100_000)
	link := filepath.Join(root, "l")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if got := LeafSize(fi); got != fi.Size() {
		t.Errorf("LeafSize(symlink) = %d, want its own size %d", got, fi.Size())
	}
}

// TestDirSizeCountsDirectoryBlocks: a tree of empty directories is not free.
// DirSize counts the blocks of the root and of every subdirectory, as du
// does, so node_modules-like trees are not under-reported.
func TestDirSizeCountsDirectoryBlocks(t *testing.T) {
	root := t.TempDir()
	dirs := []string{root, filepath.Join(root, "a"), filepath.Join(root, "a", "b")}
	if err := os.MkdirAll(dirs[2], 0o755); err != nil {
		t.Fatal(err)
	}
	var want int64
	for _, d := range dirs {
		e, err := Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		want += e.Allocated
	}
	if runtime.GOOS != "windows" && want == 0 {
		t.Skip("filesystem reports no blocks for directories")
	}
	if got := mustSize(t, root, Options{}); got.SizeBytes != want || got.Files != 0 {
		t.Errorf("DirSize = %+v, want %d bytes of directory blocks and no files", got, want)
	}
}
