//go:build windows

package scope

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/testutil"
)

func TestWindowsPathSyntax(t *testing.T) {
	tr := newTree(t)
	drive := filepath.VolumeName(tr.allowed)
	rest := tr.allowed[len(drive):]
	flipDrive := strings.ToLower(drive)
	if flipDrive == drive {
		flipDrive = strings.ToUpper(drive)
	}
	tests := []struct {
		name string
		path string
		// wantErr: 0 = allowed, 1 = ErrOutsideScope, 2 = other refusal.
		wantErr int
	}{
		{"drive letter case", flipDrive + rest, 0},
		{"verbatim drive", `\\?\` + tr.allowed, 0},
		{"verbatim drive child", `\\?\` + tr.in, 0},
		{"forward slashes", drive + strings.ReplaceAll(rest, `\`, "/") + "/in", 0},
		{"mixed separators", drive + rest + `/in\sub`, 0},
		{"different drive", `Z:\work`, 1},
		{"different verbatim drive", `\\?\Z:\work`, 1},
		{"UNC outside", `\\server\share\dir`, 1},
		{"verbatim UNC outside", `\\?\UNC\server\share\dir`, 1},
		{"device path", `\\.\` + tr.allowed, 2},
		{"device path forward slashes", `//./` + tr.allowed, 2},
		{"GLOBALROOT", `\\?\GLOBALROOT\Device\HarddiskVolume1\Windows`, 2},
		{"volume GUID", `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\dir`, 2},
		{"alternate data stream", filepath.Join(tr.in, "sub", "file") + ":stream", 2},
		{"ADS on directory", tr.in + `:$INDEX_ALLOCATION`, 2},
		{"reserved name", filepath.Join(tr.in, "NUL"), 2},
		{"trailing dot", filepath.Join(tr.in, "x."), 2},
		{"prefix sibling", filepath.Join(tr.root, "allowedc"), 1},
		{"dotdot escape", tr.allowed + `\..\outside`, 1},
		{"drive-relative other drive", `Z:dir`, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tr.guard.Resolve(tc.path)
			switch tc.wantErr {
			case 0:
				if err != nil || !strings.EqualFold(got[:len(tr.allowed)], tr.allowed) {
					t.Fatalf("Resolve(%q) = %q, %v; want it allowed", tc.path, got, err)
				}
			case 1:
				if !errors.Is(err, ErrOutsideScope) {
					t.Fatalf("Resolve(%q) = %q, %v; want ErrOutsideScope", tc.path, got, err)
				}
			default:
				if err == nil {
					t.Fatalf("Resolve(%q) = %q; want a refusal", tc.path, got)
				}
			}
		})
	}
}

func TestWindowsDriveRelativeOnCurrentDrive(t *testing.T) {
	tr := newTree(t)
	t.Chdir(tr.in)
	drive := filepath.VolumeName(tr.in)
	got, err := tr.guard.Resolve(drive + "sub")
	if want := filepath.Join(tr.in, "sub"); err != nil || got != want {
		t.Fatalf("Resolve(%ssub) = %q, %v; want %q", drive, got, err, want)
	}
	if got, err := tr.guard.Resolve(drive + `..\..\outside`); !errors.Is(err, ErrOutsideScope) {
		t.Fatalf("drive-relative escape = %q, %v; want ErrOutsideScope", got, err)
	}
}

func TestWindowsVolumeRootRefused(t *testing.T) {
	drive := filepath.VolumeName(testutil.ResolvedTempDir(t))
	for _, root := range []string{drive + `\`, drive, `\\?\` + drive + `\`} {
		if g, err := NewGuard(root); err == nil {
			t.Errorf("NewGuard(%q) = %v, want a refusal of the volume root", root, g.Allowed())
		}
	}
}

func TestWindowsCaseInsensitiveLocation(t *testing.T) {
	tr := newTree(t)
	if !tr.guard.locations[0].fold {
		t.Skip("temp dir is on a case-sensitive directory")
	}
	upper := strings.ToUpper(tr.allowed)
	got, err := tr.guard.Resolve(filepath.Join(upper, "IN"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, tr.allowed) {
		t.Errorf("Resolve = %q, want the allowed location's own spelling %q as prefix", got, tr.allowed)
	}
	if !tr.guard.IsAllowedRoot(upper) {
		t.Error("IsAllowedRoot must fold case")
	}
}

func TestWindowsShortNames(t *testing.T) {
	tr := newTree(t)
	long := mkdir(t, tr.allowed, "a rather long directory name")
	from, err := syscall.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, syscall.MAX_PATH)
	n, err := syscall.GetShortPathName(from, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 || int(n) > len(buf) {
		t.Skipf("no 8.3 short names available: %v", err)
	}
	short := syscall.UTF16ToString(buf[:n])
	if strings.EqualFold(short, long) {
		t.Skip("8.3 short names are disabled on this volume")
	}
	got, err := tr.guard.Resolve(short)
	if err != nil || !strings.EqualFold(got, long) {
		t.Fatalf("Resolve(%q) = %q, %v; want the long name %q", short, got, err, long)
	}
}

func TestWindowsSymlinks(t *testing.T) {
	requireSymlink(t)
	tr := newTree(t)
	out := filepath.Join(tr.allowed, "out")
	symlink(t, tr.outside, out)
	in := filepath.Join(tr.allowed, "link-in")
	symlink(t, tr.in, in)
	if got, err := tr.guard.Resolve(out); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("link out = %q, %v; want ErrOutsideScope", got, err)
	}
	if got, err := tr.guard.Resolve(in); err != nil || got != tr.in {
		t.Errorf("link in = %q, %v; want %q", got, err, tr.in)
	}
	if got, err := tr.guard.ResolveParent(out); err != nil || got != out {
		t.Errorf("ResolveParent(link out) = %q, %v; want the link itself %q", got, err, out)
	}
}

func TestWindowsJunctionOutsideIsRefused(t *testing.T) {
	tr := newTree(t)
	junction := filepath.Join(tr.allowed, "junction")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, tr.outside).CombinedOutput(); err != nil {
		t.Skipf("cannot create a junction: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = os.Remove(junction) })
	if got, err := tr.guard.Resolve(junction); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("junction out = %q, %v; want ErrOutsideScope", got, err)
	}
	if got, err := tr.guard.Resolve(filepath.Join(junction, "secret")); !errors.Is(err, ErrOutsideScope) {
		t.Errorf("file through junction = %q, %v; want ErrOutsideScope", got, err)
	}
	if got, err := tr.guard.ResolveParent(junction); err != nil || got != junction {
		t.Errorf("ResolveParent(junction) = %q, %v; want the junction itself", got, err)
	}
}
