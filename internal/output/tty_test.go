package output

import (
	"os"
	"testing"
)

// TestIsMSYSPtyName pins which pipe names count as a terminal: only the
// MSYS/Cygwin pty pipes, never an ordinary pipe (a redirect or a CI log must
// still be treated as non-interactive).
func TestIsMSYSPtyName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{`\msys-1888ae32e00d56aa-pty0-to-master`, true},
		{`\cygwin-1888ae32e00d56aa-pty12-from-master`, true},
		{`\msys-1888ae32e00d56aa-pty0-to-master-nat`, true},
		{`\msys-1888ae32e00d56aa-pty0-to-master-evil`, false},
		{`\msys-1888ae32e00d56aa-pty0-from-slave`, false},
		{`\msys-1888ae32e00d56aa-pipe-0x1-from-master`, false},
		{`\msys-XYZ-pty0-to-master`, false},
		{`\Device\NamedPipe\something`, false},
		{`msys-1888ae32e00d56aa-pty0-to-master`, false},
		{``, false},
	}
	for _, tt := range tests {
		if got := isMSYSPtyName(tt.name); got != tt.want {
			t.Errorf("isMSYSPtyName(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// TestIsTerminalRegularFile checks that a plain file is not a terminal on any
// OS, so redirected output is never treated as interactive.
func TestIsTerminalRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "x")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if IsTerminal(f) {
		t.Error("a regular file must not be a terminal")
	}
}
