package commands

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

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
	if token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
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
