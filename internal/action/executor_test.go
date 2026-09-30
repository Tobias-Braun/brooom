package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/session"
)

// fakeAction is a scriptable Action. Hooks default to a plain successful
// plan and apply; every Apply is appended to the shared log so tests can
// assert execution order.
type fakeAction struct {
	typ   findings.ActionType
	plan  func(f findings.Finding) (Step, error)
	apply func(s Step) (session.Entry, error)
	log   *[]string
}

func (a *fakeAction) Type() findings.ActionType { return a.typ }

func (a *fakeAction) Plan(_ context.Context, _ *Env, f findings.Finding) (Step, error) {
	if a.plan != nil {
		return a.plan(f)
	}
	return defaultStep(f), nil
}

func (a *fakeAction) Apply(_ context.Context, _ *Env, s Step) (session.Entry, error) {
	*a.log = append(*a.log, string(a.typ)+" "+label(s.Finding))
	if a.apply != nil {
		return a.apply(s)
	}
	return session.Entry{}, nil
}

func (a *fakeAction) Undo(context.Context, *Env, session.Entry) error { return nil }

func label(f findings.Finding) string {
	if f.Ref != "" {
		return filepath.Base(f.Path) + ":" + f.Ref
	}
	return filepath.Base(f.Path)
}

func defaultStep(f findings.Finding) Step {
	return Step{Finding: f, Description: string(f.SuggestedAction.Type) + " " + label(f), Command: "run " + label(f)}
}

// fixture bundles an executor with its fakes, output and store.
type fixture struct {
	t      *testing.T
	root   string
	store  *session.Store
	out    bytes.Buffer
	errOut bytes.Buffer
	fakes  map[findings.ActionType]*fakeAction
	log    []string
	opts   Options
}

func newFixture(t *testing.T, mod func(*Options)) *fixture {
	t.Helper()
	fx := &fixture{t: t, root: t.TempDir(), fakes: map[findings.ActionType]*fakeAction{}}
	fx.store = session.NewStore(filepath.Join(fx.root, "sessions"))
	fx.opts = Options{
		Apply: true, Yes: true,
		IO:      IO{In: strings.NewReader(""), Out: &fx.out, Err: &fx.errOut},
		Store:   fx.store,
		Command: "brooom test",
		Now:     func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) },
		Lookup: func(ty findings.ActionType) (Action, bool) {
			a, ok := fx.fakes[ty]
			if !ok {
				return nil, false
			}
			return a, true
		},
		StdinIsTTY: func() bool { return false },
	}
	if mod != nil {
		mod(&fx.opts)
	}
	return fx
}

// fake registers (or returns) the fake for a type.
func (fx *fixture) fake(ty findings.ActionType) *fakeAction {
	if a, ok := fx.fakes[ty]; ok {
		return a
	}
	a := &fakeAction{typ: ty, log: &fx.log}
	fx.fakes[ty] = a
	return a
}

func (fx *fixture) run(fs ...findings.Finding) (*Result, error) {
	fx.t.Helper()
	return NewExecutor(fx.opts).Run(context.Background(), fs)
}

func (fx *fixture) manifests() []*session.Manifest {
	fx.t.Helper()
	ms, problems, err := fx.store.List()
	if err != nil || len(problems) > 0 {
		fx.t.Fatalf("List: %v %v", err, problems)
	}
	return ms
}

func (fx *fixture) path(parts ...string) string {
	return filepath.Join(append([]string{fx.root}, parts...)...)
}

func find(det string, ty findings.ActionType, path, ref string, size int64) findings.Finding {
	kind := findings.KindDir
	if ref != "" {
		kind = findings.KindBranch
	}
	return findings.Finding{
		ID: findings.NewID(det+"/"+string(ty), kind, path, ref), Detector: det, Path: path, Ref: ref, Kind: kind,
		SizeBytes:       size,
		SuggestedAction: findings.SuggestedAction{Type: ty},
	}
}

