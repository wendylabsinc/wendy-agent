package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

const githubAPIHost = "api.github.com"

func newGitHubAPIClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if !isCanonicalGitHubAPIURL(req.URL) {
				req.Header.Del("Authorization")
			}
			return nil
		},
	}
}

// newGitHubAPIGetRequest creates an authenticated GitHub API request when
// GITHUB_TOKEN is set. Callers should avoid logging the returned request because
// it may contain an Authorization header.
func newGitHubAPIGetRequest(rawURL string) (*http.Request, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if !isCanonicalGitHubAPIURL(parsed) {
		return nil, fmt.Errorf("unsupported GitHub API URL: scheme=%q host=%q", parsed.Scheme, parsed.Host)
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", githubAPIUserAgent())
	// net/http header values are strings, so Go cannot zero this secret after use;
	// keep it scoped to this request and avoid logging the returned request.
	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = ghCLIToken()
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

// ghCLIToken returns a GitHub token from the gh CLI when one is available.
// Overridable in tests. The result is cached for the process lifetime so
// repeated API calls don't spawn gh more than once.
var ghCLIToken = sync.OnceValue(func() string {
	ghPath, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, ghPath, "auth", "token").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// githubAPICachePath returns the path of the GitHub API response cache.
// Overridable in tests.
var githubAPICachePath = func() (string, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "github-api-cache.json"), nil
}

// githubAPIGet performs a GET against the GitHub API and returns the response
// body. Responses are cached by ETag so revalidations come back as 304, which
// GitHub does not count against the rate limit. Rate-limit responses are
// mapped to an actionable error.
func githubAPIGet(client *http.Client, rawURL string) ([]byte, error) {
	req, err := newGitHubAPIGetRequest(rawURL)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub API request: %w", err)
	}

	githubAPICacheMu.Lock()
	cached, hasCached := loadGitHubAPICache()[rawURL]
	githubAPICacheMu.Unlock()
	if hasCached && cached.ETag != "" {
		req.Header.Set("If-None-Match", cached.ETag)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, githubAPIMaxResponseBytes))
		if err != nil {
			return nil, fmt.Errorf("reading GitHub API response: %w", err)
		}
		if etag := resp.Header.Get("Etag"); etag != "" && json.Valid(body) {
			// Reload under the lock so concurrent fetches of different URLs
			// (e.g. the background update check) don't overwrite each other.
			githubAPICacheMu.Lock()
			cache := loadGitHubAPICache()
			cache[rawURL] = githubAPICacheEntry{ETag: etag, Body: body}
			saveGitHubAPICache(cache)
			githubAPICacheMu.Unlock()
		}
		return body, nil
	case http.StatusNotModified:
		if !hasCached {
			return nil, fmt.Errorf("GitHub API returned 304 but no cached response is available")
		}
		return cached.Body, nil
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub API rate limit exceeded (status %d); set GITHUB_TOKEN or run 'gh auth login' to raise the limit", resp.StatusCode)
	default:
		return nil, fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
}

const githubAPIMaxResponseBytes = 8 << 20 // 8 MiB; release listings are far smaller

// githubAPICacheMu serializes read-modify-write cycles on the cache file
// within this process (foreground command vs. background update check).
var githubAPICacheMu sync.Mutex

type githubAPICacheEntry struct {
	ETag string          `json:"etag"`
	Body json.RawMessage `json:"body"`
}

// loadGitHubAPICache reads the on-disk response cache. Failures degrade to an
// empty cache: the request then simply runs unconditionally.
func loadGitHubAPICache() map[string]githubAPICacheEntry {
	cache := make(map[string]githubAPICacheEntry)
	path, err := githubAPICachePath()
	if err != nil {
		return cache
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cache
	}
	if err := json.Unmarshal(data, &cache); err != nil {
		return make(map[string]githubAPICacheEntry)
	}
	return cache
}

