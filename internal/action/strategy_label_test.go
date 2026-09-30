package action

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/findings"
)

// TestGroupLabelNamesResolvedStrategy pins that the plan header and the
// confirmation question say what will really happen: the action type of a removal is
// always "trash", so with --trash-strategy delete the user used to confirm
// "trash" for an irreversible deletion.
func TestGroupLabelNamesResolvedStrategy(t *testing.T) {
	tests := []struct {
		strategy config.TrashStrategy
		want     string
	}{
		{config.StrategyTrash, "trash"},
		{"", "trash"},
		{config.StrategyQuarantine, "quarantine"},
		{config.StrategyDelete, "delete permanently"},
	}
	for _, tt := range tests {
		t.Run(string(tt.strategy), func(t *testing.T) {
			f := trashFinding("/x/dist")
			p := &Plan{Groups: groupSteps([]planned{{findings.ActionTrash, Step{Finding: f, Strategy: tt.strategy}}})}

			var plan bytes.Buffer
			renderPlan(&plan, p)
			if want := "build-artifacts / " + tt.want + ": 1 item"; !strings.HasPrefix(plan.String(), want) {
				t.Errorf("plan header %q, want prefix %q", plan.String(), want)
			}

			var prompt bytes.Buffer
			newConfirmer(strings.NewReader("y\n"), &prompt).confirm(p)
			permanent := strings.Contains(prompt.String(), "deleted permanently")
			if permanent != (tt.strategy == config.StrategyDelete) {
				t.Errorf("prompt %q names a permanent deletion: %v", prompt.String(), permanent)
			}
			if p.Groups[0].Action != findings.ActionTrash {
				t.Errorf("group action %q, want the plain action type", p.Groups[0].Action)
			}
		})
	}
}

// TestGroupsSplitByStrategy checks that items of one detector and action but
// different strategies are confirmed separately instead of sharing one label.
func TestGroupsSplitByStrategy(t *testing.T) {
	step := func(path string, s config.TrashStrategy) planned {
		return planned{findings.ActionTrash, Step{Finding: trashFinding(path), Strategy: s}}
	}
	groups := groupSteps([]planned{
		step("/x/a", config.StrategyQuarantine),
		step("/x/b", config.StrategyQuarantine),
		step("/x/c", config.StrategyDelete),
	})
	if len(groups) != 2 || len(groups[0].Items) != 2 || groups[1].Label() != "delete permanently" {
		t.Fatalf("groups %+v", groups)
	}
}

// TestTrashPlanRecordsStrategy checks that the real trash action reports its
// strategy on the step, which is what the labels are derived from.
func TestTrashPlanRecordsStrategy(t *testing.T) {
	fx := newTrashFixture(t)
	dir := fx.mkdir("proj/dist")
	fx.write("proj/dist/a.txt", "x")
	step, err := trashAction{}.Plan(context.Background(), fx.env, trashFinding(dir))
	if err != nil {
		t.Fatal(err)
	}
	if step.Strategy != config.StrategyQuarantine {
		t.Errorf("Strategy = %q, want quarantine", step.Strategy)
	}
}
