package updatecheck

import (
	"errors"
	"testing"
)

func TestParseVersionValid(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{" v0.0.1 ", "0.0.1"},
		{"1.2.3-rc.1", "1.2.3-rc.1"},
		{"1.2.3+build.5", "1.2.3+build.5"},
		{"1.2.3-alpha-1+sha.abc", "1.2.3-alpha-1+sha.abc"},
	}
	for _, tt := range tests {
		v, err := ParseVersion(tt.in)
		if err != nil {
			t.Errorf("ParseVersion(%q): %v", tt.in, err)
			continue
		}
		if v.String() != tt.want {
			t.Errorf("ParseVersion(%q).String() = %q, want %q", tt.in, v.String(), tt.want)
		}
	}
}

func TestParseVersionInvalid(t *testing.T) {
	for _, in := range []string{
		"", "v", "1", "1.2", "1.2.3.4", "a.b.c", "1.2.x", "01.2.3", "1.2.3-", "1.2.3-rc..1",
		"1.2.3-01", "1.2.3-rc_1", "1.2.3+", "-1.2.3", "99999999999999999999.0.0", "latest", "v1.2.3 beta",
	} {
		_, err := ParseVersion(in)
		if err == nil {
			t.Errorf("ParseVersion(%q) succeeded, want error", in)
			continue
		}
		if !errors.Is(err, ErrInvalidVersion) {
			t.Errorf("ParseVersion(%q) error %v does not wrap ErrInvalidVersion", in, err)
		}
	}
}

func TestCompare(t *testing.T) {
	// Ordered from lowest to highest, following the semver spec example chain.
	ordered := []string{
		"0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0",
		"10.0.0",
	}
	for i, a := range ordered {
		for j, b := range ordered {
			va, err := ParseVersion(a)
			if err != nil {
				t.Fatal(err)
			}
			vb, err := ParseVersion(b)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := va.Compare(vb); got != want {
				t.Errorf("Compare(%s, %s) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestCompareIgnoresBuildMetadata(t *testing.T) {
	a, _ := ParseVersion("1.2.3+a")
	b, _ := ParseVersion("v1.2.3+b")
	if a.Compare(b) != 0 {
		t.Error("build metadata must not affect precedence")
	}
}

func TestIsDevVersion(t *testing.T) {
	tests := map[string]bool{
		"":                                       true,
		"dev":                                    true,
		"(devel)":                                true,
		"v0.0.0-20240101120000-abcdef123456":     true,
		"v1.2.4-0.20240101120000-abcdef123456":   true,
		"v1.2.4-0.20240101120000-abcdef123456+d": true,
		"1.2.3":                                  false,
		"v1.2.3-rc.1":                            false,
		"v1.2.3+dirty":                           false,
	}
	for in, want := range tests {
		if got := IsDevVersion(in); got != want {
			t.Errorf("IsDevVersion(%q) = %v, want %v", in, got, want)
		}
	}
}
