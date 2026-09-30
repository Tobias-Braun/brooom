//go:build darwin

package procs

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// openFiles asks lsof. Input paths must be symlink-resolved by the caller:
// lsof reports the real location, so /var versus /private/var differences
// would otherwise hide an open file.
func openFiles(ctx context.Context, files, dirs []string, res map[string]bool) error {
	bin, err := findLsof()
	if err != nil {
		return err
	}
	return lsofOpenFiles(ctx, execLsof(bin), files, dirs, res)
}

// platformListing is one lsof run listing every open file, working directory
// and memory map of every process lsof may inspect. A Snapshot answers all
// queries of a scan from it.
func platformListing(ctx context.Context) ([]string, error) {
	bin, err := findLsof()
	if err != nil {
		return nil, err
	}
	return runNames(ctx, execLsof(bin), lsofListAllArgs)
}

// findLsof prefers the system binary over whatever PATH offers, so a
// user-controlled PATH entry cannot substitute the tool; missing lsof is
// ErrUnavailable.
func findLsof() (string, error) {
	const system = "/usr/sbin/lsof"
	if info, err := os.Stat(system); err == nil && !info.IsDir() {
		return system, nil
	}
	p, err := exec.LookPath("lsof")
	if err != nil {
		return "", fmt.Errorf("%w: lsof not found: %w", ErrUnavailable, err)
	}
	return p, nil
}

// execLsof is the real runner: the command is killed when ctx ends, the
// locale is pinned and stderr is discarded (permission noise for processes of
// other users is expected).
func execLsof(bin string) lsofRunner {
	return func(ctx context.Context, args []string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Env = append(cmd.Environ(), "LC_ALL=C")
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		err := cmd.Run()
		return stdout.Bytes(), err
	}
}
