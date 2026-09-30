package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Tobias-Braun/brooom/internal/buildinfo"
	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/detect"
	_ "github.com/Tobias-Braun/brooom/internal/detectors/all" // links every real detector into the registry
	"github.com/Tobias-Braun/brooom/internal/findings"
	"github.com/Tobias-Braun/brooom/internal/gitx"
	"github.com/Tobias-Braun/brooom/internal/output"
	"github.com/Tobias-Braun/brooom/internal/scope"
)

// errScanInterrupted is returned (with the partial result) when the scan
// context was cancelled, for example by Ctrl-C. It maps to exit code 1.
var errScanInterrupted = errors.New("scan interrupted")

// defaultFormat is used when neither --format nor config output.format is set.
const defaultFormat = "table"

// scanRequest is a validated scan: configuration loaded, output format and
// detector selection resolved. Splitting it from execution lets commands
// fail on usage errors before any long-running work starts.
type scanRequest struct {
	opts      scanOptions
	cfg       *config.Config
	cfgPath   string
	format    string
	detectors []detect.Detector
}

// scanResult is everything a caller needs after a scan. The guard and the
// configuration are returned, not discarded, so the action executor of the
// shortcut commands can act on the findings within exactly the scope that
// was scanned.
type scanResult struct {
	Report  *findings.Report
	Config  *config.Config
	Env     *detect.Env
	Guard   *scope.Guard
	Targets []scope.Target
}

// scan is the reusable entry point: validate, then run. When the context is
// cancelled it returns the partial result together with errScanInterrupted.
// onFinding, when set, is called for every finding as soon as it is found.
func (a *app) scan(ctx context.Context, opts scanOptions, onFinding func(findings.Finding)) (*scanResult, error) {
	req, err := a.newScanRequest(opts)
	if err != nil {
		return nil, err
	}
	return a.execute(ctx, req, onFinding)
}

// newScanRequest validates flags and loads the configuration. Cheap usage
// checks come first so a typo fails before the config is read.
func (a *app) newScanRequest(opts scanOptions) (*scanRequest, error) {
	if len(a.flags.roots) > 0 && !a.flags.workspaces {
		return nil, usageError{errors.New("--root only narrows --workspaces; add --workspaces or drop --root")}
	}
	detectors, err := selectDetectors(a.flags.detectors, opts.detectors)
	if err != nil {
		return nil, err
	}
	cfg, path, err := a.loadConfig()
	if err != nil {
		return nil, err
	}
	format, err := resolveFormat(a.flags.format, cfg.Output.Format)
	if err != nil {
		return nil, err
	}
	if opts.configOverlay != nil {
		opts.configOverlay(cfg)
	}
	if opts.targetsOnly {
		detectors = nil
	}
	if opts.userLocations {
		applyUserLocations(cfg, opts.detectors)
	}
	return &scanRequest{opts: opts, cfg: cfg, cfgPath: path, format: format, detectors: detectors}, nil
}

// applyUserLocations turns on the user_locations switch of the detector the
// command selected, for this run only. --user of `brooom logs` must not
// enable the ai locations and vice versa, so the selection decides; an empty
// selection keeps the historical ai-artifacts behaviour.
func applyUserLocations(cfg *config.Config, selected []string) {
	if len(selected) == 0 || slices.Contains(selected, config.DetectorAIArtifacts) {
		cfg.Detectors.AIArtifacts.UserLocations = true
	}
	if slices.Contains(selected, config.DetectorLogs) {
		cfg.Detectors.Logs.UserLocations = true
	}
}