// saveGitHubAPICache persists the response cache best-effort; a failed write
// only costs a future revalidation opportunity.
func saveGitHubAPICache(cache map[string]githubAPICacheEntry) {
	path, err := githubAPICachePath()
	if err != nil {
		return
	}
	data, err := json.Marshal(cache)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".github-api-cache-*")
	if err != nil {
		return
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return
	}
	if err := tmp.Close(); err != nil {
		return
	}
	_ = os.Rename(tmp.Name(), path)
}

// Web (non-API) endpoints for release discovery. Requests to github.com are
// served by the website/CDN and are not subject to the API rate limit.
const (
	githubWebLatestReleaseURL = "https://github.com/wendylabsinc/wendy-agent/releases/latest"
	githubWebDownloadBaseURL  = "https://github.com/wendylabsinc/wendy-agent/releases/download"
)

// agentReleaseArchitectures lists the linux architectures the release CI
// publishes agent tarballs for (see .github/workflows/build.yml).
var agentReleaseArchitectures = []string{"amd64", "arm64"}

// resolveLatestReleaseTagViaWeb resolves the latest stable release tag by
// following github.com's /releases/latest redirect to the tag page.
func resolveLatestReleaseTagViaWeb(client *http.Client) (string, error) {
	req, err := http.NewRequest(http.MethodHead, githubWebLatestReleaseURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", githubAPIUserAgent())

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("resolving latest release: github.com returned status %d", resp.StatusCode)
	}

	// Only trust the tag if the redirect chain stayed on github.com inside
	// our org (the repo segment may differ after a repo rename).
	finalURL := resp.Request.URL
	if finalURL.Scheme != "https" || !strings.EqualFold(finalURL.Host, "github.com") ||
		!strings.HasPrefix(finalURL.Path, "/wendylabsinc/") {
		return "", fmt.Errorf("latest release redirect left the expected GitHub org")
	}

	const tagMarker = "/releases/tag/"
	finalPath := finalURL.Path
	idx := strings.LastIndex(finalPath, tagMarker)
	if idx < 0 {
		return "", fmt.Errorf("latest release redirect did not land on a tag page")
	}
	tag, err := url.PathUnescape(finalPath[idx+len(tagMarker):])
	if err != nil || tag == "" || strings.Contains(tag, "/") {
		return "", fmt.Errorf("could not extract release tag from redirect")
	}
	return tag, nil
}

// fetchAgentReleaseViaWeb resolves the latest stable agent release without the
// GitHub API: the tag comes from the github.com redirect and asset names follow
// the release CI convention wendy-agent-linux-<arch>-<tag>.tar.gz. One
// synthesized URL is validated with a HEAD request (the CI uploads all assets
// atomically with fail_on_unmatched_files), so a convention change surfaces as
// an error here and the caller can fall back to the API.
//
// The synthesized Assets list intentionally contains only the linux agent
// tarballs; callers needing other release assets must use the API path.
func fetchAgentReleaseViaWeb(client *http.Client) (*githubReleaseFull, error) {
	tag, err := resolveLatestReleaseTagViaWeb(client)
	if err != nil {
		return nil, err
	}

	release := &githubReleaseFull{TagName: tag}
	for _, arch := range agentReleaseArchitectures {
		name := fmt.Sprintf("wendy-agent-linux-%s-%s.tar.gz", arch, tag)
		release.Assets = append(release.Assets, githubReleaseAsset{
			Name:               name,
			BrowserDownloadURL: fmt.Sprintf("%s/%s/%s", githubWebDownloadBaseURL, url.PathEscape(tag), url.PathEscape(name)),
		})
	}

	req, err := http.NewRequest(http.MethodHead, release.Assets[0].BrowserDownloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", githubAPIUserAgent())
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release asset %s returned status %d", release.Assets[0].Name, resp.StatusCode)
	}

	return release, nil
}

func isCanonicalGitHubAPIURL(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && strings.EqualFold(u.Host, githubAPIHost)
}

func githubAPIUserAgent() string {
	if !isHTTPToken(version.Version) {
		return "wendy"
	}
	return "wendy/" + version.Version
}

func isHTTPToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if r > 0x7e {
			return false
		}
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case strings.ContainsRune("!#$%&'*+-.^_`|~", r):
		default:
			return false
		}
	}
	return true
}