func TestDryRunPrintsPlanAndCreatesNothing(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Apply = false; o.RerunHint = "brooom branches --apply" })
	repo := fx.path("repo")
	fx.fake(findings.ActionDeleteBranch)
	fx.fake(findings.ActionTrash)
	fs := []findings.Finding{
		find("merged-branch", findings.ActionDeleteBranch, repo, "feat/b", 1500),
		find("merged-branch", findings.ActionDeleteBranch, repo, "feat/a", 1000),
		find("build-artifacts", findings.ActionTrash, filepath.Join(repo, "dist"), "", 2_000_000),
		{ID: "x", Detector: "worktrees", Path: repo, SuggestedAction: findings.SuggestedAction{Type: findings.ActionNone}},
	}
	res, err := fx.run(fs...)
	if err != nil {
		t.Fatal(err)
	}
	want := fmt.Sprintf(`build-artifacts / trash: 1 item, %[1]s
  trash dist (%[1]s)
    $ run dist
merged-branch / delete-branch: 2 items, %[2]s
  delete-branch repo:feat/a (%[3]s)
    $ run repo:feat/a
  delete-branch repo:feat/b (%[4]s)
    $ run repo:feat/b
total reclaimable: %[5]s
flagged, not actionable: 1 (see brooom scan)
dry run: nothing was changed; brooom branches --apply to execute
`, output.FormatSize(2_000_000), output.FormatSize(2500), output.FormatSize(1000), output.FormatSize(1500), output.FormatSize(2_002_500))
	if got := fx.out.String(); got != want {
		t.Errorf("output mismatch\n got:\n%s\nwant:\n%s", got, want)
	}
	if res.SessionID != "" || len(fx.log) != 0 {
		t.Errorf("dry run must not apply: session=%q log=%v", res.SessionID, fx.log)
	}
	if ms := fx.manifests(); len(ms) != 0 {
		t.Errorf("dry run created manifests: %v", ms)
	}
	if _, err := os.Stat(fx.store.Dir); !os.IsNotExist(err) {
		t.Errorf("dry run created the sessions dir (err=%v)", err)
	}
}

func TestDefaultRerunHint(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Apply = false })
	fx.fake(findings.ActionTrash)
	if _, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(fx.out.String(), "dry run: nothing was changed; re-run with --apply to execute\n") {
		t.Errorf("output: %q", fx.out.String())
	}
}

func TestEmptyPlan(t *testing.T) {
	for _, apply := range []bool{false, true} {
		fx := newFixture(t, func(o *Options) { o.Apply = apply })
		res, err := fx.run()
		if err != nil || !res.Plan.Empty() {
			t.Fatalf("apply=%v: %v", apply, err)
		}
		if fx.out.String() != "nothing to clean\n" {
			t.Errorf("apply=%v output %q", apply, fx.out.String())
		}
		if len(fx.manifests()) != 0 {
			t.Errorf("apply=%v: manifest created for empty plan", apply)
		}
	}
}

func TestPlanRiskFlags(t *testing.T) {
	tests := []struct {
		name    string
		flags   []findings.RiskFlag
		force   bool
		planned bool
		reason  string
	}{
		{"no flags", nil, false, true, ""},
		{"informational only", []findings.RiskFlag{findings.RiskGitignored}, false, true, ""},
		{"blocked without force", []findings.RiskFlag{findings.RiskUnpushedCommits}, false, false,
			"blocked by risk flag unpushed_commits (use --force to override)"},
		{"overridable with force", []findings.RiskFlag{findings.RiskUnpushedCommits}, true, true, ""},
		{"never overridable", []findings.RiskFlag{findings.RiskFileOpen}, true, false,
			"blocked by risk flag file_open_by_process (not overridable)"},
		{"mixed lists only blocking flags", []findings.RiskFlag{findings.RiskGitignored, findings.RiskTrackedFiles, findings.RiskCurrentBranch}, true, false,
			"blocked by risk flag tracked_files, current_branch (not overridable)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFixture(t, func(o *Options) { o.Force = tt.force })
			fx.fake(findings.ActionTrash)
			f := find("d", findings.ActionTrash, fx.path("a"), "", 1)
			f.RiskFlags = tt.flags
			p := NewExecutor(fx.opts).Plan(context.Background(), []findings.Finding{f})
			if tt.planned {
				if len(p.Groups) != 1 || len(p.Skipped) != 0 {
					t.Fatalf("want planned, got %+v", p)
				}
				return
			}
			if len(p.Groups) != 0 || len(p.Skipped) != 1 || p.Skipped[0].Reason != tt.reason {
				t.Fatalf("want skip %q, got %+v", tt.reason, p.Skipped)
			}
		})
	}
}

