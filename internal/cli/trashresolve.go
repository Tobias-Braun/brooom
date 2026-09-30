package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Tobias-Braun/brooom/internal/config"
	"github.com/Tobias-Braun/brooom/internal/trash"
)

// deleteWarnedMarker is created in the Brooom home the first time the delete
// strategy is used, so the permanence warning is shown once per installation
// and not on every run.
const deleteWarnedMarker = ".delete-warned"

// deleteWarning is printed to stderr on the first use of the delete strategy.
const deleteWarning = "warning: the delete strategy removes files permanently and they cannot be restored"

// strategyList names the valid strategies in error messages.
const strategyList = "trash, quarantine, delete"

// parseTrashStrategy validates the --trash-strategy flag value. An empty
// value means "not given". An invalid one is a usage error so it fails
// before any scan work.
func parseTrashStrategy(v string) (config.TrashStrategy, error) {
	switch s := config.TrashStrategy(strings.ToLower(strings.TrimSpace(v))); s {
	case "", config.StrategyTrash, config.StrategyQuarantine, config.StrategyDelete:
		return s, nil
	default:
		return "", usageError{fmt.Errorf("invalid --trash-strategy %q (use %s)", v, strategyList)}
	}
}

// trasherResolver hands out trashers for the executor. Trashers are built
// lazily, once per strategy, so a run that never removes a file (a branch
// cleanup) never touches the OS trash or creates the quarantine directory.
type trasherResolver struct {
	cfg *config.Config
	// flag is the validated --trash-strategy; it beats every config value.
	flag      config.TrashStrategy
	dirs      config.Dirs
	sessionID string
	stderr    io.Writer

	warnOnce sync.Once

	mu    sync.Mutex
	cache map[config.TrashStrategy]trash.Trasher
}

func newTrasherResolver(cfg *config.Config, flag config.TrashStrategy, dirs config.Dirs, sessionID string, stderr io.Writer) *trasherResolver {
	return &trasherResolver{cfg: cfg, flag: flag, dirs: dirs, sessionID: sessionID, stderr: stderr,
		cache: map[config.TrashStrategy]trash.Trasher{}}
}

// strategyFor applies the precedence flag > per-detector config > global
// config > trash.
func (r *trasherResolver) strategyFor(detector string) config.TrashStrategy {
	if r.flag != "" {
		return r.flag
	}
	if s, ok := r.cfg.Trash.PerDetector[detector]; ok && s != "" {
		return s
	}
	if r.cfg.Trash.Strategy != "" {
		return r.cfg.Trash.Strategy
	}
	return config.StrategyTrash
}

// forDetector is action.Env.Trasher. The delete strategy is gated here as
// well as in config validation because the flag may be the only opt-in: a
// permanent delete must never come from a config value alone.
func (r *trasherResolver) forDetector(detector string) (trash.Trasher, error) {
	s := r.strategyFor(detector)
	if s == config.StrategyDelete && r.flag != config.StrategyDelete && !r.cfg.Trash.AllowDelete {
		return nil, errors.New("the delete strategy is selected in the config but not enabled: " +
			`set "trash.allow_delete": true in the config or pass --trash-strategy delete for this run`)
	}
	return r.get(s)
}

// forStrategy is action.Env.TrasherFor. Undo restores with the strategy of
// the manifest entry, and restoring never deletes anything, so neither the
// delete gate nor the warning apply.
func (r *trasherResolver) forStrategy(s config.TrashStrategy) (trash.Trasher, error) {
	return r.get(s)
}

// get returns the cached trasher of a strategy or builds it. It never warns:
// trashers are resolved while planning, which also happens in dry runs that
// promise nothing was changed. The warning belongs to beforeDelete.
func (r *trasherResolver) get(s config.TrashStrategy) (trash.Trasher, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.cache[s]; ok {
		return t, nil
	}
	t, err := trash.New(s, trash.Options{SessionID: r.sessionID, QuarantineDir: r.dirs.Quarantine})
	if err != nil {
		return nil, err
	}
	r.cache[s] = t
	return t, nil
}

// beforeDelete is action.Env.BeforeDelete: the trash action calls it right
// before it permanently deletes something, so the warning only appears on
// apply and never in a dry run, whose output may be discarded (json pipe,
// `clean --from`). It warns at most once per run.
func (r *trasherResolver) beforeDelete() {
	r.warnOnce.Do(r.warnDelete)
}

// warnDelete prints the warning unless the marker file exists, then creates
// the marker, so the marker only ever exists after the warning was shown. A
// marker that cannot be written only means the warning shows again next time;
// it never blocks the run.
func (r *trasherResolver) warnDelete() {
	marker := filepath.Join(r.dirs.Home, deleteWarnedMarker)
	if _, err := os.Stat(marker); err == nil {
		return
	}
	fmt.Fprintln(r.stderr, deleteWarning)
	if err := os.MkdirAll(r.dirs.Home, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(marker, nil, 0o600)
}
