package config

import (
	"strings"
	"testing"
)

// TestValidationErrorEscapesControls pins that the deliberate line breaks are
// the only ones in the message: a newline or escape character quoted from the
// config must not start a line of its own.
func TestValidationErrorEscapesControls(t *testing.T) {
	err := &ValidationError{Problems: []Problem{
		{Field: "roots[0].path", Message: "bad\nforged: line\x1b[31m"},
		{Field: "scan.max_depth", Message: "must not be negative"},
	}}
	lines := strings.Split(err.Error(), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines, got %d: %q", len(lines), err.Error())
	}
	if strings.ContainsRune(err.Error(), '\x1b') || !strings.Contains(lines[1], `bad\nforged: line`) {
		t.Errorf("control characters not escaped: %q", err.Error())
	}
}
