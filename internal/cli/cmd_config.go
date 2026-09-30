package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/Tobias-Braun/brooom/internal/config"
)

func newConfigCmd(a *app) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Create, show, edit and validate the configuration",
		Args:  cobra.NoArgs,
	}
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write a config file with the defaults",
		Long: `Write the complete default configuration to the config file (creating the
directory). An existing file is never overwritten unless --force is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return a.runConfigInit(force) },
	}
	initCmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config file")
	cmd.AddCommand(
		initCmd,
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective configuration",
			Long:  "Print the effective global configuration (defaults merged with the file). Supports --format json (default) and table.",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return a.runConfigShow() },
		},
		&cobra.Command{
			Use:   "edit",
			Short: "Open the config file in $VISUAL / $EDITOR",
			Long: `Open the config file in $VISUAL, else $EDITOR, else notepad (Windows) or vi.
The file is created with the defaults first if it does not exist. After the
editor exits the file is validated; problems are reported but your edit is
never reverted.`,
			Args: cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error { return a.runConfigEdit() },
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Validate the config file",
			Args:  cobra.NoArgs,
			RunE:  func(cmd *cobra.Command, args []string) error { return a.runConfigValidate() },
		},
		&cobra.Command{
			Use:   "path",
			Short: "Print the config file path",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				path, err := a.configPath()
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(a.io.Out, path)
				return err
			},
		},
	)
	return cmd
}

func (a *app) runConfigInit(force bool) error {
	path, err := a.configPath()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil && !force {
		return usageError{fmt.Errorf("%s already exists; use --force to overwrite it", path)}
	}
	// SaveFull, not Save: Save writes only non-default values, which for the
	// defaults is just {"version":1} and would make the file useless as a
	// starting point for editing.
	if err := config.SaveFull(path, config.Default()); err != nil {
		return err
	}
	_, err = fmt.Fprintf(a.io.Out, "wrote %s\n", path)
	return err
}

func (a *app) runConfigShow() error {
	format := a.flags.format
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "table" {
		return usageError{fmt.Errorf("unsupported format %q for config show (supported: json, table)", format)}
	}
	_, cfg, err := loadConfigForEdit(a)
	if err != nil {
		return err
	}
	data, err := config.Marshal(cfg, true)
	if err != nil {
		return err
	}
	if format == "json" {
		_, err = a.io.Out.Write(data)
		return err
	}
	return writeConfigTable(a.io.Out, data)
}

// writeConfigTable prints the document as flat "dotted.key  value" rows.
func writeConfigTable(w io.Writer, data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return err
	}
	var rows [][2]string
	flatten("", doc, &rows)
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\n", r[0], r[1])
	}
	return tw.Flush()
}

// flatten collects the leaves of v under dotted keys. Arrays and empty
// objects are leaves rendered as compact JSON so a row is always one line.
func flatten(prefix string, v any, rows *[][2]string) {
	if m, ok := v.(map[string]any); ok && len(m) > 0 {
		for k, sub := range m {
			key := k
			if prefix != "" {
				key = prefix + "." + k
			}
			flatten(key, sub, rows)
		}
		return
	}
	if s, ok := v.(string); ok {
		*rows = append(*rows, [2]string{prefix, s})
		return
	}
	b, _ := json.Marshal(v)
	*rows = append(*rows, [2]string{prefix, string(b)})
}

// reportProblems prints every validation problem of err to stderr, one per
// line, and returns a short summary error. Errors that are not validation
// errors (syntax errors, unknown keys) are returned unchanged.
func (a *app) reportProblems(path string, err error) error {
	var ve *config.ValidationError
	if !errors.As(err, &ve) {
		return err
	}
	for _, p := range ve.Problems {
		fmt.Fprintf(a.io.Err, "%s: %s: %s\n", path, p.Field, p.Message)
	}
	return fmt.Errorf("%s is invalid (%d problem(s))", path, len(ve.Problems))
}

func (a *app) runConfigValidate() error {
	path, err := a.configPath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		_, err = fmt.Fprintf(a.io.Out, "ok (no config file at %s; defaults apply)\n", path)
		return err
	}
	if _, err := config.Load(path); err != nil {
		return a.reportProblems(path, err)
	}
	_, err = fmt.Fprintln(a.io.Out, "ok")
	return err
}

func (a *app) runConfigEdit() error {
	path, err := a.configPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := config.SaveFull(path, config.Default()); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	argv := editorCommand(os.Getenv, runtime.GOOS)
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.io.In, a.io.Out, a.io.Err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q failed: %w", strings.Join(argv, " "), err)
	}
	// The file stays as edited even when invalid: reverting would destroy
	// the user's work, and they can run `config edit` again to fix it.
	if _, err := config.Load(path); err != nil {
		return a.reportProblems(path, err)
	}
	return nil
}

// editorCommand returns the editor command line: $VISUAL, then $EDITOR, else
// notepad on Windows and vi elsewhere. Blank values count as unset.
func editorCommand(env func(string) string, goos string) []string {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if argv := splitCommand(env(name), goos); len(argv) > 0 {
			return argv
		}
	}
	if goos == "windows" {
		return []string{"notepad"}
	}
	return []string{"vi"}
}

// splitCommand splits s into words with shell-like quoting: single quotes are
// literal, double quotes group, and (except on Windows, where backslashes are
// path separators) a backslash escapes the next character. An unterminated
// quote runs to the end of the string.
func splitCommand(s, goos string) []string {
	sp := &splitter{rs: []rune(s), escapes: goos != "windows"}
	for sp.i = 0; sp.i < len(sp.rs); sp.i++ {
		sp.step(sp.rs[sp.i])
	}
	sp.endWord()
	return sp.words
}

// splitter is the state machine behind splitCommand.
type splitter struct {
	rs      []rune
	i       int
	escapes bool // backslash escapes the next character
	quote   rune // active quote character, 0 outside quotes
	inWord  bool
	cur     strings.Builder
	words   []string
}

func (sp *splitter) endWord() {
	if sp.inWord {
		sp.words = append(sp.words, sp.cur.String())
		sp.cur.Reset()
		sp.inWord = false
	}
}

func (sp *splitter) step(c rune) {
	switch {
	case sp.quote != 0 && c == sp.quote:
		sp.quote = 0
	case sp.quote == '\'':
		sp.cur.WriteRune(c)
	case c == '\\' && sp.escapes && sp.i+1 < len(sp.rs):
		sp.i++
		sp.cur.WriteRune(sp.rs[sp.i])
		sp.inWord = true
	case sp.quote == 0 && (c == '\'' || c == '"'):
		sp.quote, sp.inWord = c, true
	case sp.quote == 0 && (c == ' ' || c == '\t'):
		sp.endWord()
	default:
		sp.cur.WriteRune(c)
		sp.inWord = true
	}
}