// loadConfig loads the config file named by --config, or the default one.
// An explicitly given file must exist; a missing default file means the
// defaults. Load and validation errors are returned unchanged because they
// name the offending key.
func (a *app) loadConfig() (*config.Config, string, error) {
	path, err := a.configPath()
	if err != nil {
		return nil, "", err
	}
	if err := a.requireExplicitConfig(path); err != nil {
		return nil, "", err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}

// resolveFormat picks the output format (flag, then config, then table) and
// checks that a formatter is registered for it.
func resolveFormat(flagValue, configValue string) (string, error) {
	name := flagValue
	if name == "" {
		name = configValue
	}
	if name == "" {
		name = defaultFormat
	}
	if _, err := output.Get(name); err != nil {
		return "", usageError{fmt.Errorf("unknown format %q (available: %s)", name, strings.Join(output.Names(), ", "))}
	}
	return name, nil
}

// resolveActingFormat is resolveFormat for a run that acts (--apply, purge
// operations). Its plan and confirmation text are human output, so a machine
// format that only came from the config's output.format (the user never passed
// --format) must not block the run: it falls back to the default human format.
// An explicit --format is returned as is, for the caller to refuse.
func resolveActingFormat(flagValue, configValue string) (string, error) {
	name, err := resolveFormat(flagValue, configValue)
	if err != nil {
		return "", err
	}
	if flagValue == "" && machineFormats[name] {
		return defaultFormat, nil
	}
	return name, nil
}

// selectDetectors resolves the detectors of a scan from the --detector flag
// and the preset of a shortcut command. Both must name registered detectors;
// when both are set the selection is their intersection, and an empty
// intersection is a usage error. With neither set, every registered
// detector is a candidate.
func selectDetectors(flagNames, preset []string) ([]detect.Detector, error) {
	names, err := validateDetectorNames(flagNames)
	if err != nil {
		return nil, err
	}
	presetNames, err := validateDetectorNames(preset)
	if err != nil {
		return nil, err
	}
	switch {
	case len(names) > 0 && len(presetNames) > 0:
		names = intersect(names, presetNames)
		if len(names) == 0 {
			return nil, usageError{fmt.Errorf("--detector %s does not overlap with this command's detectors (%s)",
				strings.Join(flagNames, ","), strings.Join(preset, ","))}
		}
	case len(presetNames) > 0:
		names = presetNames
	}
	if len(names) == 0 {
		return detect.All(), nil
	}
	out := make([]detect.Detector, 0, len(names))
	for _, n := range names {
		d, _ := detect.Get(n)
		out = append(out, d)
	}
	return out, nil
}

// validateDetectorNames checks names against the registry, drops duplicates
// and returns them sorted by name (the order detect.All uses).
func validateDetectorNames(names []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		if _, ok := detect.Get(n); !ok {
			return nil, usageError{fmt.Errorf("unknown detector %q (available: %s)", n, strings.Join(registeredNames(), ", "))}
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// registeredNames lists the names of all registered detectors.
func registeredNames() []string {
	all := detect.All()
	names := make([]string, len(all))
	for i, d := range all {
		names[i] = d.Name()
	}
	return names
}

// intersect returns the names present in both sorted lists.
func intersect(a, b []string) []string {
	inB := map[string]bool{}
	for _, n := range b {
		inB[n] = true
	}
	var out []string
	for _, n := range a {
		if inB[n] {
			out = append(out, n)
		}
	}
	return out
}

// execute builds targets, guard and environment, runs the detectors and
// assembles the report. Nothing here modifies the file system.
func (a *app) execute(ctx context.Context, req *scanRequest, onFinding func(findings.Finding)) (*scanResult, error) {
	runner, gitErr := newGitRunner()
	ts, err := a.buildTargets(ctx, req, runner)
	if err != nil {
		return nil, err
	}
	ts.addExtraTargets(ctx, req.cfg, req.detectors)
	effective, targets, effErrs := effectiveConfigs(req.cfg, ts.targets)
	ts.errs = append(ts.errs, effErrs...)
	if err := requireGit(gitErr, req.detectors, targets); err != nil {
		return nil, err
	}
	guard, err := ts.newGuard()
	if err != nil {
		return nil, fmt.Errorf("build scope: %w", err)
	}
	env, err := newEnv(req, runner, guard)
	if err != nil {
		return nil, err
	}
	a.progressf("config: %s", req.cfgPath)
	a.progressf("scanning %d target(s) with %d detector(s)", len(targets), len(req.detectors))

	found, runErrs, stats := a.runDetectors(ctx, env, targets, req, effective, req.opts.filterStream(onFinding))
	if kept := req.opts.filter(found); len(kept) != len(found) {
		a.progressf("dropped %d finding(s) below minimum confidence %s", len(found)-len(kept), req.opts.minConfidence)
		found = kept
	}
	a.logDetectorStats(req.detectors, found, stats)
	sort.SliceStable(runErrs, func(i, j int) bool {
		x, y := runErrs[i], runErrs[j]
		if x.Detector != y.Detector {
			return x.Detector < y.Detector
		}
		if x.Path != y.Path {
			return x.Path < y.Path
		}
		return x.Message < y.Message
	})
	errs := append(ts.errs, runErrs...)
	report := findings.NewReport(buildinfo.Get().Version, env.Now, scopesOf(targets), found, errs)
	res := &scanResult{Report: report, Config: req.cfg, Env: env, Guard: guard, Targets: targets}
	if ctx.Err() != nil {
		return res, errScanInterrupted
	}
	return res, nil
}

// newGitRunner returns the git runner, or the error why there is none. A
// missing git is only fatal when a git detector has a repo to look at
// (requireGit), so scans of plain project folders work without git.
func newGitRunner() (gitx.Runner, error) {
	r, err := gitx.NewExecRunner()
	if err != nil {
		return nil, err
	}
	return r, nil
}

// requireGit turns a missing git binary into an error when a selected git
// detector would run on a repo target.
func requireGit(gitErr error, detectors []detect.Detector, targets []scope.Target) error {
	if gitErr == nil {
		return nil
	}
	hasRepo := false
	for _, t := range targets {
		hasRepo = hasRepo || t.Kind == scope.TargetRepo
	}
	if !hasRepo {
		return nil
	}
	for _, d := range detectors {
		if d.Category() == detect.CategoryGit {
			return fmt.Errorf("the %s detector needs git: %w", d.Name(), gitErr)
		}
	}
	return nil
}

// newEnv builds the shared detector environment. Now is taken once so every
// age computation of the scan agrees; CacheDir is only set when caching is
// enabled, and detectors never resolve it themselves.
func newEnv(req *scanRequest, runner gitx.Runner, guard *scope.Guard) (*detect.Env, error) {
	env := &detect.Env{
		Config: req.cfg,
		Git:    runner,
		Guard:  guard,
		Now:    time.Now(),
		Force:  req.opts.force,
	}
	if runner != nil {
		env.Repos = gitx.NewCache(runner)
	}
	if req.cfg.Scan.Cache {
		dirs, err := config.ResolveDirs()
		if err != nil {
			return nil, err
		}
		env.CacheDir = dirs.Cache
	}
	return env, nil
}

// runDetectors runs the selected detectors, timing them when verbose.
func (a *app) runDetectors(ctx context.Context, env *detect.Env, targets []scope.Target, req *scanRequest,
	effective map[string]*config.Config, onFinding func(findings.Finding)) ([]findings.Finding, []findings.ScanError, *detectorStats) {
	stats := &detectorStats{elapsed: map[string]time.Duration{}}
	dets := req.detectors
	if a.verboseOn() {
		dets = make([]detect.Detector, len(req.detectors))
		for i, d := range req.detectors {
			dets[i] = timedDetector{Detector: d, stats: stats}
		}
	}
	found, errs := detect.Run(ctx, env, targets, dets, detect.RunOptions{
		Concurrency: req.cfg.Scan.Concurrency,
		Applies:     appliesFunc(effective),
		OnFinding:   onFinding,
	})
	return found, errs, stats
}

// scopesOf returns the deduplicated target scopes in target order.
func scopesOf(targets []scope.Target) []findings.Scope {
	scopes := []findings.Scope{}
	seen := map[findings.Scope]bool{}
	for _, t := range targets {
		if !seen[t.Scope] {
			seen[t.Scope] = true
			scopes = append(scopes, t.Scope)
		}
	}
	return scopes
}

// detectorStats accumulates the time spent in each detector across targets.
type detectorStats struct {
	mu      sync.Mutex
	elapsed map[string]time.Duration
}

func (s *detectorStats) add(name string, d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.elapsed[name] += d
}

// timedDetector measures Detect for the verbose per-detector summary. It
// embeds the detector so Name and Category (used by Applies) pass through.
type timedDetector struct {
	detect.Detector
	stats *detectorStats
}

func (t timedDetector) Detect(ctx context.Context, env *detect.Env, target scope.Target, emit func(findings.Finding)) error {
	start := time.Now()
	defer func() { t.stats.add(t.Name(), time.Since(start)) }()
	return t.Detector.Detect(ctx, env, target, emit)
}

// verboseOn reports whether diagnostics go to stderr: --verbose is ignored
// when --quiet is given.
func (a *app) verboseOn() bool { return a.flags.verbose && !a.flags.quiet }

// progressf writes one diagnostic line to stderr in verbose mode. Progress
// never goes to stdout, which keeps json, ndjson and plain pipe-safe.
func (a *app) progressf(format string, args ...any) {
	if a.verboseOn() {
		fmt.Fprintln(a.io.Err, output.Sanitize(fmt.Sprintf(format, args...)))
	}
}

// logDetectorStats prints the finding count and the accumulated time of each
// selected detector.
func (a *app) logDetectorStats(dets []detect.Detector, found []findings.Finding, stats *detectorStats) {
	if !a.verboseOn() {
		return
	}
	counts := map[string]int{}
	for _, f := range found {
		counts[f.Detector]++
	}
	for _, d := range dets {
		stats.mu.Lock()
		el := stats.elapsed[d.Name()]
		stats.mu.Unlock()
		a.progressf("  %s: %d finding(s) in %s", d.Name(), counts[d.Name()], el.Round(time.Millisecond))
	}
}

// logScanErrors prints scan errors to stderr: always when always is set
// (streaming formats cannot render them), otherwise only in verbose mode.
func (a *app) logScanErrors(errs []findings.ScanError, always bool) {
	if !always && !a.verboseOn() {
		return
	}
	for _, e := range errs {
		fmt.Fprintln(a.io.Err, "scan error:", formatScanError(e))
	}
}

// errorsInBand reports whether the format renders Report.Errors itself. The
// others (plain, ndjson) would hide a failed scan behind an empty stdout, so
// their errors always go to stderr.
func errorsInBand(format string) bool {
	switch format {
	case "json", "table", "tree", "summary":
		return true
	}
	return false
}

// scanFailure returns the error for a scan that covered no target although
// errors occurred (every repository skipped, unusable configuration), so
// scripts can tell it from a clean scan. The rule is deliberately about
// targets and not about detectors: the engine records every error a detector
// returns as a ScanError, and a detector also returns non-fatal notes that way
// (for example the worktrees detector naming a linked worktree that lies
// outside the scope while it still analysed the others), so "every detector
// reported an error" cannot tell a total failure from a note. Partial and
// per-detector failures therefore stay exit 0 and are reported with the
// report.
func scanFailure(r *findings.Report) error {
	if len(r.Errors) == 0 || len(r.Scopes) > 0 {
		return nil
	}
	return scanFailedError{fmt.Errorf("nothing was scanned: %d scan error(s)", len(r.Errors))}
}

func formatScanError(e findings.ScanError) string {
	var parts []string
	if e.Detector != "" {
		parts = append(parts, e.Detector)
	}
	if e.Path != "" {
		parts = append(parts, e.Path)
	}
	parts = append(parts, e.Message)
	return output.Sanitize(strings.Join(parts, ": "))
}

// outputOptions resolves rendering options: color from flag, config and
// environment, width from the terminal, quiet from the flag.
func (a *app) outputOptions(cfg *config.Config) output.Options {
	return output.Options{
		Color: colorEnabled(a.io.Out, a.flags.noColor, cfg.Output.Color),
		Width: terminalWidth(a.io.Out),
		Quiet: a.flags.quiet,
	}
}

// streamingFormatter is implemented by formatters that can print findings
// while the scan runs (ndjson). It is declared here with plain func types so
// the output package does not need to import anything from cli.
type streamingFormatter interface {
	NewStream(w io.Writer, opts output.Options) (onFinding func(findings.Finding), finish func() error)
}