func TestPlanEnvForceIsHonouredAndNotMutated(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Force = true; o.Env = &Env{} })
	fx.fake(findings.ActionTrash)
	f := find("d", findings.ActionTrash, fx.path("a"), "", 1)
	f.RiskFlags = []findings.RiskFlag{findings.RiskWorktreeDirty}
	e := NewExecutor(fx.opts)
	if p := e.Plan(context.Background(), []findings.Finding{f}); len(p.Groups) != 1 {
		t.Fatalf("force not applied: %+v", p)
	}
	if fx.opts.Env.Force {
		t.Error("caller's Env was mutated")
	}
}

func TestPlanMissingAction(t *testing.T) {
	fx := newFixture(t, nil)
	p := NewExecutor(fx.opts).Plan(context.Background(), []findings.Finding{find("d", findings.ActionGitGC, fx.path("a"), "", 1)})
	if len(p.Skipped) != 1 || p.Skipped[0].Reason != "action not available" {
		t.Fatalf("skips: %+v", p.Skipped)
	}
}

func TestPlanErrors(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash).plan = func(f findings.Finding) (Step, error) {
		switch filepath.Base(f.Path) {
		case "skip":
			return Step{}, fmt.Errorf("%w: file is open", ErrSkipped)
		case "bare":
			return Step{}, ErrSkipped
		case "boom":
			return Step{}, errors.New("stat failed")
		}
		return defaultStep(f), nil
	}
	var fs []findings.Finding
	for _, n := range []string{"skip", "bare", "boom", "ok"} {
		fs = append(fs, find("d", findings.ActionTrash, fx.path(n), "", 1))
	}
	p := NewExecutor(fx.opts).Plan(context.Background(), fs)
	reasons := []string{p.Skipped[0].Reason, p.Skipped[1].Reason}
	if len(p.Skipped) != 2 || !slices.Equal(reasons, []string{"file is open", "skipped"}) {
		t.Errorf("skips %v", reasons)
	}
	if len(p.Failed) != 1 || p.Failed[0].Reason != "stat failed" {
		t.Errorf("failed %+v", p.Failed)
	}
	if len(p.Groups) != 1 || len(p.Groups[0].Items) != 1 {
		t.Errorf("groups %+v", p.Groups)
	}

	// A plan-time failure never aborts the run and is reported.
	res, err := NewExecutor(fx.opts).Run(context.Background(), fs)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || res.Failed != 1 || res.Skipped != 2 {
		t.Errorf("counts %d/%d/%d", res.Applied, res.Skipped, res.Failed)
	}
	if !strings.Contains(fx.out.String(), "failed to plan (1):") {
		t.Errorf("output: %s", fx.out.String())
	}
}

func planPaths(p *Plan) []string {
	var out []string
	for _, g := range p.Groups {
		for _, it := range g.Items {
			out = append(out, label(it.Step.Finding))
		}
	}
	return out
}

