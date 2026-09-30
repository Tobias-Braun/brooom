package procs

import (
	"context"
	"testing"
)

// TestLsofDirectoryItselfCountsAsOpen covers a process whose working
// directory is exactly the directory asked about: lsof reports that name
// without anything below it, and it must still mark the directory in use.
func TestLsofDirectoryItselfCountsAsOpen(t *testing.T) {
	tests := []struct {
		name  string
		names []string
		want  bool
	}{
		{"cwd equals dir", []string{"/tmp/dir"}, true},
		{"cwd equals dir, other case", []string{"/TMP/Dir"}, true},
		{"below dir", []string{"/tmp/dir/sub"}, true},
		{"sibling with same prefix", []string{"/tmp/dirty"}, false},
		{"parent", []string{"/tmp"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyBelow(tt.names, dirPrefix("/tmp/dir")); got != tt.want {
				t.Errorf("anyBelow(%q) = %v, want %v", tt.names, got, tt.want)
			}
		})
	}

	run := func(context.Context, []string) ([]byte, error) {
		return []byte("p1\x00\nfcwd\x00n/tmp/dir\x00\n"), nil
	}
	res := map[string]bool{"/tmp/dir": false}
	if err := lsofOpenFiles(context.Background(), run, nil, []string{"/tmp/dir"}, res); err != nil {
		t.Fatal(err)
	}
	if !res["/tmp/dir"] {
		t.Error("directory whose name lsof reports itself is not marked open")
	}
}
