package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func newRootsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "roots",
		Short: "Manage workspace roots used by --workspaces",
		Example: `  brooom roots list
  brooom roots add ~/code
  brooom roots remove ~/code`,
		Long: `Workspace roots are the only locations --workspaces may touch. Each root
must be an existing directory; filesystem roots such as / or C:\ are refused.
Roots are edited in place in the config file: every other key is preserved.`,
		Args: cobra.NoArgs,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "add <path>...",
			Short: "Add workspace roots",
			Example: `  brooom roots add ~/code ~/work
  brooom roots add .`,
			Args: cobra.MinimumNArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error { return a.runRootsAdd(args) },
		},
		&cobra.Command{
			Use:     "remove <path>...",
			Short:   "Remove workspace roots",
			Example: `  brooom roots remove ~/work`,
			Args:    cobra.MinimumNArgs(1),
			RunE:    func(cmd *cobra.Command, args []string) error { return a.runRootsRemove(args) },
		},
		&cobra.Command{
			Use:   "list",
			Short: "List workspace roots",
			Example: `  brooom roots list
  brooom roots list --format json`,
			Long: "List the configured roots with their status. Supports --format table (default), plain and json.",
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error { return a.runRootsList() },
		},
	)
	return cmd
}

// configPath returns the effective config file path: the --config flag, else
// ~/.brooom/config.json (or $BROOOM_HOME). The file need not exist.
func (a *app) configPath() (string, error) {
	if a.flags.configPath != "" {
		return a.flags.configPath, nil
	}
	dirs, err := config.ResolveDirs()
	if err != nil {
		return "", err
	}
	return dirs.ConfigFile, nil
}

// loadConfigForEdit returns the effective config path and the configuration
// loaded from it (defaults when the file is missing).
func loadConfigForEdit(a *app) (string, *config.Config, error) {
	path, err := a.configPath()
	if err != nil {
		return "", nil, err
	}
	cfg, err := config.Load(path)
	if err != nil {
		return path, nil, err
	}
	return path, cfg, nil
}

// configuredRootStrings returns the root paths as stored in the config, for
// shell completion of `roots remove` (#39). It is best effort and returns
// nothing when the config cannot be loaded.
func (a *app) configuredRootStrings() []string {
	_, cfg, err := loadConfigForEdit(a)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		out = append(out, r.Path)
	}
	return out
}

// pathKey normalises a path for equality checks. The default filesystems of
// Windows and macOS are case-insensitive, so a differently cased alias of a
// root must count as the same root.
func pathKey(p string) string {
	p = filepath.Clean(p)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(p)
	}
	return p
}

// canonicalKey returns the comparison key of an absolute path: symlinks
// resolved when the path exists, then case-folded where needed.
func canonicalKey(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return pathKey(p)
}

// rootKey is canonicalKey of a configured root, or "" if it cannot be
// expanded.
func rootKey(r config.Root) string {
	p, err := r.ResolvedPath()
	if err != nil {
		return ""
	}
	return canonicalKey(p)
}

