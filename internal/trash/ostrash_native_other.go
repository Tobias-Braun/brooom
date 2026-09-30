//go:build !darwin

package trash

import "errors"

// nativeTrashItem is the NSFileManager call, which only exists on macOS. The
// error sends every item through the ~/.Trash fallback; the macOS trasher is
// unreachable on other platforms anyway and only its tests run here.
func nativeTrashItem(string) (string, error) {
	return "", errors.New("the native macOS trash is only available on macOS")
}
