package cli

import (
	"bytes"
	"testing"
)

func TestResolveColor(t *testing.T) {
	tests := []struct {
		name string
		in   colorInputs
		want bool
	}{
		{"tty auto", colorInputs{isTTY: true, term: "xterm-256color", configColor: "auto"}, true},
		{"tty empty config", colorInputs{isTTY: true, term: "xterm"}, true},
		{"not a tty", colorInputs{isTTY: false, term: "xterm"}, false},
		{"dumb terminal", colorInputs{isTTY: true, term: "dumb"}, false},
		{"NO_COLOR set", colorInputs{isTTY: true, term: "xterm", noColorEnv: "1"}, false},
		{"NO_COLOR empty is ignored", colorInputs{isTTY: true, term: "xterm", noColorEnv: ""}, true},
		{"config never beats tty", colorInputs{isTTY: true, term: "xterm", configColor: "never"}, false},
		{"config always without tty", colorInputs{isTTY: false, configColor: "always"}, true},
		{"config always beats NO_COLOR", colorInputs{noColorEnv: "1", configColor: "always"}, true},
		{"config always beats dumb", colorInputs{isTTY: true, term: "dumb", configColor: "always"}, true},
		{"flag beats config always", colorInputs{noColorFlag: true, configColor: "always", isTTY: true, term: "xterm"}, false},
		{"flag with tty", colorInputs{noColorFlag: true, isTTY: true, term: "xterm"}, false},
		{"unknown config value acts as auto", colorInputs{isTTY: true, term: "xterm", configColor: "sometimes"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveColor(tt.in); got != tt.want {
				t.Errorf("resolveColor(%+v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNonFileWritersAreNotTerminals(t *testing.T) {
	var buf bytes.Buffer
	if w := terminalWidth(&buf); w != 0 {
		t.Errorf("terminalWidth(buffer) = %d, want 0 (never truncate when piped)", w)
	}
	if _, ok := stdoutTTY(&buf); ok {
		t.Error("a buffer must not be a terminal")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	if colorEnabled(&buf, false, "auto") {
		t.Error("auto color on a non-terminal must be off")
	}
	if !colorEnabled(&buf, false, "always") {
		t.Error("config always must force color even when piped")
	}
}
