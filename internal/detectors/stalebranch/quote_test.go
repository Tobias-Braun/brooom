package stalebranch

import (
	"testing"

	"github.com/Tobias-Braun/brooom/internal/detect"
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
)

// oddBranchNames are legal git refnames that a shell would interpret.
var oddBranchNames = []struct{ name, quoted string }{
	{"feat/plain", "feat/plain"},
	{"fix;touch${IFS}pwned", "'fix;touch${IFS}pwned'"},
	{"a$b", "'a$b'"},
	{"it's", `'it'\''s'`},
	{"with space", "'with space'"},
	{"a&b|c", "'a&b|c'"},
}

// TestSuggestedCommandsQuoteBranchNames: the Command field is copy-pasted by
// users, so a branch called `fix;touch${IFS}pwned` must stay one shell word
// and every name must follow `--`.
func TestSuggestedCommandsQuoteBranchNames(t *testing.T) {
	for _, tt := range oddBranchNames {
		t.Run(tt.name, func(t *testing.T) {
			b := gitx.Branch{Name: tt.name, Upstream: "origin/x"}
			safeD := safeAction(b, remoteState{upstreamHasTip: true}).Command
			if want := "git branch -d -- " + tt.quoted; safeD != want {
				t.Errorf("safe -d command = %q, want %q", safeD, want)
			}
			safeBig := safeAction(b, remoteState{}).Command
			if want := "git branch -D -- " + tt.quoted; safeBig != want {
				t.Errorf("safe -D command = %q, want %q", safeBig, want)
			}
			s := &scan{env: &detect.Env{Force: true}}
			forced := s.action(b, remoteState{}, []findings.RiskFlag{findings.RiskHasOpenPR}).Command
			if want := "git branch -D -- " + tt.quoted; forced != want {
				t.Errorf("forced command = %q, want %q", forced, want)
			}
		})
	}
}
