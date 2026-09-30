package trash

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/Tobias-Braun/brooom/internal/config"
)

// This file is the whole macOS Trash logic. It carries no build tag on
// purpose: only the constructor hook (ostrash_darwin.go) is darwin-specific,
// so everything here, including the osascript argument handling, the Finder
// style naming and the Restore refusals, is unit-tested on every CI platform
// through the injectable run/lstat/move functions. On other platforms the
// code is simply unreachable.

// trashAccessDenied is the actionable message for the macOS TCC limitation:
// since 10.15 ~/.Trash is protected, and a CLI without Full Disk Access gets
// EPERM when it inspects or moves items inside it. trashItemAtURL itself
// works, so removal succeeds while restore may not.
const trashAccessDenied = "macOS denies access to the Trash; restore with Finder 'Put Back' or grant Full Disk Access to your terminal"

const (
	// maxTrashBatch caps the paths per osascript invocation to keep argv small
	// and the per-invocation timeout meaningful.
	maxTrashBatch = 100
	// defaultScriptTimeout bounds one osascript invocation.
	defaultScriptTimeout = 30 * time.Second
	// maxUniqueName bounds the "name N" search so a pathological Trash cannot
	// loop forever.
	maxUniqueName = 100000
)

// trashItemScript is the JXA program. Paths arrive as ONE argv element holding
// a JSON array, so no path is ever interpolated into the source and quotes,
// `$`, backticks or newlines in names are inert. fileURLWithPath with
// isDirectory:false builds the URL from the string alone: it does not stat
// the path, so a symlink to a directory is trashed as the link, not its
// target. The result is written to a file whose path arrives as the second
// argv element (see execScript), because osascript prints a JXA return value
// on stderr and writing to stdout through NSFileHandle crashed osascript.
const trashItemScript = `ObjC.import('Foundation');
function run(argv) {
  var paths = JSON.parse(argv[0]);
  var fm = $.NSFileManager.defaultManager;
  var results = paths.map(function (p) {
    var item = {path: p, resulting: '', error: ''};
    try {
      var url = $.NSURL.fileURLWithPathIsDirectory(p, false);
      var out = Ref();
      var err = Ref();
      if (fm.trashItemAtURLResultingItemURLError(url, out, err)) {
        item.resulting = ObjC.unwrap(out[0].path);
      } else {
        item.error = ObjC.unwrap(err[0].localizedDescription);
      }
    } catch (e) {
      item.error = String(e);
    }
    return item;
  });
  // The result goes to the file named by argv[1] instead of stdout: the
  // NSFileHandle write on stdout crashed osascript on macOS runners, after
  // the items had already been trashed.
  var text = $.NSString.stringWithString(JSON.stringify(results) + '\n');
  text.writeToFileAtomicallyEncodingError(argv[1], true, $.NSUTF8StringEncoding, null);
}`

// scriptRunner executes osascript with argv and returns its stdout.
type scriptRunner func(ctx context.Context, argv []string) ([]byte, error)

// jxaResult is one element of the script's JSON output.
type jxaResult struct {
	Path      string `json:"path"`
	Resulting string `json:"resulting"`
	Error     string `json:"error"`
}

// macTrash trashes through NSFileManager (via osascript) so Finder's "Put
// Back" works and other volumes use their .Trashes, and falls back to moving
// into ~/.Trash when that is not possible.
type macTrash struct {
	osascript string
	timeout   time.Duration
	home      string
	now       func() time.Time
	// run, lstat and move are seams for tests: they replace the osascript
	// process, the Trash inspection (TCC) and the move helper.
	run   scriptRunner
	lstat func(string) (fs.FileInfo, error)
	move  func(ctx context.Context, src, dst string) error
	// mkdirAll recreates missing parents on Restore.
	mkdirAll func(path string, perm fs.FileMode) error
}

