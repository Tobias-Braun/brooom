package findings

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
)

func TestReadReport(t *testing.T) {
	valid, err := json.Marshal(NewReport("1.0.0", time.Unix(0, 0), nil, []Finding{{ID: "a", Path: "/x"}}, nil))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		input   string
		wantErr string
	}{
		{"valid", string(valid), ""},
		{"unknown fields tolerated", `{"schema_version":1,"future":{"x":[1]},"findings":[{"id":"a","new_field":true}]}`, ""},
		{"empty", "", "empty"},
		{"whitespace only", "  \n", "empty"},
		{"invalid json", "{", "invalid JSON"},
		{"not an object", "[1]", "invalid JSON"},
		{"second value", string(valid) + string(valid), "unexpected data"},
		{"missing version", `{"findings":[]}`, "not a brooom findings file"},
		{"zero version", `{"schema_version":0}`, "not a brooom findings file"},
		{"negative version", `{"schema_version":-1}`, "not a brooom findings file"},
		{"newer version", `{"schema_version":2}`, "upgrade brooom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := ReadReport(strings.NewReader(tt.input))
			if tt.wantErr == "" {
				if err != nil || rep == nil {
					t.Fatalf("unexpected error %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// endless yields JSON whitespace forever, like a hostile pipe would.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

func TestReadReportCapsInput(t *testing.T) {
	_, err := ReadReport(io.MultiReader(bytes.NewReader([]byte(`{"schema_version":1,"findings":[`)), endless{}))
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error %v, want a size error", err)
	}
}

// TestReadReportEncodings covers what shells write for `scan --format json >
// file`: Windows PowerShell 5.1 produces UTF-16 LE with a BOM, and some tools
// prepend a UTF-8 BOM. The path holds a non-BMP rune to exercise surrogates.
func TestReadReportEncodings(t *testing.T) {
	const path = "/x/ä🧹"
	doc := `{"schema_version":1,"findings":[{"id":"a","path":"` + path + `"}]}`
	encode16 := func(bom []byte, put func([]byte, uint16)) []byte {
		out := append([]byte{}, bom...)
		for _, u := range utf16.Encode([]rune(doc)) {
			var b [2]byte
			put(b[:], u)
			out = append(out, b[:]...)
		}
		return out
	}
	tests := []struct {
		name  string
		input []byte
	}{
		{"utf-8", []byte(doc)},
		{"utf-8 bom", append([]byte{0xEF, 0xBB, 0xBF}, doc...)},
		{"utf-16 le bom", encode16([]byte{0xFF, 0xFE}, binary.LittleEndian.PutUint16)},
		{"utf-16 be bom", encode16([]byte{0xFE, 0xFF}, binary.BigEndian.PutUint16)},
		{"utf-16 le without bom", encode16(nil, binary.LittleEndian.PutUint16)},
		{"utf-16 be without bom", encode16(nil, binary.BigEndian.PutUint16)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep, err := ReadReport(bytes.NewReader(tt.input))
			if err != nil {
				t.Fatal(err)
			}
			if len(rep.Findings) != 1 || rep.Findings[0].Path != path {
				t.Fatalf("findings %+v", rep.Findings)
			}
		})
	}
	t.Run("utf-16 odd length", func(t *testing.T) {
		if _, err := ReadReport(bytes.NewReader([]byte{0xFF, 0xFE, '{'})); err == nil {
			t.Fatal("want an error")
		}
	})
}

// TestReadReportNDJSONHint checks that brooom's own ndjson output, which is
// not accepted as input, is named as such instead of failing with a generic
// JSON error.
func TestReadReportNDJSONHint(t *testing.T) {
	const want = "input looks like ndjson; clean --from needs --format json output"
	one := `{"id":"a","detector":"logs","path":"/x"}` + "\n"
	tests := []struct{ name, input string }{
		{"one finding", one},
		{"two findings", one + `{"id":"b","detector":"logs","path":"/y"}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ReadReport(strings.NewReader(tt.input))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("error %v, want %q", err, want)
			}
		})
	}
}
