package largeuntracked_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/walk"
)

// allocated is the size a finding must carry for the file at path: the bytes
// it occupies on disk, the number the plan and the trash record use too.
func allocated(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return walk.LeafSize(fi)
}

// makeSparse creates a file of the given logical size that occupies almost
// nothing, skipping the test where the filesystem cannot do that.
func makeSparse(t *testing.T, path string, logical int64) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows sizes are logical; the allocation is not available cheaply")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(logical); err != nil {
		t.Skipf("cannot create a sparse file here: %v", err)
	}
	if allocated(t, path) >= logical/2 {
		t.Skip("filesystem does not support sparse files")
	}
}

// TestSparseFilesAreSizedByAllocation: two 2 GiB sparse files occupy almost
// nothing. Sized by fi.Size() they were reported as two 2 GiB findings (an
// inflated "reclaimable"), and the minimum-size threshold decided on the
// logical number. With the allocation rule neither is reported.
func TestSparseFilesAreSizedByAllocation(t *testing.T) {
	h := newHarness(t)
	makeSparse(t, filepath.Join(h.repo.Dir, "a.sparse"), 2<<30)
	makeSparse(t, filepath.Join(h.repo.Dir, "b.sparse"), 2<<30)
	h.want(h.run())
}

// TestSparseFileNextToRealFileReportsAllocation: a large real file is still
// found, with its allocated size, next to a sparse one that is not.
func TestSparseFileNextToRealFileReportsAllocation(t *testing.T) {
	h := newHarness(t)
	makeSparse(t, filepath.Join(h.repo.Dir, "a.sparse"), 2<<30)
	p := h.big("real.bin")
	fs := h.run()
	h.want(fs, "real.bin")
	if fs[0].Kind != findings.KindFile || fs[0].SizeBytes != allocated(t, p) {
		t.Errorf("finding = %+v, want size %d", fs[0], allocated(t, p))
	}
}
