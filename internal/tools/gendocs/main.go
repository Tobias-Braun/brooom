// Command gendocs writes the CLI reference docs/cli.md from the cobra command
// tree, so the documentation cannot drift from the flags and help texts. Run
// it from anywhere inside the repository after changing a command:
//
//	go run ./internal/tools/gendocs
//
// A test in internal/cli fails while the committed file is out of date. The
// tool is a development helper and is not part of the released binary.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Tobias-Braun/brooom/internal/cli"
)

func main() {
	out := flag.String("o", "", "output file (default: docs/cli.md in the module root)")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "gendocs:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	if out == "" {
		root, err := moduleRoot()
		if err != nil {
			return err
		}
		out = filepath.Join(root, "docs", "cli.md")
	}
	doc := cli.ReferenceMarkdown(cli.NewRootCommand())
	if err := os.WriteFile(out, []byte(doc), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", out, err)
	}
	return nil
}

// moduleRoot walks up from the working directory to the directory holding
// go.mod.
func moduleRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("go.mod not found; run gendocs inside the repository or pass -o")
		}
		dir = parent
	}
}
