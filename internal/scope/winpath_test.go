package scope

import "testing"

// The Windows path syntax logic is pure string handling, so these rows run on
// every platform; guard_windows_test.go exercises the same rules end to end.

func TestWinNormalizeExtended(t *testing.T) {
	tests := []struct{ in, want string }{
		{`C:\work\repo`, `C:\work\repo`},
		{`\\?\C:\work\repo`, `C:\work\repo`},
		{`\\?\c:\work`, `c:\work`},
		{`//?/C:/work/repo`, `C:\work\repo`},
		{`\\?\UNC\server\share\dir`, `\\server\share\dir`},
		{`\\?\unc\server\share`, `\\server\share`},
		{`\\server\share\dir`, `\\server\share\dir`},
		{`\\?\GLOBALROOT\Device\X`, `\\?\GLOBALROOT\Device\X`},
		{`\\?\Volume{1234}\dir`, `\\?\Volume{1234}\dir`},
		{`relative\path`, `relative\path`},
	}
	for _, tc := range tests {
		if got := winNormalizeExtended(tc.in); got != tc.want {
			t.Errorf("winNormalizeExtended(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestWinCheckInput(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{`C:\work`, false},
		{`c:/work`, false},
		{`\\server\share\x`, false},
		{`\\?\C:\work`, false},
		{`\\?\UNC\server\share\x`, false},
		{`\\.\C:`, true},
		{`//./PhysicalDrive0`, true},
		{`\\.\pipe\name`, true},
		{`\\?\GLOBALROOT\Device\HarddiskVolume1`, true},
		{`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\dir`, true},
		{`\??\C:\work`, true},
		{`\\?\`, true},
	}
	for _, tc := range tests {
		if err := winCheckInput(tc.in); (err != nil) != tc.wantErr {
			t.Errorf("winCheckInput(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
	}
}

func TestWinRejectComponent(t *testing.T) {
	tests := []struct {
		in      string
		wantErr bool
	}{
		{"file.txt", false},
		{"PROGRA~1", false},
		{"my dir", false},
		{".git", false},
		{"..", false},
		{".", false},
		{"console", false},
		{"file.txt:stream", true},
		{"file.txt::$DATA", true},
		{"a*b", true},
		{"a?b", true},
		{"a<b", true},
		{"a|b", true},
		{`a"b`, true},
		{"trailingdot.", true},
		{"trailingspace ", true},
		{"...", true},
		{"NUL", true},
		{"nul.txt", true},
		{"Con", true},
		{"COM1", true},
		{"lpt9.log", true},
		{"AUX ", true},
		{"a\x01b", true},
	}
	for _, tc := range tests {
		if err := winRejectComponent(tc.in); (err != nil) != tc.wantErr {
			t.Errorf("winRejectComponent(%q) error = %v, wantErr %v", tc.in, err, tc.wantErr)
		}
	}
}
