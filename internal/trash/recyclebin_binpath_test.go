package trash

import (
	"strings"
	"testing"
)

// TestValidateBinPathCountsUTF16 checks that the length limit counts UTF-16
// code units: 200 three-byte characters are 600 bytes but only 200 units.
func TestValidateBinPathCountsUTF16(t *testing.T) {
	if err := validateBinPath(`C:\` + strings.Repeat("日", 200)); err != nil {
		t.Errorf("short path in UTF-16 refused: %v", err)
	}
	// Each emoji is a surrogate pair, so 130 of them are 260 units plus the prefix.
	if err := validateBinPath(`C:\` + strings.Repeat("😀", 130)); err == nil {
		t.Error("path beyond the UTF-16 limit accepted")
	}
}

// TestCheckBinInfoPath checks that only the $I file derived from the stored
// $R item, in the same directory, is accepted.
func TestCheckBinInfoPath(t *testing.T) {
	stored := `C:\$Recycle.Bin\S-1-5-21-1\$RABC.txt`
	tests := map[string]bool{
		`C:\$Recycle.Bin\S-1-5-21-1\$IABC.txt`:   false,
		`c:\$recycle.bin\s-1-5-21-1\$IABC.txt`:   false,
		`C:\$Recycle.Bin\S-1-5-21-1\$IOTHER.txt`: true,
		`C:\$Recycle.Bin\S-1-5-21-2\$IABC.txt`:   true,
		`D:\$Recycle.Bin\S-1-5-21-1\$IABC.txt`:   true,
	}
	for info, wantErr := range tests {
		if err := checkBinInfoPath(info, stored); (err != nil) != wantErr {
			t.Errorf("checkBinInfoPath(%q) = %v, wantErr %v", info, err, wantErr)
		}
	}
}