// newMacTrash returns a macTrash using the real osascript and filesystem.
func newMacTrash(home string) *macTrash {
	m := &macTrash{
		osascript: "osascript",
		timeout:   defaultScriptTimeout,
		home:      home,
		now:       time.Now,
		lstat:     os.Lstat,
		move:      moveTree,
		mkdirAll:  os.MkdirAll,
	}
	m.run = m.execScript
	return m
}

// Strategy implements Trasher.
func (m *macTrash) Strategy() config.TrashStrategy { return config.StrategyTrash }

// Remove implements Trasher as a batch of one.
func (m *macTrash) Remove(ctx context.Context, path string) (Record, error) {
	recs, errs := m.RemoveMany(ctx, []string{path})
	return recs[0], errs[0]
}

// pendingItem is a validated path waiting for the trash call. Size and type
// are measured before trashing because the item is gone afterwards.
type pendingItem struct {
	idx   int
	path  string
	size  int64
	isDir bool
}

// RemoveMany trashes several paths with as few osascript invocations as
// possible (at most maxTrashBatch paths each). The returned slices are as long
// as paths; a failing item never stops the others. The error of an item that
// was moved but whose source cleanup failed is returned together with its
// record.
func (m *macTrash) RemoveMany(ctx context.Context, paths []string) ([]Record, []error) {
	recs := make([]Record, len(paths))
	errs := make([]error, len(paths))
	var todo []pendingItem
	for i, p := range paths {
		it, err := m.prepare(i, p)
		if err != nil {
			errs[i] = err
			continue
		}
		todo = append(todo, it)
	}
	for len(todo) > 0 {
		n := min(len(todo), maxTrashBatch)
		m.trashChunk(ctx, todo[:n], recs, errs)
		todo = todo[n:]
	}
	return recs, errs
}

// prepare validates path and measures it.
func (m *macTrash) prepare(idx int, path string) (pendingItem, error) {
	// The paths travel as a JSON array, which can only carry valid UTF-8.
	// Failing this one item keeps an odd name from rejecting its whole batch.
	if !utf8.ValidString(path) {
		return pendingItem{}, fmt.Errorf("cannot trash %q: path is not valid UTF-8", path)
	}
	if err := checkMacRemovable(path); err != nil {
		return pendingItem{}, err
	}
	fi, err := checkRemovable(path)
	if err != nil {
		return pendingItem{}, err
	}
	size, err := treeSize(path)
	if err != nil {
		return pendingItem{}, fmt.Errorf("cannot measure %q: %w", path, err)
	}
	return pendingItem{idx: idx, path: path, size: size, isDir: fi.IsDir() && !isSymlink(fi)}, nil
}

// trashChunk trashes one batch and stores each item's outcome.
//
// When osascript completed (runErr == nil) its per-item results are
// authoritative and every success is recorded, even if the context was
// cancelled in the meantime: the items are in the Trash and must stay
// undoable. When the invocation itself failed part-way (timeout, kill,
// cancellation) some items may already have been trashed without brooom
// learning about it, so items whose original is gone are reported as possibly
// trashed and never fall back. A cancelled context never leads to a fallback,
// since the caller gave up.
func (m *macTrash) trashChunk(ctx context.Context, chunk []pendingItem, recs []Record, errs []error) {
	results, runErr := m.invoke(ctx, chunk)
	for k, it := range chunk {
		resulting, err := itemOutcome(it, results, runErr, k)
		if err == nil {
			recs[it.idx] = m.record(it, resulting)
			continue
		}
		if runErr != nil {
			if gone := originalGone(it.path); gone != nil {
				errs[it.idx] = fmt.Errorf("%w; %w", err, gone)
				continue
			}
		}
		if cerr := ctx.Err(); cerr != nil {
			errs[it.idx] = fmt.Errorf("cannot trash %q: %w", it.path, cerr)
			continue
		}
		m.fallback(ctx, it, err, recs, errs)
	}
}