func TestPlanDedupe(t *testing.T) {
	tests := []struct {
		name string
		mk   func(root string) []findings.Finding
		want []string
		skip []string
	}{
		{"same finding id twice", func(r string) []findings.Finding {
			f := find("m", findings.ActionDeleteBranch, filepath.Join(r, "repo"), "b", 1)
			return []findings.Finding{f, f}
		}, []string{"repo:b"}, nil},
		{"same action path ref, different id", func(r string) []findings.Finding {
			a := find("m", findings.ActionDeleteBranch, filepath.Join(r, "repo"), "b", 1)
			b := a
			b.ID = "other"
			b.Path = filepath.Join(r, "repo", ".") // cleans to the same path
			return []findings.Finding{a, b}
		}, []string{"repo:b"}, nil},
		{"same path different ref is kept", func(r string) []findings.Finding {
			return []findings.Finding{
				find("m", findings.ActionDeleteBranch, filepath.Join(r, "repo"), "a", 1),
				find("m", findings.ActionDeleteBranch, filepath.Join(r, "repo"), "b", 1),
			}
		}, []string{"repo:a", "repo:b"}, nil},
		{"nested trash, outer first", func(r string) []findings.Finding {
			return []findings.Finding{
				find("b", findings.ActionTrash, filepath.Join(r, "p"), "", 10),
				find("b", findings.ActionTrash, filepath.Join(r, "p", "node_modules"), "", 5),
			}
		}, []string{"p"}, []string{"covered by trashing %s/p"},
		},
		{"nested trash, inner first", func(r string) []findings.Finding {
			return []findings.Finding{
				find("b", findings.ActionTrash, filepath.Join(r, "p", "node_modules"), "", 5),
				find("b", findings.ActionTrash, filepath.Join(r, "p"), "", 10),
			}
		}, []string{"p"}, []string{"covered by trashing %s/p"},
		},
		{"sibling with shared prefix is not nested", func(r string) []findings.Finding {
			return []findings.Finding{
				find("b", findings.ActionTrash, filepath.Join(r, "a", "b"), "", 10),
				find("b", findings.ActionTrash, filepath.Join(r, "a", "bc"), "", 5),
			}
		}, []string{"b", "bc"}, nil},
		{"branch step inside trashed dir is covered", func(r string) []findings.Finding {
			return []findings.Finding{
				find("b", findings.ActionTrash, filepath.Join(r, "repo"), "", 10),
				find("m", findings.ActionDeleteBranch, filepath.Join(r, "repo", "sub"), "x", 1),
			}
		}, []string{"repo"}, []string{"covered by trashing %s/repo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFixture(t, nil)
			fx.fake(findings.ActionTrash)
			fx.fake(findings.ActionDeleteBranch)
			p := NewExecutor(fx.opts).Plan(context.Background(), tt.mk(fx.root))
			got := planPaths(p)
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Errorf("planned %v, want %v", got, tt.want)
			}
			var reasons []string
			for _, s := range p.Skipped {
				reasons = append(reasons, s.Reason)
			}
			var wantReasons []string
			for _, r := range tt.skip {
				wantReasons = append(wantReasons, strings.ReplaceAll(r, "%s/", fx.root+string(filepath.Separator)))
			}
			if !slices.Equal(reasons, wantReasons) {
				t.Errorf("skip reasons %q, want %q", reasons, wantReasons)
			}
		})
	}
}

func TestNestedTrashSizeCountedOnce(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Apply = false })
	fx.fake(findings.ActionTrash)
	fs := []findings.Finding{
		find("b", findings.ActionTrash, fx.path("p"), "", 10_000),
		find("b", findings.ActionTrash, fx.path("p", "nm"), "", 4_000),
		find("b", findings.ActionTrash, fx.path("q"), "", 1_000),
	}
	p := NewExecutor(fx.opts).Plan(context.Background(), fs)
	if p.ReclaimableBytes() != 11_000 || p.Groups[0].ReclaimableBytes() != 11_000 {
		t.Errorf("reclaimable %d / %d", p.ReclaimableBytes(), p.Groups[0].ReclaimableBytes())
	}
}

func TestActionPriorityOrder(t *testing.T) {
	gc, prune, reflog, trash, del := findings.ActionGitGC, findings.ActionGitPrune, findings.ActionGitReflogExpire, findings.ActionTrash, findings.ActionDeleteBranch
	orders := map[string][]findings.ActionType{
		"gc first":       {gc, prune, reflog, trash, del},
		"reflog first":   {reflog, prune, gc, del, trash},
		"prune in front": {prune, del, gc, trash, reflog},
	}
	want := []string{"git-reflog-expire repo", "git-prune repo", "git-gc repo", "delete-branch repo:x", "trash repo"}
	for name, order := range orders {
		t.Run(name, func(t *testing.T) {
			fx := newFixture(t, nil)
			repo := fx.path("repo")
			var fs []findings.Finding
			for _, ty := range order {
				ref := ""
				if ty == del {
					ref = "x"
				}
				fs = append(fs, find("git-bloat", ty, repo, ref, 1))
				fx.fake(ty)
			}
			res, err := fx.run(fs...)
			if err != nil {
				t.Fatal(err)
			}
			var groups []string
			for _, g := range res.Plan.Groups {
				groups = append(groups, string(g.Action))
			}
			wantGroups := []string{"git-reflog-expire", "git-prune", "git-gc", "delete-branch", "trash"}
			if !slices.Equal(groups, wantGroups) {
				t.Errorf("group order %v, want %v", groups, wantGroups)
			}
			if !slices.Equal(fx.log, want) {
				t.Errorf("apply order %v, want %v", fx.log, want)
			}
		})
	}
}

