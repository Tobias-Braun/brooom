// Package updatecheck implements Brooom's opt-in update check.
//
// It is the only package that may import net/http (enforced by a test): the
// tool never phones home by default and has no telemetry. The single request
// it can make is an unauthenticated GET of the latest GitHub release of the
// Brooom repository, carrying no identifying data and no query parameters.
package updatecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Tobias-Braun/brooom/internal/buildinfo"
)

const (
	// DefaultBaseURL is the GitHub API root contacted unless overridden.
	DefaultBaseURL = "https://api.github.com"
	// BaseURLEnv overrides the API root. It exists for tests and mirrors and
	// is not meant for everyday use.
	BaseURLEnv = "BROOOM_UPDATE_URL"
	// LatestPath is the path of the latest-release endpoint.
	LatestPath = "/repos/Tobias-Braun/brooom/releases/latest"

	maxRedirects = 5
)

// Timeout bounds one whole request including redirects and reading the
// body. It is a variable so tests can shorten it.
var Timeout = 5 * time.Second

// MaxBody caps how much of a response is read. A release document is a few
// KiB; anything bigger is a misbehaving server or mirror.
var MaxBody int64 = 1 << 20

// Release is the subset of a GitHub release Brooom needs.
type Release struct {
	TagName    string `json:"tag_name"`
	HTMLURL    string `json:"html_url"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// BaseURL returns the API root: $BROOOM_UPDATE_URL if set, otherwise the
// GitHub API, without a trailing slash.
func BaseURL() string {
	if u := strings.TrimSpace(os.Getenv(BaseURLEnv)); u != "" {
		return strings.TrimRight(u, "/")
	}
	return DefaultBaseURL
}

// LatestURL is the full URL Latest requests for baseURL.
func LatestURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + LatestPath
}

// NewClient returns an HTTP client with the update-check timeout and the
// redirect policy for baseURL. It carries no cookie jar and no credentials.
func NewClient(baseURL string) *http.Client {
	return &http.Client{
		Timeout: Timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return checkRedirectTarget(req.URL, baseURL)
		},
	}
}

// checkRedirectTarget allows a redirect only to https on GitHub's own
// domains, or to the host of the override URL (which may be plain http in
// tests) so a hijacked or misconfigured response cannot send us elsewhere.
func checkRedirectTarget(target *url.URL, baseURL string) error {
	host := strings.ToLower(target.Hostname())
	if base, err := url.Parse(baseURL); err == nil && strings.EqualFold(base.Host, target.Host) &&
		(target.Scheme == "https" || base.Scheme == target.Scheme) {
		return nil
	}
	if target.Scheme == "https" && isGitHubHost(host) {
		return nil
	}
	return fmt.Errorf("refusing redirect to %s://%s", target.Scheme, target.Host)
}

func isGitHubHost(host string) bool {
	for _, d := range []string{"github.com", "githubusercontent.com"} {
		if host == d || strings.HasSuffix(host, "."+d) {
			return true
		}
	}
	return false
}

// Latest asks baseURL for the latest published release. The request is
// unauthenticated and sends only Accept and User-Agent headers. Errors never
// contain response bodies. Drafts and prereleases are rejected even though
// the endpoint already excludes them.
func Latest(ctx context.Context, client *http.Client, baseURL string) (Release, error) {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	target := LatestURL(baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Release{}, fmt.Errorf("build update request for %s: %w", target, err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "brooom/"+buildinfo.Get().Version)
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("contact %s: %w", target, unwrapURLError(err))
	}
	defer resp.Body.Close()
	if err := statusError(resp.StatusCode); err != nil {
		return Release{}, err
	}
	return decodeRelease(resp.Body)
}

// unwrapURLError drops the *url.Error wrapper, whose text repeats the URL
// the caller already names.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func statusError(code int) error {
	switch code {
	case http.StatusOK:
		return nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return fmt.Errorf("GitHub refused the request (HTTP %d), probably rate limited; try again later", code)
	case http.StatusNotFound:
		return fmt.Errorf("no release found (HTTP %d)", code)
	}
	return fmt.Errorf("unexpected response from GitHub (HTTP %d)", code)
}

func decodeRelease(body io.Reader) (Release, error) {
	data, err := io.ReadAll(io.LimitReader(body, MaxBody+1))
	if err != nil {
		return Release{}, fmt.Errorf("read update response: %w", err)
	}
	if int64(len(data)) > MaxBody {
		return Release{}, fmt.Errorf("update response is larger than %d bytes, ignoring it", MaxBody)
	}
	var rel Release
	if err := json.Unmarshal(data, &rel); err != nil {
		return Release{}, errors.New("update response is not valid JSON")
	}
	if rel.Draft || rel.Prerelease {
		return Release{}, errors.New("latest release is a draft or prerelease, ignoring it")
	}
	if _, err := ParseVersion(rel.TagName); err != nil {
		return Release{}, fmt.Errorf("latest release has an unusable tag: %w", err)
	}
	return rel, nil
}
