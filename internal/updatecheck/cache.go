package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// CacheTTL is how long a background check result stays fresh.
const CacheTTL = 24 * time.Hour

// AttemptBackoff is how long after a fetch attempt no new attempt is made,
// whether the attempt succeeded, failed or was cancelled when the command
// exited. It bounds the cost of an offline, slow or rate limited network to
// one request per backoff instead of one per command.
const AttemptBackoff = time.Hour

// Cache is the content of ~/.brooom/cache/update.json.
type Cache struct {
	CheckedAt time.Time `json:"checked_at"`
	// LastAttempt is when the last fetch was started, even if it failed or was
	// cancelled. It is written before the request so an interrupted attempt is
	// still recorded.
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	Latest      string    `json:"latest"`
	URL         string    `json:"url"`
}

// ReadCache loads the cache file. A missing or corrupt file is an error the
// caller treats as "no cache".
func ReadCache(path string) (Cache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Cache{}, err
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		return Cache{}, fmt.Errorf("read update cache %s: %w", path, err)
	}
	return c, nil
}

// WriteCache stores c atomically (temp file in the same directory + rename)
// so a concurrent brooom or a killed process never leaves a torn file.
func WriteCache(path string, c Cache) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create cache dir %s: %w", dir, err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "update-*.tmp")
	if err != nil {
		return fmt.Errorf("write update cache in %s: %w", dir, err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write update cache %s: %w", tmp.Name(), err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("write update cache %s: %w", tmp.Name(), err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replace update cache %s: %w", path, err)
	}
	return nil
}

// Checker performs the cached background check. All collaborators are
// injectable so tests use a fake clock and an httptest server.
type Checker struct {
	Client    *http.Client
	BaseURL   string
	Now       func() time.Time
	CachePath string
}

// Cached returns the latest known release. A cache younger than CacheTTL is
// returned as is without any network access. Otherwise, unless an attempt was
// made within AttemptBackoff, the attempt is recorded in the cache first
// (keeping the previous latest/url) and GitHub is asked once; a command that
// exits before the answer arrives therefore still suppresses further
// requests. When no request is made and nothing is known, ErrNoData is
// returned; a previously cached release is returned as is. Failing to write
// the cache is ignored: the answer is still valid for this run.
func (c Checker) Cached(ctx context.Context) (Cache, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	cached, err := ReadCache(c.CachePath)
	if err != nil {
		cached = Cache{}
	}
	// A timestamp in the future (clock change) counts as stale.
	if age := now().Sub(cached.CheckedAt); cached.Latest != "" && age >= 0 && age < CacheTTL {
		return cached, nil
	}
	if age := now().Sub(cached.LastAttempt); !cached.LastAttempt.IsZero() && age >= 0 && age < AttemptBackoff {
		if cached.Latest == "" {
			return Cache{}, ErrNoData
		}
		return cached, nil
	}
	cached.LastAttempt = now().UTC()
	_ = WriteCache(c.CachePath, cached)

	base := c.BaseURL
	if base == "" {
		base = BaseURL()
	}
	client := c.Client
	if client == nil {
		client = NewClient(base)
	}
	rel, err := Latest(ctx, client, base)
	if err != nil {
		return Cache{}, err
	}
	entry := Cache{CheckedAt: now().UTC(), LastAttempt: cached.LastAttempt, Latest: rel.TagName, URL: rel.HTMLURL}
	_ = WriteCache(c.CachePath, entry)
	return entry, nil
}

// ErrNoData is returned by Cached when a recent attempt suppressed the request
// and no release is known yet.
var ErrNoData = errors.New("no update information cached yet")