func TestPriorityOrdersDetectorFirstThenPathAndRef(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionGitGC)
	fx.fake(findings.ActionTrash)
	fs := []findings.Finding{
		find("z-det", findings.ActionGitGC, fx.path("z"), "", 1),
		find("a-det", findings.ActionTrash, fx.path("b"), "", 1),
		find("a-det", findings.ActionTrash, fx.path("a"), "", 1),
		find("a-det", findings.ActionGitGC, fx.path("a"), "", 1),
	}
	if _, err := fx.run(fs...); err != nil {
		t.Fatal(err)
	}
	want := []string{"git-gc a", "trash a", "trash b", "git-gc z"}
	if !slices.Equal(fx.log, want) {
		t.Errorf("apply order %v, want %v", fx.log, want)
	}
}

func TestApplyRefusesWithoutTTY(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Yes = false })
	fx.fake(findings.ActionTrash)
	res, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1))
	if !errors.Is(err, ErrConfirmationRequired) || res != nil {
		t.Fatalf("got %v, %v", res, err)
	}
	if !strings.Contains(err.Error(), "pass --yes") {
		t.Errorf("message: %v", err)
	}
	if len(fx.log) != 0 || len(fx.manifests()) != 0 || fx.out.Len() != 0 {
		t.Errorf("side effects: log=%v out=%q", fx.log, fx.out.String())
	}
}

func TestYesSkipsPromptsWithoutTTY(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash)
	res, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1))
	if err != nil || res.Applied != 1 {
		t.Fatalf("%v %+v", err, res)
	}
	if strings.Contains(fx.out.String(), "[y]es") {
		t.Errorf("prompted despite --yes: %s", fx.out.String())
	}
}

func TestConfirmationFlows(t *testing.T) {
	// Two groups: delete-branch (a, b) sorts before trash (t).
	tests := []struct {
		name  string
		input string
		want  []string
		save  bool
	}{
		{"yes to all", "y\ny\n", []string{"delete-branch r:a", "delete-branch r:b", "trash t"}, true},
		{"no then yes", "n\ny\n", []string{"trash t"}, true},
		{"empty means no", "\n\n", nil, false},
		{"uppercase and spaces and CRLF", "  Y \r\nYES\r\n", []string{"delete-branch r:a", "delete-branch r:b", "trash t"}, true},
		{"individually mixed", "i\ny\nn\nn\n", []string{"delete-branch r:a"}, true},
		{"individually then group yes", "i\nn\ny\ny\n", []string{"delete-branch r:b", "trash t"}, true},
		{"quit at first group", "q\n", nil, false},
		{"quit at second group discards first", "y\nq\n", nil, false},
		{"quit inside individual discards everything", "y\ni\nq\n", nil, false},
		{"eof at first prompt", "", nil, false},
		{"eof after first answer", "y\n", nil, false},
		{"eof without trailing newline still answers", "y\nn", []string{"delete-branch r:a", "delete-branch r:b"}, true},
		{"invalid input re-asks", "maybe\nx\ny\nn\n", []string{"delete-branch r:a", "delete-branch r:b"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fx := newFixture(t, func(o *Options) {
				o.Yes = false
				o.StdinIsTTY = func() bool { return true }
				o.IO.In = strings.NewReader(tt.input)
			})
			fx.fake(findings.ActionDeleteBranch)
			fx.fake(findings.ActionTrash)
			repo := fx.path("r")
			res, err := fx.run(
				find("m", findings.ActionDeleteBranch, repo, "a", 1),
				find("m", findings.ActionDeleteBranch, repo, "b", 1),
				find("t", findings.ActionTrash, fx.path("t"), "", 1),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(fx.log, tt.want) {
				t.Errorf("applied %v, want %v\noutput:\n%s", fx.log, tt.want, fx.out.String())
			}
			if got := len(fx.manifests()) == 1; got != tt.save {
				t.Errorf("manifest created=%v, want %v", got, tt.save)
			}
			if tt.save != (res.SessionID != "") {
				t.Errorf("session id %q", res.SessionID)
			}
			notConfirmed := 0
			for _, s := range res.Skips {
				if s.Reason == "not confirmed" {
					notConfirmed++
				}
			}
			if notConfirmed != 3-len(tt.want) {
				t.Errorf("%d 'not confirmed' skips, want %d", notConfirmed, 3-len(tt.want))
			}
		})
	}
}

func TestManifestLifecycle(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.SessionID = "20260930-120000-abcd" })
	var seen []int
	check := func(s Step) (session.Entry, error) {
		m, err := fx.store.Load("20260930-120000-abcd")
		if err != nil {
			t.Errorf("manifest missing before Apply of %s: %v", label(s.Finding), err)
			return session.Entry{}, nil
		}
		if !m.FinishedAt.IsZero() || m.Command != "brooom test" || m.Version != session.ManifestVersion {
			t.Errorf("unexpected manifest %+v", m)
		}
		seen = append(seen, len(m.Entries))
		return session.Entry{}, nil
	}
	fx.fake(findings.ActionTrash).apply = check
	res, err := fx.run(
		find("d", findings.ActionTrash, fx.path("a"), "", 100),
		find("d", findings.ActionTrash, fx.path("b"), "", 50),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []int{0, 1}) {
		t.Errorf("entries visible before each apply: %v, want [0 1]", seen)
	}
	m := fx.manifests()[0]
	if res.SessionID != m.ID || len(m.Entries) != 2 || m.ReclaimedBytes != 150 || m.FinishedAt.IsZero() {
		t.Errorf("final manifest %+v", m)
	}
	if !m.StartedAt.Equal(fx.opts.Now()) {
		t.Errorf("StartedAt %v", m.StartedAt)
	}
}