// isTildePath reports whether s starts with a home reference.
func isTildePath(s string) bool {
	return s == "~" || strings.HasPrefix(s, "~/") || strings.HasPrefix(s, `~\`)
}

// candidateRoot is a validated `roots add` argument.
type candidateRoot struct {
	stored string // what is written to the config
	abs    string // absolute expanded path
	key    string // canonical comparison key
}

// prepareRoot validates one argument of `roots add`. The stored form keeps a
// leading "~" or an already absolute path as the user wrote it; anything else
// (relative paths, bare variable references) is stored absolute because a
// relative root would mean something different from another directory.
func prepareRoot(arg string) (candidateRoot, error) {
	if strings.TrimSpace(arg) == "" {
		return candidateRoot{}, errors.New("path must not be empty")
	}
	expanded, err := config.ExpandPath(arg)
	if err != nil {
		return candidateRoot{}, err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return candidateRoot{}, err
	}
	if err := requireDir(abs); err != nil {
		return candidateRoot{}, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		resolved = abs
	}
	if config.IsFilesystemRoot(abs) || config.IsFilesystemRoot(resolved) {
		return candidateRoot{}, fmt.Errorf("%s is a filesystem root; choose a workspace folder instead", abs)
	}
	stored := storedForm(arg, abs)
	return candidateRoot{stored: stored, abs: abs, key: pathKey(resolved)}, nil
}

// requireDir checks that path exists and is a directory.
func requireDir(path string) error {
	fi, err := os.Stat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s does not exist", path)
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

// storedForm is the string written to the config for arg (see prepareRoot).
func storedForm(arg, abs string) string {
	switch {
	case isTildePath(arg):
		return arg
	case filepath.IsAbs(arg):
		return filepath.Clean(arg)
	}
	return abs
}

// homeWarning is printed when the home directory itself becomes a root.
const homeWarning = "warning: %s is your home directory; scanning all of it with --workspaces is slow and broad, consider a subfolder such as ~/dev\n"

func (a *app) runRootsAdd(args []string) error {
	var cands []candidateRoot
	var problems []string
	for _, arg := range args {
		c, err := prepareRoot(arg)
		if err != nil {
			problems = append(problems, fmt.Sprintf("  %s: %v", arg, err))
			continue
		}
		cands = append(cands, c)
	}
	if len(problems) > 0 {
		return fmt.Errorf("nothing was added; invalid root path(s):\n%s", strings.Join(problems, "\n"))
	}
	home := homeKey()
	var added, existing []string
	err := a.rewriteRoots(func(roots []config.Root) ([]config.Root, bool, error) {
		seen := map[string]bool{}
		for _, r := range roots {
			seen[rootKey(r)] = true
		}
		for _, c := range cands {
			if seen[c.key] {
				existing = append(existing, c.stored)
				continue
			}
			seen[c.key] = true
			roots = append(roots, config.Root{Path: c.stored})
			added = append(added, c.stored)
			if home != "" && c.key == home {
				fmt.Fprintf(a.io.Err, homeWarning, c.abs)
			}
		}
		return roots, len(added) > 0, nil
	})
	if err != nil {
		return err
	}
	for _, p := range existing {
		a.say("%s is already a workspace root, nothing to do\n", p)
	}
	for _, p := range added {
		a.say("added root %s\n", p)
	}
	return nil
}

// homeKey is the comparison key of the user's home directory, or "" if it is
// unknown.
func homeKey() string {
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return ""
	}
	return canonicalKey(h)
}

// say prints a status line to stdout unless --quiet is set.
func (a *app) say(format string, args ...any) {
	if !a.flags.quiet {
		fmt.Fprintf(a.io.Out, format, args...)
	}
}

func (a *app) runRootsRemove(args []string) error {
	var removed []string
	err := a.rewriteRoots(func(roots []config.Root) ([]config.Root, bool, error) {
		drop := make([]bool, len(roots))
		var unknown []string
		for _, arg := range args {
			matched := false
			for i, r := range roots {
				if rootMatches(r, arg) {
					drop[i], matched = true, true
				}
			}
			if !matched {
				unknown = append(unknown, arg)
			}
		}
		if len(unknown) > 0 {
			return nil, false, unknownRootsError(unknown, roots)
		}
		kept := make([]config.Root, 0, len(roots))
		for i, r := range roots {
			if drop[i] {
				removed = append(removed, r.Path)
			} else {
				kept = append(kept, r)
			}
		}
		return kept, true, nil
	})
	if err != nil {
		return err
	}
	for _, p := range removed {
		a.say("removed root %s\n", p)
	}
	return nil
}

func unknownRootsError(unknown []string, roots []config.Root) error {
	var b strings.Builder
	fmt.Fprintf(&b, "not a configured root: %s\nnothing was removed; configured roots:", strings.Join(unknown, ", "))
	if len(roots) == 0 {
		b.WriteString(" (none)")
	}
	for _, r := range roots {
		b.WriteString("\n  " + r.Path)
	}
	return errors.New(b.String())
}

// rootMatches reports whether arg names root r: by the stored string, by the
// expanded path or by the symlink-resolved path. Matching by string first
// lets a root whose directory is gone still be removed.
func rootMatches(r config.Root, arg string) bool {
	if r.Path == arg {
		return true
	}
	expanded, err := config.ExpandPath(arg)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return false
	}
	rp, err := r.ResolvedPath()
	if err != nil {
		return false
	}
	if pathKey(abs) == pathKey(rp) {
		return true
	}
	return canonicalKey(abs) == canonicalKey(rp)
}

// kv is one top-level member of the config document; keeping the raw value
// and the member order is what lets roots be edited without touching any
// other key.
type kv struct {
	key string
	val json.RawMessage
}

// decodeOrdered decodes the top-level JSON object of data into ordered pairs.
func decodeOrdered(data []byte) ([]kv, error) {
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("top level must be a JSON object")
	}
	var out []kv
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, kv{key: kt.(string), val: raw})
	}
	return out, nil
}

// encodeOrdered renders pairs as an indented JSON object with a trailing
// newline.
func encodeOrdered(pairs []kv) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(p.key)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(p.val)
	}
	b.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, b.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// setRoots replaces the "roots" member (appending it when absent).
func setRoots(pairs []kv, roots []config.Root) ([]kv, error) {
	if roots == nil {
		roots = []config.Root{}
	}
	raw, err := json.Marshal(roots)
	if err != nil {
		return nil, err
	}
	for i := range pairs {
		if pairs[i].key == "roots" {
			pairs[i].val = raw
			return pairs, nil
		}
	}
	return append(pairs, kv{key: "roots", val: raw}), nil
}

// rewriteRoots applies mutate to the configured roots and writes the file
// back. mutate returns the new roots and whether anything changed; when not,
// the file is left untouched. It edits at the JSON level (never through
// config.Save, whose minimal-diff output would drop keys the user pinned to a
// default value): only the "roots" value changes, the result is validated
// with the same code as config.Load and only then written atomically. A
// missing file is treated as {"version":1}.
func (a *app) rewriteRoots(mutate func([]config.Root) ([]config.Root, bool, error)) error {
	path, err := a.configPath()
	if err != nil {
		return err
	}
	data, err := readConfigOrDefault(path)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(path, data)
	if err != nil {
		return err
	}
	roots, changed, err := mutate(cfg.Roots)
	if err != nil || !changed {
		return err
	}
	out, err := withRoots(path, data, roots)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, out, 0o600)
}

// readConfigOrDefault returns the file content, or a minimal document when
// the file does not exist yet.
func readConfigOrDefault(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return []byte(`{"version":1}`), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// withRoots returns data with only the roots value replaced, validated with
// the rules of config.Load.
func withRoots(path string, data []byte, roots []config.Root) ([]byte, error) {
	pairs, err := decodeOrdered(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if pairs, err = setRoots(pairs, roots); err != nil {
		return nil, err
	}
	out, err := encodeOrdered(pairs)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", path, err)
	}
	if _, err := config.Parse(path, out); err != nil {
		return nil, err
	}
	return out, nil
}

// rootInfo is one entry of `roots list --format json`; status feeds the
// table only.
type rootInfo struct {
	Path     string `json:"path"`
	Resolved string `json:"resolved"`
	Exists   bool   `json:"exists"`
	status   string
}

func describeRoot(r config.Root) rootInfo {
	info := rootInfo{Path: r.Path}
	resolved, err := r.ResolvedPath()
	if err != nil {
		info.status = "invalid: " + err.Error()
		return info
	}
	info.Resolved = resolved
	fi, err := os.Stat(resolved)
	switch {
	case err != nil:
		info.status = "missing"
	case !fi.IsDir():
		info.status = "not a directory"
	default:
		info.status, info.Exists = "exists", true
	}
	return info
}

func (a *app) runRootsList() error {
	format := a.flags.format
	if format == "" {
		format = "table"
	}
	if format != "table" && format != "plain" && format != "json" {
		return usageError{fmt.Errorf("unsupported format %q for roots list (supported: table, plain, json)", format)}
	}
	_, cfg, err := loadConfigForEdit(a)
	if err != nil {
		return err
	}
	infos := make([]rootInfo, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		infos = append(infos, describeRoot(r))
	}
	return renderRoots(a.io.Out, format, infos)
}

func renderRoots(w io.Writer, format string, infos []rootInfo) error {
	switch format {
	case "json":
		return writeJSON(w, infos)
	case "plain":
		for _, i := range infos {
			fmt.Fprintln(w, i.Path)
		}
		return nil
	}
	if len(infos) == 0 {
		_, err := fmt.Fprintln(w, "No workspace roots configured. Add one with: brooom roots add <path>")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PATH\tRESOLVED\tSTATUS")
	for _, i := range infos {
		fmt.Fprintf(tw, "%s\t%s\t%s\n", i.Path, i.Resolved, i.status)
	}
	return tw.Flush()
}
