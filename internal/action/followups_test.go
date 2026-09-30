package action

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// TestRenderPlanEscapesControlCharactersInCommand reproduces #182 item 9: a
// file name with an ESC survives shell quoting, so the displayed command has
// to escape it itself or the dry-run plan emits terminal sequences.
func TestRenderPlanEscapesControlCharactersInCommand(t *testing.T) {
	f := hostileFinding()
	cmd := displayCommandFor("linux", config.StrategyTrash, "/work/\x1b[2Jevil\nname", "/q")
	p := &Plan{Groups: []Group{{Detector: "d", Action: findings.ActionTrash,
		Items: []Item{{Step: Step{Finding: f, Description: "trash it", Command: cmd}}}}}}
	var out bytes.Buffer
	renderPlan(&out, p)
	assertClean(t, out.String())
	if !strings.Contains(out.String(), `\x1b`) {
		t.Errorf("escaped ESC missing from the command line:\n%s", out.String())
	}
}

// fixedTrasher is a trasher with a fixed strategy that never removes anything.
type fixedTrasher struct{ strategy config.TrashStrategy }

func (s fixedTrasher) Strategy() config.TrashStrategy { return s.strategy }
func (fixedTrasher) Remove(context.Context, string) (trash.Record, error) {
	return trash.Record{}, nil
}
func (fixedTrasher) Restore(context.Context, trash.Record) error { return nil }

func envWith(strategy config.TrashStrategy, onDelete func()) *Env {
	return &Env{
		Trasher:      func(string) (trash.Trasher, error) { return fixedTrasher{strategy}, nil },
		BeforeDelete: onDelete,
	}
}

// TestDeleteWarningPrecedesConfirmation reproduces #182 item 16: the delete
// warning came from the trash action, after the user had already confirmed.
func TestDeleteWarningPrecedesConfirmation(t *testing.T) {
	var log bytes.Buffer
	fx := newFixture(t, func(o *Options) {
		o.Yes = false
		o.StdinIsTTY = func() bool { return true }
		o.IO.Out = &log
		o.Env = envWith(config.StrategyDelete, func() { log.WriteString("WARNING-DELETE\n") })
	})
	fx.opts.IO.In = strings.NewReader("y\n")
	fx.fake(findings.ActionTrash)
	if _, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1)); err != nil {
		t.Fatal(err)
	}
	out := log.String()
	warn := strings.Index(out, "WARNING-DELETE")
	prompt := strings.Index(out, "[y]es")
	if warn < 0 || prompt < 0 || warn > prompt {
		t.Fatalf("warning must come before the prompt (warn %d, prompt %d):\n%s", warn, prompt, out)
	}
}

// TestDeleteWarningOnlyForAppliedDeletes pins that the early warning neither
// fires in a dry run nor for the reversible strategies.
func TestDeleteWarningOnlyForAppliedDeletes(t *testing.T) {
	tests := []struct {
		name     string
		apply    bool
		strategy config.TrashStrategy
		want     bool
	}{
		{"apply with delete", true, config.StrategyDelete, true},
		{"apply with trash", true, config.StrategyTrash, false},
		{"apply with quarantine", true, config.StrategyQuarantine, false},
		{"dry run with delete", false, config.StrategyDelete, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			warned := false
			fx := newFixture(t, func(o *Options) {
				o.Apply = tt.apply
				o.Env = envWith(tt.strategy, func() { warned = true })
			})
			fx.fake(findings.ActionTrash)
			if _, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1)); err != nil {
				t.Fatal(err)
			}
			if warned != tt.want {
				t.Errorf("warned = %v, want %v", warned, tt.want)
			}
		})
	}
}