// TestApplyJournalsEntriesInsteadOfRewritingManifest is the regression test
// for the quadratic manifest rewrite: while steps run, <id>.json keeps the
// snapshot taken at the start and every entry is durable in <id>.journal
// (visible through Load); Finish then folds the journal into the snapshot.
func TestApplyJournalsEntriesInsteadOfRewritingManifest(t *testing.T) {
	const id = "20260930-120000-abcd"
	fx := newFixture(t, func(o *Options) { o.SessionID = id })
	snapshot := filepath.Join(fx.store.Dir, id+".json")
	journal := filepath.Join(fx.store.Dir, id+".journal")
	var start []byte
	fx.fake(findings.ActionTrash).apply = func(s Step) (session.Entry, error) {
		cur, err := os.ReadFile(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if start == nil {
			start = cur
		}
		if string(cur) != string(start) {
			t.Errorf("manifest snapshot rewritten before %s", label(s.Finding))
		}
		return session.Entry{}, nil
	}
	if _, err := fx.run(
		find("d", findings.ActionTrash, fx.path("a"), "", 1),
		find("d", findings.ActionTrash, fx.path("b"), "", 1),
		find("d", findings.ActionTrash, fx.path("c"), "", 1),
	); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Errorf("journal not compacted at finish: %v", err)
	}
	if m := fx.manifests()[0]; len(m.Entries) != 3 || m.FinishedAt.IsZero() {
		t.Errorf("final manifest %+v", m)
	}
}

func TestGeneratedSessionID(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash)
	res, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1))
	if err != nil || !strings.HasPrefix(res.SessionID, "20260930-120000-") {
		t.Fatalf("%v %q", err, res.SessionID)
	}
}

func TestEntryFillDoesNotOverride(t *testing.T) {
	fx := newFixture(t, nil)
	at := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	fx.fake(findings.ActionTrash).apply = func(s Step) (session.Entry, error) {
		return session.Entry{Path: "/custom", SizeBytes: 7, At: at, Restorable: true, RecoveryHint: "hint", Status: session.StatusApplied}, nil
	}
	f := find("d", findings.ActionTrash, fx.path("a"), "", 100)
	res, err := fx.run(f)
	if err != nil {
		t.Fatal(err)
	}
	e := res.Entries[0]
	if e.Path != "/custom" || e.SizeBytes != 7 || !e.At.Equal(at) || e.FindingID != f.ID || e.Detector != "d" ||
		e.Action != findings.ActionTrash || e.Status != session.StatusApplied {
		t.Errorf("entry %+v", e)
	}
	if res.ReclaimedBytes != 7 {
		t.Errorf("reclaimed %d", res.ReclaimedBytes)
	}
}

func TestFailingStepContinues(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash).apply = func(s Step) (session.Entry, error) {
		if filepath.Base(s.Finding.Path) == "a" {
			return session.Entry{RecoveryHint: "look in /trash"}, errors.New("locked")
		}
		return session.Entry{}, nil
	}
	res, err := fx.run(
		find("d", findings.ActionTrash, fx.path("a"), "", 100),
		find("d", findings.ActionTrash, fx.path("b"), "", 50),
	)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 1 || res.Failed != 1 || res.ReclaimedBytes != 50 {
		t.Errorf("result %+v", res)
	}
	m := fx.manifests()[0]
	if len(m.Entries) != 2 || m.Entries[0].Status != session.StatusFailed || m.Entries[0].Error != "locked" || m.ReclaimedBytes != 50 {
		t.Errorf("manifest %+v", m)
	}
	out := fx.out.String()
	for _, want := range []string{"1 applied, 0 skipped, 1 failed", "locked", "look in /trash", "session: " + m.ID} {
		if !strings.Contains(out, want) {
			t.Errorf("summary lacks %q:\n%s", want, out)
		}
	}
}

