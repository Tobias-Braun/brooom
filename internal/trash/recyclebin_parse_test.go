package trash

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

// infoBytes builds a $I file of the given version for tests.
func infoBytes(version int64, size int64, deleted time.Time, path string) []byte {
	head := make([]byte, 24)
	binary.LittleEndian.PutUint64(head[0:], uint64(version))
	binary.LittleEndian.PutUint64(head[8:], uint64(size))
	ft := (deleted.Unix()+filetimeEpochOffset)*1e7 + int64(deleted.Nanosecond()/100)
	binary.LittleEndian.PutUint64(head[16:], uint64(ft))
	units := append(utf16.Encode([]rune(path)), 0)
	body := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(body[2*i:], u)
	}
	if version == 1 {
		field := make([]byte, 520)
		copy(field, body)
		return append(head, field...)
	}
	n := make([]byte, 4)
	binary.LittleEndian.PutUint32(n, uint32(len(units)))
	return append(append(head, n...), body...)
}

func TestParseInfo(t *testing.T) {
	when := time.Date(2026, 9, 30, 12, 30, 15, 500_000_000, time.UTC)
	const orig = `C:\Users\me\caf\u00e9 [1]\file name.txt`
	tests := []struct {
		name    string
		data    []byte
		wantErr bool
		want    infoRecord
	}{
		{"v1", infoBytes(1, 1234, when, orig), false, infoRecord{1, 1234, when, orig}},
		{"v2", infoBytes(2, 99, when, orig), false, infoRecord{2, 99, when, orig}},
		{"v2 unicode", infoBytes(2, 0, when, `D:\日本語\ファイル.txt`), false, infoRecord{2, 0, when, `D:\日本語\ファイル.txt`}},
		{"empty", nil, true, infoRecord{}},
		{"short header", make([]byte, 10), true, infoRecord{}},
		{"v1 truncated", infoBytes(1, 1, when, orig)[:300], true, infoRecord{}},
		{"v2 truncated", infoBytes(2, 1, when, orig)[:40], true, infoRecord{}},
		{"v2 no length", infoBytes(2, 1, when, orig)[:26], true, infoRecord{}},
		{"unknown version", infoBytes(3, 1, when, orig), true, infoRecord{}},
		{"negative size", infoBytes(2, -5, when, orig), true, infoRecord{}},
		{"empty path v2", infoBytes(2, 1, when, ""), true, infoRecord{}},
		{"garbage", []byte(strings.Repeat("garbage!", 80)), true, infoRecord{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseInfo(tt.data)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil && (got.Version != tt.want.Version || got.Size != tt.want.Size ||
				!got.DeletedAt.Equal(tt.want.DeletedAt) || got.Path != tt.want.Path) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseInfoInvalidV2Length(t *testing.T) {
	data := infoBytes(2, 1, time.Unix(1, 0), `C:\a`)
	binary.LittleEndian.PutUint32(data[24:], 0xFFFFFFFF) // negative as int32
	if _, err := parseInfo(data); err == nil {
		t.Error("negative path length accepted")
	}
	binary.LittleEndian.PutUint32(data[24:], 40000)
	if _, err := parseInfo(data); err == nil {
		t.Error("oversized path length accepted")
	}
}

func TestFiletimeToTime(t *testing.T) {
	tests := []struct {
		ft   int64
		want time.Time
	}{
		{116444736000000000, time.Unix(0, 0).UTC()},
		{116444736010000000, time.Unix(1, 0).UTC()},
		{116444736000000001, time.Unix(0, 100).UTC()},
		{133_000_000_000_000_000, time.Unix(13_300_000_000-filetimeEpochOffset, 0).UTC()},
	}
	for _, tt := range tests {
		if got := filetimeToTime(tt.ft); !got.Equal(tt.want) {
			t.Errorf("filetimeToTime(%d) = %v, want %v", tt.ft, got, tt.want)
		}
	}
}

func TestBuildFromBuffer(t *testing.T) {
	got, err := buildFromBuffer([]string{`C:\a b\c`, `D:\é`})
	if err != nil {
		t.Fatal(err)
	}
	want := append(utf16.Encode([]rune(`C:\a b\c`)), 0)
	want = append(want, utf16.Encode([]rune(`D:\é`))...)
	want = append(want, 0, 0)
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unit %d = %#x, want %#x", i, got[i], want[i])
		}
	}
	if n := len(got); got[n-1] != 0 || got[n-2] != 0 {
		t.Error("buffer is not double-NUL terminated")
	}

	for _, bad := range [][]string{nil, {""}, {"C:\\a\x00b"}, {`C:\a*`}, {`C:\a?.txt`}, {`C:\ok`, `C:\*.*`}} {
		if _, err := buildFromBuffer(bad); err == nil {
			t.Errorf("buildFromBuffer(%q) accepted", bad)
		}
	}
}

func TestValidateBinPath(t *testing.T) {
	long := `C:\` + strings.Repeat("a", 260)
	tests := []struct {
		name    string
		path    string
		wantErr string // substring, empty for ok
	}{
		{"file", `C:\Users\me\file.txt`, ""},
		{"forward slashes", `C:/Users/me/file.txt`, ""},
		{"spaces unicode brackets", `C:\Users\me\café [x] ü\a b.txt`, ""},
		{"unc file", `\\server\share\dir\file.txt`, ""},
		{"empty", ``, "empty"},
		{"relative", `dir\file.txt`, "relative"},
		{"drive relative", `C:file.txt`, "relative"},
		{"drive root", `C:\`, "root"},
		{"drive root no slash", `C:\\`, "root"},
		{"unc root", `\\server\share`, "root"},
		{"unc root slash", `\\server\share\`, "root"},
		{"unc server only", `\\server`, "root"},
		{"wildcard star", `C:\dir\*.txt`, "wildcard"},
		{"wildcard question", `C:\dir\a?.txt`, "wildcard"},
		{"nul", "C:\\dir\\a\x00b", "NUL"},
		{"in bin", `C:\$Recycle.Bin\S-1-5-21\$RABC.txt`, "Recycle Bin"},
		{"in bin lower", `c:\$recycle.bin\x`, "Recycle Bin"},
		{"dotdot", `C:\dir\..\x`, "clean"},
		{"dot", `C:\dir\.\x`, "clean"},
		{"verbatim", `\\?\C:\dir\file`, `\\?\`},
		{"long", long, "quarantine"},
		{"exactly max", `C:\` + strings.Repeat("a", maxShellPath-3), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBinPath(tt.path)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Errorf("unexpected error: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Errorf("accepted %q", tt.path)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Errorf("error %q lacks %q", err, tt.wantErr)
			}
		})
	}
}

func TestDecideBinAvailability(t *testing.T) {
	const mb = 1024 * 1024
	tests := []struct {
		name      string
		settings  binSettings
		readErr   error
		size      int64
		wantError bool
	}{
		{"fits", binSettings{MaxCapacityMB: 100}, nil, 10 * mb, false},
		{"zero size", binSettings{MaxCapacityMB: 100}, nil, 0, false},
		{"exactly at capacity", binSettings{MaxCapacityMB: 100}, nil, 100 * mb, false},
		{"one byte over", binSettings{MaxCapacityMB: 100}, nil, 100*mb + 1, true},
		{"above capacity", binSettings{MaxCapacityMB: 1}, nil, 5 * mb, true},
		{"nuke on delete", binSettings{NukeOnDelete: true, MaxCapacityMB: 1000}, nil, 1, true},
		{"nuke off sufficient", binSettings{NukeOnDelete: false, MaxCapacityMB: 1000}, nil, mb, false},
		{"settings unreadable", binSettings{MaxCapacityMB: 1000}, errors.New("key missing"), 1, true},
		{"unreadable overrides good values", binSettings{}, errors.New("boom"), 0, true},
		{"large volume no overflow", binSettings{MaxCapacityMB: 4_000_000_000}, nil, 1 << 40, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := decideBinAvailability(`C:\x\f.bin`, tt.settings, tt.readErr, tt.size)
			if (err != nil) != tt.wantError {
				t.Fatalf("err = %v, wantError %v", err, tt.wantError)
			}
			if err != nil {
				for _, want := range []string{`C:\x\f.bin`, "--trash-strategy quarantine"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q lacks %q", err, want)
					}
				}
			}
		})
	}
}

func TestVolumeGUIDFromName(t *testing.T) {
	const guid = "{01234567-89ab-cdef-0123-456789abcdef}"
	if got, err := volumeGUIDFromName(`\\?\Volume` + guid + `\`); err != nil || got != guid {
		t.Errorf("got %q, %v", got, err)
	}
	for _, bad := range []string{"", `C:\`, `\\?\Volume{short}\`, `\\?\Volume01234567-89ab-cdef-0123-456789abcdef\`, `\\?\GLOBALROOT\x`} {
		if _, err := volumeGUIDFromName(bad); err == nil {
			t.Errorf("volumeGUIDFromName(%q) accepted", bad)
		}
	}
}

func TestChooseRecycled(t *testing.T) {
	at := time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)
	mk := func(name, path string, off time.Duration) binEntry {
		return binEntry{Name: name, Info: infoRecord{Path: path, DeletedAt: at.Add(off)}}
	}
	entries := []binEntry{
		mk("$IOLD.txt", `C:\d\f.txt`, -time.Hour),
		mk("$IA.txt", `C:\d\f.txt`, time.Second),
		mk("$IB.txt", `c:/D/F.TXT`, 2*time.Second),
		mk("$IC.txt", `C:\d\other.txt`, time.Second),
	}
	got, ok := chooseRecycled(entries, `C:\d\f.txt`, at, matchTolerance)
	if !ok || got.Name != "$IB.txt" {
		t.Errorf("got %q, %v; want newest case-insensitive match $IB.txt", got.Name, ok)
	}
	if _, ok := chooseRecycled(entries, `C:\d\missing.txt`, at, matchTolerance); ok {
		t.Error("matched a different path")
	}
	if _, ok := chooseRecycled(entries, `C:\d\f.txt`, at.Add(time.Hour), matchTolerance); ok {
		t.Error("matched outside the tolerance")
	}
	if got, ok := chooseRecycled(entries, `C:\d\f.txt`, at.Add(-time.Hour), 2*time.Second); !ok || got.Name != "$IOLD.txt" {
		t.Errorf("got %q, %v; want $IOLD.txt", got.Name, ok)
	}
	// The bin records the long spelling; a caller may hold the 8.3 one. Any
	// of the given spellings may match.
	short := `C:\USERS\RUNNER~1\f.txt`
	long := binEntry{Name: "$ILONG.txt", Info: infoRecord{Path: `C:\Users\runneradmin\f.txt`, DeletedAt: at}}
	if got, ok := chooseRecycledAny([]binEntry{long}, []string{short, `C:\Users\runneradmin\f.txt`}, at, matchTolerance); !ok || got.Name != "$ILONG.txt" {
		t.Errorf("got %q, %v; want $ILONG.txt via the second spelling", got.Name, ok)
	}
	if _, ok := chooseRecycledAny([]binEntry{long}, []string{short}, at, matchTolerance); ok {
		t.Error("matched an unrelated spelling")
	}
}

func TestStoredAndInfoNames(t *testing.T) {
	if got := storedName("$IAB12CD.tar.gz"); got != "$RAB12CD.tar.gz" {
		t.Errorf("storedName = %q", got)
	}
	if got := infoNameOf("$RAB12CD"); got != "$IAB12CD" {
		t.Errorf("infoNameOf = %q", got)
	}
}

func TestCheckBinItemPath(t *testing.T) {
	const bin = `C:\$Recycle.Bin\S-1`
	tests := []struct {
		name    string
		path    string
		prefix  string
		wantErr bool
	}{
		{"ok stored", `C:\$Recycle.Bin\S-1\$RABC.txt`, "$R", false},
		{"other sid", `C:\$Recycle.Bin\S-2\$RABC.txt`, "$R", true},
		{"ok info lowercase bin", `c:\$recycle.bin\S-1\$IABC.txt`, "$I", false},
		{"other volume", `D:\$Recycle.Bin\S-1\$RABC`, "$R", true},
		{"not in bin", `C:\Users\me\$RABC`, "$R", true},
		{"nested deeper", `C:\$Recycle.Bin\S-1\$RABC\inner`, "$R", true},
		{"no sid", `C:\$Recycle.Bin\$RABC`, "$R", true},
		{"wrong prefix", `C:\$Recycle.Bin\S-1\file.txt`, "$R", true},
		{"prefix only", `C:\$Recycle.Bin\S-1\$R`, "$R", true},
		{"unc", `\\srv\share\$Recycle.Bin\S-1\$RA`, "$R", true},
		{"relative", `$Recycle.Bin\S-1\$RA`, "$R", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkBinItemPath(tt.path, tt.prefix, bin); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestShellErrors(t *testing.T) {
	if !isInUseCode(32) || !isInUseCode(33) || isInUseCode(5) || isInUseCode(0) {
		t.Error("isInUseCode wrong")
	}
	if got := shellErrorMessage(0x78); got != "access denied" {
		t.Errorf("0x78 = %q", got)
	}
	if got := shellErrorMessage(0xDEAD); !strings.Contains(got, "0xDEAD") {
		t.Errorf("unknown code = %q", got)
	}
}

func TestNormalizeWinPath(t *testing.T) {
	for _, in := range []string{`C:\Dir\File.TXT`, `c:/dir/file.txt`, `C:\dir\\file.txt\`} {
		if got := normalizeWinPath(in); got != `c:\dir\file.txt` {
			t.Errorf("normalizeWinPath(%q) = %q", in, got)
		}
	}
}

// TestCheckBinOwnerSID covers the SID comparison of checkBinOwner without
// needing the real current user.
func TestCheckBinOwnerSID(t *testing.T) {
	const sid = "S-1-5-21-1-2-3-1001"
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"own bin", `C:\$Recycle.Bin\` + sid + `\$RABC.txt`, false},
		{"case differs", `C:\$Recycle.Bin\s-1-5-21-1-2-3-1001\$RABC.txt`, false},
		{"other user", `C:\$Recycle.Bin\S-1-5-21-1-2-3-1002\$RABC.txt`, true},
		{"no sid directory", `C:\$RABC.txt`, true},
		{"sid is the item", `C:\$Recycle.Bin\` + sid, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := checkBinOwnerSID(tt.path, sid); (err != nil) != tt.wantErr {
				t.Errorf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
