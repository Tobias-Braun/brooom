package trash

import "testing"

// TestCheckBinItemPathMountedVolume covers issue #136 item 1: an item
// recycled from a volume mounted into a folder lives in the bin at the mount
// point (C:\mnt\data\$Recycle.Bin\<sid>), which has more than three path
// components. The check must accept exactly the bin directory of the item's
// own volume, however deep its mount point is.
func TestCheckBinItemPathMountedVolume(t *testing.T) {
	const bin = `C:\mnt\data\$Recycle.Bin\S-1`
	tests := []struct {
		name    string
		path    string
		prefix  string
		wantErr bool
	}{
		{"stored in mounted bin", `C:\mnt\data\$Recycle.Bin\S-1\$RABC.txt`, "$R", false},
		{"case-insensitive", `c:\MNT\Data\$RECYCLE.BIN\s-1\$IABC.txt`, "$I", false},
		{"bin of the parent volume", `C:\$Recycle.Bin\S-1\$RABC.txt`, "$R", true},
		{"bin of another mount", `C:\mnt\second\$Recycle.Bin\S-1\$RABC.txt`, "$R", true},
		{"nested below the bin", `C:\mnt\data\$Recycle.Bin\S-1\$RABC\x`, "$R", true},
		{"sibling directory named like the bin", `C:\mnt\data\x\$Recycle.Bin\S-1\$RABC`, "$R", true},
		{"wrong prefix", `C:\mnt\data\$Recycle.Bin\S-1\file.txt`, "$R", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkBinItemPath(tt.path, tt.prefix, bin); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestCheckBinItemPathRejectsBadBinDir checks that a bin directory that does
// not have the <...>\$Recycle.Bin\<sid> shape never validates anything.
func TestCheckBinItemPathRejectsBadBinDir(t *testing.T) {
	for _, bin := range []string{``, `C:\`, `C:\Users\me`, `C:\$Recycle.Bin`, `\\srv\share\$Recycle.Bin\S-1`} {
		if err := checkBinItemPath(bin+`\$RABC`, "$R", bin); err == nil {
			t.Errorf("bin dir %q accepted", bin)
		}
	}
}
