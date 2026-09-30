package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerifyFile(t *testing.T) {
	elf := "\x7fELF\x02\x01\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x04\x00\x3e\x00"
	tests := []struct {
		name, verifier, content string
		want                    bool
	}{
		{"elf core", VerifyCoreDump, elf, true},
		{"script", VerifyCoreDump, "#!/bin/sh\n", false},
		{"empty", VerifyCoreDump, "", false},
		{"unknown byte order", VerifyCoreDump, "\x7fELF\x02\x03\x01\x00\x00\x00\x00\x00\x00\x00\x00\x00\x04\x00", false},
		{"minidump", VerifyMinidump, "MDMP1234", true},
		{"pagedump", VerifyMinidump, "PAGEDUMP", true},
		{"pagedu64", VerifyMinidump, "PAGEDU64", true},
		{"text", VerifyMinidump, "MDM", false},
		{"unknown verifier", "nope", "MDMP", false},
		{"elf is no minidump", VerifyMinidump, elf, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := VerifyFile(tc.verifier, p); got != tc.want {
				t.Errorf("VerifyFile = %v, want %v", got, tc.want)
			}
		})
	}
	if VerifyFile(VerifyMinidump, filepath.Join(t.TempDir(), "missing")) {
		t.Error("a missing file must not verify")
	}
	if VerifyFile(VerifyMinidump, t.TempDir()) {
		t.Error("a directory must not verify")
	}
}

// TestVerifyFileRefusesSymlink: a link named core is never a dump, even when
// it points at one.
func TestVerifyFileRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.dmp")
	if err := os.WriteFile(target, []byte("MDMP1234"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.dmp")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if VerifyFile(VerifyMinidump, link) {
		t.Error("symlink verified")
	}
}

func TestCrashDumpEntriesAreVerified(t *testing.T) {
	cat, err := Load(Options{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"core": VerifyCoreDump, "core.[0-9]*": VerifyCoreDump, "*.dmp": VerifyMinidump}
	got := map[string]string{}
	for _, tool := range cat.Tools() {
		if tool.ID != "crash-dumps" {
			continue
		}
		for _, e := range tool.Entries {
			for _, p := range e.Patterns {
				got[p] = e.Verify
			}
		}
	}
	for p, v := range want {
		if got[p] != v {
			t.Errorf("pattern %q verify = %q, want %q", p, got[p], v)
		}
	}
}