func TestSaveFailureBeforeFirstStep(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash)
	// A regular file where the sessions dir should be makes every Save fail.
	if err := os.WriteFile(fx.store.Dir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1))
	if err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err: %v", err)
	}
	if len(fx.log) != 0 {
		t.Errorf("applied despite Save failure: %v", fx.log)
	}
}

func TestSaveFailureAfterStepStopsRun(t *testing.T) {
	fx := newFixture(t, nil)
	fx.fake(findings.ActionTrash).apply = func(s Step) (session.Entry, error) {
		// Break the store while the first step runs: the sessions dir turns
		// into a file, so the Save after this step fails.
		if err := os.RemoveAll(fx.store.Dir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fx.store.Dir, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return session.Entry{RecoveryHint: "git branch x abc"}, nil
	}
	res, err := fx.run(
		find("d", findings.ActionTrash, fx.path("a"), "", 1),
		find("d", findings.ActionTrash, fx.path("b"), "", 1),
	)
	if err == nil || !strings.Contains(err.Error(), "save session manifest") {
		t.Fatalf("err: %v", err)
	}
	if len(fx.log) != 1 {
		t.Errorf("further steps ran: %v", fx.log)
	}
	if !strings.Contains(fx.errOut.String(), "git branch x abc") {
		t.Errorf("recovery hint not printed: %q", fx.errOut.String())
	}
	if res.Applied != 1 || res.Skipped != 1 || !strings.Contains(fx.out.String(), "session manifest could not be saved") {
		t.Errorf("result %+v\n%s", res, fx.out.String())
	}
}

func TestInterruptBetweenSteps(t *testing.T) {
	fx := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fx.fake(findings.ActionTrash).apply = func(s Step) (session.Entry, error) {
		cancel() // Ctrl-C arrives while step 1 is running.
		return session.Entry{}, nil
	}
	res, err := NewExecutor(fx.opts).Run(ctx, []findings.Finding{
		find("d", findings.ActionTrash, fx.path("a"), "", 1),
		find("d", findings.ActionTrash, fx.path("b"), "", 1),
	})
	if !errors.Is(err, ErrInterrupted) || !errors.Is(err, context.Canceled) {
		t.Fatalf("err: %v", err)
	}
	if !slices.Equal(fx.log, []string{"trash a"}) {
		t.Errorf("applied %v", fx.log)
	}
	m := fx.manifests()[0]
	if len(m.Entries) != 1 || m.FinishedAt.IsZero() {
		t.Errorf("manifest %+v", m)
	}
	if res.Applied != 1 || len(res.Skips) != 1 || res.Skips[0].Reason != "interrupted" {
		t.Errorf("result %+v", res)
	}
	if !strings.Contains(fx.out.String(), "summary: 1 applied, 1 skipped, 0 failed") {
		t.Errorf("no summary:\n%s", fx.out.String())
	}
}

func TestApplyUsesUncancellableContext(t *testing.T) {
	fx := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	var applyErr error
	calls := 0
	a := fx.fake(findings.ActionTrash)
	a.plan = func(f findings.Finding) (Step, error) { return defaultStep(f), nil }
	f := find("d", findings.ActionTrash, fx.path("a"), "", 1)
	fx.opts.Lookup = func(findings.ActionType) (Action, bool) { return ctxProbe{a, &applyErr, cancel, &calls}, true }
	if _, err := NewExecutor(fx.opts).Run(ctx, []findings.Finding{f}); err != nil {
		t.Fatal(err)
	}
	if applyErr != nil {
		t.Errorf("Apply saw a cancelled context: %v", applyErr)
	}
}

// ctxProbe cancels the run's context in the second Plan call (the re-plan
// right before Apply) and records whether Apply still sees a live context.
type ctxProbe struct {
	*fakeAction
	seen   *error
	cancel context.CancelFunc
	calls  *int
}

func (p ctxProbe) Plan(ctx context.Context, env *Env, f findings.Finding) (Step, error) {
	if *p.calls++; *p.calls == 2 {
		p.cancel()
	}
	return p.fakeAction.Plan(ctx, env, f)
}

func (p ctxProbe) Apply(ctx context.Context, env *Env, s Step) (session.Entry, error) {
	*p.seen = ctx.Err()
	return p.fakeAction.Apply(ctx, env, s)
}

func TestCancelledDuringPromptChangesNothing(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Yes = false; o.StdinIsTTY = func() bool { return true } })
	ctx, cancel := context.WithCancel(context.Background())
	fx.opts.IO.In = cancelOnRead{strings.NewReader("y\n"), cancel}
	fx.fake(findings.ActionTrash)
	_, err := NewExecutor(fx.opts).Run(ctx, []findings.Finding{find("d", findings.ActionTrash, fx.path("a"), "", 1)})
	if !errors.Is(err, ErrInterrupted) || len(fx.log) != 0 || len(fx.manifests()) != 0 {
		t.Fatalf("err=%v log=%v", err, fx.log)
	}
}