// originalGone returns a non-nil error when path is no longer present, or
// cannot be inspected, after a failed osascript run. Both mean the item may
// already sit in the Trash without a Record, so a ~/.Trash fallback (which
// would fail with not-exist) must not be attempted.
func originalGone(path string) error {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%q is gone and may already be in the Trash (look in ~/.Trash or /Volumes/<volume>/.Trashes/<uid>; brooom cannot undo it)", path)
	}
	return fmt.Errorf("cannot tell whether %q was trashed: %w", path, err)
}

// itemOutcome extracts the resulting path of item k or the reason it failed.
func itemOutcome(it pendingItem, results []jxaResult, runErr error, k int) (string, error) {
	switch {
	case runErr != nil:
		return "", runErr
	case results[k].Error != "":
		return "", fmt.Errorf("cannot trash %q: %s", it.path, results[k].Error)
	case !filepath.IsAbs(results[k].Resulting):
		return "", fmt.Errorf("cannot trash %q: osascript returned no usable trash path", it.path)
	}
	return filepath.Clean(results[k].Resulting), nil
}

// record builds the Record of a successfully trashed item.
func (m *macTrash) record(it pendingItem, stored string) Record {
	return Record{
		Strategy:     config.StrategyTrash,
		OriginalPath: it.path,
		StoredPath:   stored,
		SizeBytes:    it.size,
		IsDir:        it.isDir,
		RemovedAt:    m.now().UTC(),
		// Access cannot be probed reliably at removal time (TCC); Restore
		// reports a denial explicitly instead.
		Restorable: true,
	}
}

// fallback moves the item into ~/.Trash after the native call failed for it.
// It never deletes permanently.
func (m *macTrash) fallback(ctx context.Context, it pendingItem, cause error, recs []Record, errs []error) {
	stored, err := m.trashToHome(ctx, it)
	var partial *SourceNotRemovedError
	if err == nil || errors.As(err, &partial) {
		recs[it.idx] = m.record(it, stored)
	}
	if err != nil {
		errs[it.idx] = fmt.Errorf("%w; fallback to ~/.Trash failed: %w", cause, err)
	}
}

// trashToHome moves the item into ~/.Trash under a unique Finder-style name.
// The result carries no Put Back metadata, so only brooom's own undo works.
func (m *macTrash) trashToHome(ctx context.Context, it pendingItem) (string, error) {
	if m.home == "" {
		return "", errors.New("home directory unknown")
	}
	dir := filepath.Join(m.home, ".Trash")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", trashDenied(it.path, err)
	}
	name, err := uniqueTrashName(filepath.Base(it.path), it.isDir, func(candidate string) (bool, error) {
		_, err := m.lstat(filepath.Join(dir, candidate))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	})
	if err != nil {
		return "", trashDenied(it.path, err)
	}
	dst := filepath.Join(dir, name)
	if err := m.move(ctx, it.path, dst); err != nil {
		var partial *SourceNotRemovedError
		if errors.As(err, &partial) {
			return dst, err
		}
		return "", trashDenied(it.path, err)
	}
	return dst, nil
}

// trashDenied wraps permission failures with the quarantine hint; other
// errors are only named.
func trashDenied(path string, err error) error {
	if isPermissionErr(err) {
		return fmt.Errorf("cannot move %q into ~/.Trash: %w; use --trash-strategy quarantine instead", path, err)
	}
	return fmt.Errorf("cannot move %q into ~/.Trash: %w", path, err)
}

// invoke runs one osascript call for the chunk and parses its output.
func (m *macTrash) invoke(ctx context.Context, chunk []pendingItem) ([]jxaResult, error) {
	paths := make([]string, len(chunk))
	for i, it := range chunk {
		paths[i] = it.path
	}
	argv, err := buildTrashArgv(paths)
	if err != nil {
		return nil, err
	}
	out, err := m.run(ctx, argv)
	if err != nil {
		return nil, err
	}
	return parseTrashOutput(out, paths)
}

