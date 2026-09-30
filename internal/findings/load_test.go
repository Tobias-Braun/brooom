package findings

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
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
