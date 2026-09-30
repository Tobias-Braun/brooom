package config

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func TestExpandPathInjected(t *testing.T) {
	env := map[string]string{"WORK": "/w", "EMPTY": "", "N1": "x"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	home := func() (string, error) { return "/home/u", nil }
	tests := []struct {
		name, in string
		percent  bool
		want     string
		wantErr  string
	}{
		{"plain", "/a/b", false, "/a/b", ""},
		{"tilde alone", "~", false, "/home/u", ""},
		{"tilde slash", "~/dev", false, "/home/u/dev", ""},
		{"tilde backslash", `~\dev`, false, `/home/u\dev`, ""},
		{"tilde user untouched", "~bob/x", false, "~bob/x", ""},
		{"dollar var", "$WORK/x", false, "/w/x", ""},
		{"braced var", "${WORK}/x", false, "/w/x", ""},
		{"digits in name", "$N1/y", false, "x/y", ""},
		{"undefined var", "$NOPE/x", false, "", "NOPE"},
		{"undefined braced", "${NOPE}/x", false, "", "NOPE"},
		{"empty var", "$EMPTY/x", false, "", "EMPTY"},
		{"lone dollar literal", "/a$/b", false, "/a$/b", ""},
		{"unterminated brace literal", "/a${b", false, "/a${b", ""},
		{"percent ignored off windows", "%WORK%/x", false, "%WORK%/x", ""},
		{"percent var", `%WORK%\x`, true, `/w\x`, ""},
		{"percent undefined", "%NOPE%", true, "", "NOPE"},
		{"lone percent literal", "50%", true, "50%", ""},
		{"tilde then var", "~/$WORK", false, "/home/u//w", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := expandPath(tt.in, tt.percent, lookup, home)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestExpandPathHomeError(t *testing.T) {
	_, err := expandPath("~/x", false, os.LookupEnv, func() (string, error) { return "", errors.New("no home") })
	if err == nil || !strings.Contains(err.Error(), "home directory") {
		t.Fatalf("got %v", err)
	}
}

func TestExpandPathUsesHomeOverride(t *testing.T) {
	h := t.TempDir()
	t.Setenv("HOME", h)
	t.Setenv("USERPROFILE", h)
	t.Setenv("BROOOM_TEST_VAR", "val")
	got, err := ExpandPath("~/a/${BROOOM_TEST_VAR}")
	if err != nil || got != h+"/a/val" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestIsFilesystemRoot(t *testing.T) {
	type row struct {
		path string
		want bool
	}
	rows := []row{{"", false}, {".", false}, {"relative", false}}
	if isWindows() {
		rows = append(rows, []row{
			{`C:\`, true}, {`C:`, true}, {`c:/`, true}, {`C:\Users`, false},
			{`\\server\share`, true}, {`\\server\share\`, true}, {`\\server\share\dir`, false},
			{`\\?\C:\`, true}, {`\\?\C:\dir`, false},
		}...)
	} else {
		rows = append(rows, []row{
			{"/", true}, {"//", true}, {"/a/..", true}, {"/usr", false}, {"/usr/", false},
		}...)
	}
	for _, r := range rows {
		if got := IsFilesystemRoot(r.path); got != r.want {
			t.Errorf("IsFilesystemRoot(%q) = %v, want %v", r.path, got, r.want)
		}
	}
}