// execScript runs osascript with a bounded timeout. The script writes its JSON
// result to a private temp file whose path is appended to argv, and that file
// is what is returned. Stderr is kept for diagnostics only.
//
// If osascript exits abnormally (it was seen to crash after trashing), a
// complete result file is still returned: the items are already in the Trash
// and dropping the result would lose their Records. parseTrashOutput validates
// the content strictly, so a partial or absent file still becomes an error.
// Timeouts and cancellation never use the file.
func (m *macTrash) execScript(ctx context.Context, argv []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	f, err := os.CreateTemp("", "brooom-trash-*.json")
	if err != nil {
		return nil, fmt.Errorf("cannot create osascript result file: %w", err)
	}
	outPath := f.Name()
	f.Close()
	defer os.Remove(outPath)

	cmd := exec.CommandContext(ctx, m.osascript, append(slices.Clone(argv), outPath)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if runErr != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("osascript timed out after %s", m.timeout)
		}
		return nil, fmt.Errorf("osascript interrupted: %w", ctx.Err())
	}
	out, readErr := os.ReadFile(outPath)
	if runErr != nil {
		if readErr == nil && len(bytes.TrimSpace(out)) > 0 {
			return out, nil
		}
		return nil, fmt.Errorf("osascript failed: %w: %s", runErr, strings.TrimSpace(stderr.String()))
	}
	if readErr != nil {
		return nil, fmt.Errorf("cannot read osascript result: %w", readErr)
	}
	return out, nil
}

// buildTrashArgv returns the osascript arguments: the script and the JSON
// array of paths as the single script argument.
func buildTrashArgv(paths []string) ([]string, error) {
	payload, err := json.Marshal(paths)
	if err != nil {
		return nil, fmt.Errorf("cannot encode paths: %w", err)
	}
	return []string{"-l", "JavaScript", "-e", trashItemScript, string(payload)}, nil
}

// parseTrashOutput strictly decodes the script output: exactly one JSON
// array, known fields only, one entry per requested path in request order.
func parseTrashOutput(out []byte, paths []string) ([]jxaResult, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	var results []jxaResult
	if err := dec.Decode(&results); err != nil {
		return nil, fmt.Errorf("unexpected osascript output: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("unexpected osascript output: trailing data after the result array")
	}
	if len(results) != len(paths) {
		return nil, fmt.Errorf("unexpected osascript output: %d results for %d paths", len(results), len(paths))
	}
	for i, r := range results {
		if r.Path != paths[i] {
			return nil, fmt.Errorf("unexpected osascript output: result %d is for %q, expected %q", i, r.Path, paths[i])
		}
	}
	return results, nil
}

// Restore implements Trasher. The stored copy must live inside a Trash
// directory; access denials there are reported as ErrNotRestorable with the
// Full Disk Access hint, never as success.
func (m *macTrash) Restore(ctx context.Context, r Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := checkRestoreRecord(r); err != nil {
		return err
	}
	if _, err := m.lstat(r.StoredPath); err != nil {
		return restoreStatErr(r, err)
	}
	if _, err := m.lstat(r.OriginalPath); err == nil {
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, ErrRestoreConflict)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("cannot check %q: %w", r.OriginalPath, err)
	}
	if err := m.mkdirAll(filepath.Dir(r.OriginalPath), 0o755); err != nil {
		return fmt.Errorf("cannot recreate parent of %q: %w", r.OriginalPath, err)
	}
	if err := m.move(ctx, r.StoredPath, r.OriginalPath); err != nil {
		if isPermissionErr(err) {
			return trashAccessErr(r, err)
		}
		return fmt.Errorf("cannot restore %q: %w", r.OriginalPath, err)
	}
	return nil
}

