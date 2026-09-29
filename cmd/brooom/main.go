// Command brooom sweeps disk clutter left behind by AI-assisted development:
// agent logs and runtime files, stale and merged branches, leftover
// worktrees, bloated git histories and build artifacts.
package main

import (
	"os"

	"github.com/Tobias-Braun/brooom/internal/cli"
)

func main() {
	os.Exit(cli.Main(os.Args[1:], cli.StdIO()))
}
