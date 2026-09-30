package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func releaseServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != LatestPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func latest(t *testing.T, base string) (Release, error) {
	t.Helper()
	return Latest(context.Background(), NewClient(base), base)
}

func TestLatestSuccess(t *testing.T) {
	srv := releaseServer(t, 200, `{"tag_name":"v1.4.0","html_url":"https://github.com/x/y/releases/v1.4.0","prerelease":false,"draft":false,"body":"ignored"}`)
	rel, err := latest(t, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.4.0" || !strings.HasSuffix(rel.HTMLURL, "v1.4.0") {
		t.Errorf("release = %+v", rel)
	}
}

func TestLatestRequestShape(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	defer srv.Close()
	if _, err := latest(t, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if got.Method != http.MethodGet || got.URL.Path != LatestPath || got.URL.RawQuery != "" {
		t.Errorf("request = %s %s?%s", got.Method, got.URL.Path, got.URL.RawQuery)
	}
	if got.Header.Get("Accept") != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got.Header.Get("Accept"))
	}
	if !strings.HasPrefix(got.Header.Get("User-Agent"), "brooom/") {
		t.Errorf("User-Agent = %q", got.Header.Get("User-Agent"))
	}
	for _, h := range []string{"Authorization", "Cookie", "X-Github-Token"} {
		if got.Header.Get(h) != "" {
			t.Errorf("header %s must not be sent", h)
		}
	}
}

func TestLatestErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"server error", 500, "SECRET-BODY-500", "HTTP 500"},
		{"rate limit 403", 403, "SECRET-BODY-403", "rate limited"},
		{"rate limit 429", 429, "SECRET-BODY-429", "rate limited"},
		{"not found", 404, "SECRET-BODY-404", "HTTP 404"},
		{"bad json", 200, "SECRET-not-json", "not valid JSON"},
		{"invalid tag", 200, `{"tag_name":"nightly"}`, "unusable tag"},
		{"empty tag", 200, `{}`, "unusable tag"},
		{"draft", 200, `{"tag_name":"v1.0.0","draft":true}`, "draft or prerelease"},
		{"prerelease", 200, `{"tag_name":"v1.0.0-rc.1","prerelease":true}`, "draft or prerelease"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := releaseServer(t, tt.status, tt.body)
			_, err := latest(t, srv.URL)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("error leaks the response body: %v", err)
			}
		})
	}
}

func TestLatestOversizedBody(t *testing.T) {
	old := MaxBody
	MaxBody = 64
	t.Cleanup(func() { MaxBody = old })
	srv := releaseServer(t, 200, `{"tag_name":"v1.0.0","pad":"`+strings.Repeat("x", 200)+`"}`)
	_, err := latest(t, srv.URL)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error = %v, want size error", err)
	}
}

func TestLatestTimeout(t *testing.T) {
	old := Timeout
	Timeout = 100 * time.Millisecond
	t.Cleanup(func() { Timeout = old })
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(release)
	start := time.Now()
	_, err := latest(t, srv.URL)
	if err == nil {
		t.Fatal("want timeout error")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("timeout not honoured, took %v", elapsed)
	}
}

func TestLatestServerDown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close()
	if _, err := latest(t, base); err == nil {
		t.Fatal("want connection error")
	}
}

func TestBaseURLOverride(t *testing.T) {
	t.Setenv(BaseURLEnv, "")
	if got := BaseURL(); got != DefaultBaseURL {
		t.Errorf("default = %q", got)
	}
	srv := releaseServer(t, 200, `{"tag_name":"v9.9.9"}`)
	t.Setenv(BaseURLEnv, srv.URL+"/")
	if got := BaseURL(); got != srv.URL {
		t.Errorf("override = %q, want %q", got, srv.URL)
	}
	rel, err := latest(t, BaseURL())
	if err != nil || rel.TagName != "v9.9.9" {
		t.Errorf("rel = %+v, err = %v", rel, err)
	}
}

func TestLatestURL(t *testing.T) {
	want := "https://api.github.com/repos/Tobias-Braun/brooom/releases/latest"
	if got := LatestURL(DefaultBaseURL + "/"); got != want {
		t.Errorf("LatestURL = %q, want %q", got, want)
	}
}

func TestRedirectPolicy(t *testing.T) {
	tests := []struct {
		name    string
		base    string
		target  string
		allowed bool
	}{
		{"github https", DefaultBaseURL, "https://api.github.com/other", true},
		{"github subdomain", DefaultBaseURL, "https://objects.githubusercontent.com/x", true},
		{"github plain http", DefaultBaseURL, "http://api.github.com/x", false},
		{"foreign host", DefaultBaseURL, "https://evil.example.com/x", false},
		{"lookalike host", DefaultBaseURL, "https://notgithub.com/x", false},
		{"override same host http", "http://127.0.0.1:8080", "http://127.0.0.1:8080/x", true},
		{"override host upgrade to https", "http://127.0.0.1:8080", "https://127.0.0.1:8080/x", true},
		{"override other host", "http://127.0.0.1:8080", "http://127.0.0.1:9999/x", false},
		{"https override downgrade", "https://mirror.example.com", "http://mirror.example.com/x", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, tt.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = NewClient(tt.base).CheckRedirect(req, nil)
			if (err == nil) != tt.allowed {
				t.Errorf("allowed = %v, want %v (err %v)", err == nil, tt.allowed, err)
			}
		})
	}
}

func TestRedirectFollowedAndLimited(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc(LatestPath, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v2.0.0"}`))
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if rel, err := latest(t, srv.URL); err != nil || rel.TagName != "v2.0.0" {
		t.Errorf("same-host redirect: rel=%+v err=%v", rel, err)
	}

	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, LatestPath, http.StatusFound)
	}))
	defer loop.Close()
	if _, err := latest(t, loop.URL); err == nil {
		t.Error("redirect loop must fail")
	}

	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"tag_name":"v3.0.0"}`))
	}))
	defer foreign.Close()
	hop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL+LatestPath, http.StatusFound)
	}))
	defer hop.Close()
	if _, err := latest(t, hop.URL); err == nil || !strings.Contains(err.Error(), "refusing redirect") {
		t.Errorf("cross-host redirect error = %v", err)
	}
}
