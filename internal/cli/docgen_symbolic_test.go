package cli

import "testing"

func TestSymbolicWith(t *testing.T) {
	tests := []struct {
		name, v, cwd, home, want string
	}{
		{"cwd is root", "/var/lib/x", "/", "/home/u", "/var/lib/x"},
		{"home is root", "/var/lib/x", "/work", "/", "/var/lib/x"},
		{"both root", "/a/b", "/", "/", "/a/b"},
		{"empty prefixes", "/a/b", "", "", "/a/b"},
		{"normal home", "/home/u/.brooom/config.json", "/work", "/home/u", "~/.brooom/config.json"},
		{"exact value", "/home/u", "/work", "/home/u", "~"},
		{"trailing slash prefix", "/home/u/x", "/work", "/home/u/", "~/x"},
		{"cwd replacement", "/work/repo/out", "/work/repo", "/home/u", "<cwd>/out"},
		{"sibling with shared prefix", "/home/user2/x", "/work", "/home/u", "/home/user2/x"},
		{"sibling cwd", "/work/repo2", "/work/repo", "/home/u", "/work/repo2"},
		{"prefix inside longer path", "/mnt/home/u/x", "/work", "/home/u", "/mnt/home/u/x"},
		{"list value", "/home/u/a,/home/user2/b", "/work", "/home/u", "~/a,/home/user2/b"},
		{"windows separators", `C:\Users\u\x`, `C:\work`, `C:\Users\u`, `~\x`},
		{"windows root", `C:\Users\u`, `C:\`, `C:\Users\u`, "~"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := symbolicWith(tt.v, tt.cwd, tt.home); got != tt.want {
				t.Errorf("symbolicWith(%q, %q, %q) = %q, want %q", tt.v, tt.cwd, tt.home, got, tt.want)
			}
		})
	}
}