type cancelOnRead struct {
	*strings.Reader
	cancel context.CancelFunc
}

func (c cancelOnRead) Read(p []byte) (int, error) {
	c.cancel()
	return c.Reader.Read(p)
}

func TestReplanSkipAtApplyTime(t *testing.T) {
	fx := newFixture(t, nil)
	calls := map[string]int{}
	fx.fake(findings.ActionTrash).plan = func(f findings.Finding) (Step, error) {
		n := filepath.Base(f.Path)
		calls[n]++
		if n == "a" && calls[n] == 2 {
			return Step{}, fmt.Errorf("%w: file became open", ErrSkipped)
		}
		if n == "c" && calls[n] == 2 {
			return Step{}, errors.New("stat failed")
		}
		return defaultStep(f), nil
	}
	res, err := fx.run(
		find("d", findings.ActionTrash, fx.path("a"), "", 1),
		find("d", findings.ActionTrash, fx.path("b"), "", 1),
		find("d", findings.ActionTrash, fx.path("c"), "", 1),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fx.log, []string{"trash b"}) {
		t.Errorf("applied %v", fx.log)
	}
	if res.Applied != 1 || res.Skipped != 1 || res.Failed != 1 || res.Skips[0].Reason != "file became open" {
		t.Errorf("result %+v", res)
	}
	// Skipped-at-apply items are not in the manifest; a failed re-plan is.
	m := fx.manifests()[0]
	if len(m.Entries) != 2 || m.Entries[0].Status != session.StatusApplied || m.Entries[1].Status != session.StatusFailed {
		t.Errorf("entries %+v", m.Entries)
	}
}

func TestSummaryUndoHintOnlyForRestorable(t *testing.T) {
	for _, restorable := range []bool{true, false} {
		t.Run(fmt.Sprint(restorable), func(t *testing.T) {
			fx := newFixture(t, func(o *Options) { o.SessionID = "sid-1" })
			fx.fake(findings.ActionDeleteBranch).apply = func(s Step) (session.Entry, error) {
				return session.Entry{Restorable: restorable, RecoveryHint: "git branch feat/x 1a2b3c4"}, nil
			}
			res, err := fx.run(find("m", findings.ActionDeleteBranch, fx.path("repo"), "feat/x", 2_500_000))
			if err != nil {
				t.Fatal(err)
			}
			out := fx.out.String()
			if got := strings.Contains(out, "undo: brooom undo sid-1"); got != restorable {
				t.Errorf("undo hint present=%v, want %v\n%s", got, restorable, out)
			}
			for _, want := range []string{
				"summary: 1 applied, 0 skipped, 0 failed",
				"reclaimed: " + output.FormatSize(2_500_000),
				"session: sid-1",
				"recovery hints:",
				"git branch feat/x 1a2b3c4",
			} {
				if !strings.Contains(out, want) {
					t.Errorf("summary lacks %q:\n%s", want, out)
				}
			}
			if res.ReclaimedBytes != 2_500_000 || res.Restorable() != restorable {
				t.Errorf("result %+v", res)
			}
		})
	}
}

func TestApplyWithoutStore(t *testing.T) {
	fx := newFixture(t, func(o *Options) { o.Store = nil })
	fx.fake(findings.ActionTrash)
	if _, err := fx.run(find("d", findings.ActionTrash, fx.path("a"), "", 1)); err == nil {
		t.Fatal("want error")
	}
}
