package updatecheck

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

var t0 = time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)

func countingServer(t *testing.T, tag string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"` + tag + `","html_url":"https://example.test/` + tag + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestCacheRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "update.json")
	in := Cache{CheckedAt: t0, Latest: "v1.2.3", URL: "https://example.test"}
	if err := WriteCache(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := ReadCache(path)
	if err != nil || out != in {
		t.Fatalf("round trip: %+v, %v", out, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestReadCacheErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadCache(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file must error")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadCache(bad); err == nil {
		t.Error("corrupt file must error")
	}
}

func TestCheckerCachedFreshSkipsNetwork(t *testing.T) {
	srv, hits := countingServer(t, "v9.0.0")
	path := filepath.Join(t.TempDir(), "update.json")
	if err := WriteCache(path, Cache{CheckedAt: t0, Latest: "v1.5.0", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	c := Checker{BaseURL: srv.URL, CachePath: path, Now: func() time.Time { return t0.Add(23 * time.Hour) }}
	got, err := c.Cached(context.Background())
	if err != nil || got.Latest != "v1.5.0" || hits.Load() != 0 {
		t.Fatalf("got %+v err %v hits %d", got, err, hits.Load())
	}
}

func TestCheckerCachedExpiryFetchesAndWrites(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
	}{
		{"exactly 24h", CacheTTL},
		{"older", 48 * time.Hour},
		{"checked in the future", -time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, hits := countingServer(t, "v2.0.0")
			path := filepath.Join(t.TempDir(), "update.json")
			if err := WriteCache(path, Cache{CheckedAt: t0, Latest: "v1.0.0"}); err != nil {
				t.Fatal(err)
			}
			now := t0.Add(tt.age)
			c := Checker{BaseURL: srv.URL, CachePath: path, Now: func() time.Time { return now }}
			got, err := c.Cached(context.Background())
			if err != nil || got.Latest != "v2.0.0" || hits.Load() != 1 {
				t.Fatalf("got %+v err %v hits %d", got, err, hits.Load())
			}
			stored, err := ReadCache(path)
			if err != nil || stored.Latest != "v2.0.0" || !stored.CheckedAt.Equal(now) {
				t.Errorf("stored = %+v, %v", stored, err)
			}
		})
	}
}

func TestCheckerCachedNoCacheAndCorruptCache(t *testing.T) {
	srv, hits := countingServer(t, "v3.0.0")
	dir := t.TempDir()
	path := filepath.Join(dir, "update.json")
	c := Checker{BaseURL: srv.URL, CachePath: path, Now: func() time.Time { return t0 }}
	if got, err := c.Cached(context.Background()); err != nil || got.Latest != "v3.0.0" {
		t.Fatalf("no cache: %+v %v", got, err)
	}
	if err := os.WriteFile(path, []byte("garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Cached(context.Background()); err != nil || got.Latest != "v3.0.0" || hits.Load() != 2 {
		t.Fatalf("corrupt cache: %+v %v hits %d", got, err, hits.Load())
	}
}

// A failed fetch keeps the known release but records the attempt, and the
// recorded attempt suppresses further requests for AttemptBackoff.
func TestCheckerCachedFailureRecordsAttempt(t *testing.T) {
	srv, hits := countingServer(t, "v9.0.0")
	base := srv.URL
	srv.Close()
	path := filepath.Join(t.TempDir(), "update.json")
	if err := WriteCache(path, Cache{CheckedAt: t0, Latest: "v1.0.0", URL: "u"}); err != nil {
		t.Fatal(err)
	}
	now := t0.Add(48 * time.Hour)
	c := Checker{BaseURL: base, CachePath: path, Now: func() time.Time { return now }}
	if _, err := c.Cached(context.Background()); err == nil {
		t.Fatal("want fetch error")
	}
	got, _ := ReadCache(path)
	if got.Latest != "v1.0.0" || got.URL != "u" || !got.CheckedAt.Equal(t0) || !got.LastAttempt.Equal(now) {
		t.Errorf("cache after failure: %+v", got)
	}

	// Within the backoff no request is made and the old release is returned.
	live, liveHits := countingServer(t, "v9.0.0")
	c.BaseURL = live.URL
	now = now.Add(AttemptBackoff / 2)
	if entry, err := c.Cached(context.Background()); err != nil || entry.Latest != "v1.0.0" || liveHits.Load() != 0 {
		t.Fatalf("within backoff: %+v %v hits %d", entry, err, liveHits.Load())
	}
	// After the backoff a new attempt is made.
	now = now.Add(AttemptBackoff)
	if entry, err := c.Cached(context.Background()); err != nil || entry.Latest != "v9.0.0" || liveHits.Load() != 1 {
		t.Fatalf("after backoff: %+v %v hits %d", entry, err, liveHits.Load())
	}
	_ = hits
}

// With nothing cached, a recent failed attempt yields ErrNoData, not a request.
func TestCheckerCachedBackoffWithoutData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.json")
	if err := WriteCache(path, Cache{LastAttempt: t0}); err != nil {
		t.Fatal(err)
	}
	srv, hits := countingServer(t, "v2.0.0")
	c := Checker{BaseURL: srv.URL, CachePath: path, Now: func() time.Time { return t0.Add(time.Minute) }}
	if _, err := c.Cached(context.Background()); !errors.Is(err, ErrNoData) || hits.Load() != 0 {
		t.Fatalf("err %v hits %d", err, hits.Load())
	}
}

func TestCheckerCachedUnwritableCacheStillAnswers(t *testing.T) {
	srv, _ := countingServer(t, "v4.0.0")
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// A cache path below a regular file can never be created.
	c := Checker{BaseURL: srv.URL, CachePath: filepath.Join(blocker, "update.json")}
	got, err := c.Cached(context.Background())
	if err != nil || got.Latest != "v4.0.0" {
		t.Fatalf("got %+v err %v", got, err)
	}
}
