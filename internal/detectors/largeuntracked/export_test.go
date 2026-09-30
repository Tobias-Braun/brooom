package largeuntracked

import "context"

// SetOpenFiles replaces the open-file lookup and returns a restore function.
func SetOpenFiles(fn func(ctx context.Context, paths []string) (map[string]bool, error)) (restore func()) {
	old := openFiles
	openFiles = fn
	return func() { openFiles = old }
}