// checkRestoreRecord refuses records that would move something from outside
// a Trash directory or to a non-absolute place.
func checkRestoreRecord(r Record) error {
	if r.StoredPath == "" || r.OriginalPath == "" {
		return fmt.Errorf("record for %q has no stored path: %w", r.OriginalPath, ErrNotRestorable)
	}
	if !filepath.IsAbs(r.OriginalPath) || !filepath.IsAbs(r.StoredPath) {
		return fmt.Errorf("refusing to restore relative path %q from %q", r.OriginalPath, r.StoredPath)
	}
	if !isInsideTrash(r.StoredPath) || isTrashDirName(filepath.Base(r.StoredPath)) {
		return fmt.Errorf("refusing to restore from %q: not inside a .Trash or .Trashes directory", r.StoredPath)
	}
	return nil
}

// restoreStatErr classifies a failed Lstat of the stored copy.
func restoreStatErr(r Record, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%q is no longer in the Trash: %w", r.StoredPath, ErrNotRestorable)
	case isPermissionErr(err):
		return trashAccessErr(r, err)
	}
	return fmt.Errorf("cannot inspect %q: %w", r.StoredPath, err)
}

// trashAccessErr is the TCC error: it wraps ErrNotRestorable and keeps the
// underlying cause.
func trashAccessErr(r Record, cause error) error {
	return fmt.Errorf("cannot restore %q from %q: %s (%w): %w", r.OriginalPath, r.StoredPath, trashAccessDenied, cause, ErrNotRestorable)
}

// isPermissionErr reports EPERM/EACCES style errors. EPERM is what TCC
// returns, and it does not match fs.ErrPermission on every platform.
func isPermissionErr(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES)
}

// isTrashDirName reports whether name is ".Trash" or ".Trashes". The
// comparison ignores case because the default macOS volumes are case
// insensitive, so ".trash" addresses the very same directory.
func isTrashDirName(name string) bool {
	return strings.EqualFold(name, ".Trash") || strings.EqualFold(name, ".Trashes")
}

// isInsideTrash reports whether p is a Trash directory or below one. Any path
// component with a Trash name counts, also an unrelated directory that merely
// carries that name: refusing too much is the safe side. Symlinked parents
// that lead into a Trash are not resolved here; scope.Guard rejects symlinks
// that leave the scope before a path ever reaches the trasher.
func isInsideTrash(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(filepath.Clean(p)), "/") {
		if isTrashDirName(part) {
			return true
		}
	}
	return false
}

// checkMacRemovable adds the macOS-specific refusals to checkRemovable:
// volume roots and anything in or being a Trash directory.
func checkMacRemovable(path string) error {
	if path == "" || !filepath.IsAbs(path) {
		return nil // checkRemovable reports these with its own message
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	if clean == "/Volumes" || filepath.ToSlash(filepath.Dir(clean)) == "/Volumes" {
		return fmt.Errorf("refusing to remove volume root %q", path)
	}
	if isInsideTrash(clean) {
		return fmt.Errorf("refusing to remove %q: it is a Trash directory or already inside one", path)
	}
	return nil
}

// uniqueTrashName returns name, or "name 2", "name 3" ... (Finder style) for
// the first candidate that does not exist. Files keep their extension
// ("file 2.txt"); directories and dotfiles without one get the number
// appended. exists reports whether a candidate is taken; its error aborts the
// search, since an unreadable Trash must not lead to overwriting.
func uniqueTrashName(name string, isDir bool, exists func(string) (bool, error)) (string, error) {
	stem, ext := name, ""
	if !isDir {
		if e := filepath.Ext(name); e != name {
			stem, ext = strings.TrimSuffix(name, e), e
		}
	}
	for n := 1; n <= maxUniqueName; n++ {
		candidate := name
		if n > 1 {
			candidate = stem + " " + strconv.Itoa(n) + ext
		}
		taken, err := exists(candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no free name for %q in the Trash", name)
}
