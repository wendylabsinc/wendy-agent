package commands

import (
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

func TestNewGitHubAPIGetRequestWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	oldGhCLIToken := ghCLIToken
	ghCLIToken = func() string { return "" }
	t.Cleanup(func() { ghCLIToken = oldGhCLIToken })

	req, err := newGitHubAPIGetRequest(githubReleasesURL)
	if err != nil {
		t.Fatalf("newGitHubAPIGetRequest: %v", err)
	}

	if req.Method != http.MethodGet {
		t.Fatalf("method = %q; want %q", req.Method, http.MethodGet)
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q; want empty", got)
	}
	if got := req.Header.Get("User-Agent"); got == "" {
		t.Fatal("User-Agent should be set")
	}
	if got, want := req.Header.Get("Accept"), "application/vnd.github+json"; got != want {
		t.Fatalf("Accept = %q; want %q", got, want)
	}
	if got, want := req.Header.Get("X-GitHub-Api-Version"), "2022-11-28"; got != want {
		t.Fatalf("X-GitHub-Api-Version = %q; want %q", got, want)
	}
}

func TestNewGitHubAPIGetRequestFallsBackToGhCLIToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	oldGhCLIToken := ghCLIToken
	ghCLIToken = func() string { return "gh-cli-token" }
	t.Cleanup(func() { ghCLIToken = oldGhCLIToken })

	req, err := newGitHubAPIGetRequest(githubReleasesURL)
	if err != nil {
		t.Fatalf("newGitHubAPIGetRequest: %v", err)
	}

	if got, want := req.Header.Get("Authorization"), "Bearer gh-cli-token"; got != want {
		t.Fatalf("Authorization = %q; want %q", got, want)
	}
}

func TestNewGitHubAPIGetRequestPrefersGitHubTokenOverGhCLI(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "env-token")
	oldGhCLIToken := ghCLIToken
	ghCLIToken = func() string { return "gh-cli-token" }
	t.Cleanup(func() { ghCLIToken = oldGhCLIToken })

	req, err := newGitHubAPIGetRequest(githubReleasesURL)
	if err != nil {
		t.Fatalf("newGitHubAPIGetRequest: %v", err)
	}

	if got, want := req.Header.Get("Authorization"), "Bearer env-token"; got != want {
		t.Fatalf("Authorization = %q; want %q", got, want)
	}
}

func TestNewGitHubAPIGetRequestWithToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token")

	req, err := newGitHubAPIGetRequest(githubReleasesURL)
	if err != nil {
		t.Fatalf("newGitHubAPIGetRequest: %v", err)
	}

	if got, want := req.Header.Get("Authorization"), "Bearer secret-token"; got != want {
		t.Fatalf("Authorization = %q; want %q", got, want)
	}
}

func TestNewGitHubAPIGetRequestRejectsNonGitHubAPIURL(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "secret-token")

	_, err := newGitHubAPIGetRequest("https://example.com/releases?token=secret-token")
	if err == nil {
		t.Fatal("expected error for non-GitHub API URL")
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "/releases") {
		t.Fatalf("error exposes rejected URL details: %v", err)
	}
	if _, err := newGitHubAPIGetRequest("http://api.github.com/releases"); err == nil {
		t.Fatal("expected error for non-HTTPS GitHub API URL")
	}
}

func TestGitHubAPIClientRedirectAuthorizationHandling(t *testing.T) {
	tests := []struct {
		name              string
		redirectURL       string
		wantAuthorization string
	}{
		{
			name:        "external host strips authorization",
			redirectURL: "https://example.com/redirect",
		},
		{
			name:        "http downgrade strips authorization",
			redirectURL: "http://api.github.com/redirect",
		},
		{
			name:        "non-default port strips authorization",
			redirectURL: "https://api.github.com:8443/redirect",
		},
		{
			name:              "canonical GitHub API keeps authorization",
			redirectURL:       "https://api.github.com/redirect",
			wantAuthorization: "Bearer secret-token",
		},
	}

	client := newGitHubAPIClient(0)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redirectURL, err := url.Parse(tt.redirectURL)
			if err != nil {
				t.Fatalf("url.Parse: %v", err)
			}
			req := &http.Request{URL: redirectURL, Header: make(http.Header)}
			req.Header.Set("Authorization", "Bearer secret-token")

			if err := client.CheckRedirect(req, nil); err != nil {
				t.Fatalf("CheckRedirect: %v", err)
			}
			if got := req.Header.Get("Authorization"); got != tt.wantAuthorization {
				t.Fatalf("Authorization after redirect = %q; want %q", got, tt.wantAuthorization)
			}
		})
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func newFakeGitHubClient(rt roundTripperFunc) *http.Client {
	return &http.Client{Transport: rt}
}

func stubGitHubTokens(t *testing.T) {
	t.Helper()
	t.Setenv("GITHUB_TOKEN", "")
	oldGhCLIToken := ghCLIToken
	ghCLIToken = func() string { return "" }
	t.Cleanup(func() { ghCLIToken = oldGhCLIToken })
}

func stubGitHubAPICachePath(t *testing.T) {
	t.Helper()
	oldPath := githubAPICachePath
	tmp := t.TempDir()
	githubAPICachePath = func() (string, error) {
		return filepath.Join(tmp, "github-api-cache.json"), nil
	}
	t.Cleanup(func() { githubAPICachePath = oldPath })
}

func TestGitHubAPIGetRateLimitErrorIsActionable(t *testing.T) {
	stubGitHubTokens(t)
	stubGitHubAPICachePath(t)

	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests} {
		client := newFakeGitHubClient(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Header:     http.Header{"X-Ratelimit-Remaining": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader(`{"message":"API rate limit exceeded"}`)),
			}, nil
		})

		_, err := githubAPIGet(client, githubReleasesURL)
		if err == nil {
			t.Fatalf("status %d: expected error", status)
		}
		if !strings.Contains(err.Error(), "rate limit") {
			t.Fatalf("status %d: error should mention rate limit: %v", status, err)
		}
		if !strings.Contains(err.Error(), "GITHUB_TOKEN") {
			t.Fatalf("status %d: error should suggest GITHUB_TOKEN: %v", status, err)
		}
	}
}

func TestGitHubAPIGetReturnsBodyOnSuccess(t *testing.T) {
	stubGitHubTokens(t)
	stubGitHubAPICachePath(t)

	client := newFakeGitHubClient(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.3"}`)),
		}, nil
	})

	body, err := githubAPIGet(client, githubReleasesURL)
	if err != nil {
		t.Fatalf("githubAPIGet: %v", err)
	}
	if got, want := string(body), `{"tag_name":"v1.2.3"}`; got != want {
		t.Fatalf("body = %q; want %q", got, want)
	}
}

func TestGitHubAPIGetUsesETagCacheOn304(t *testing.T) {
	stubGitHubTokens(t)
	stubGitHubAPICachePath(t)

	calls := 0
	client := newFakeGitHubClient(func(req *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			if got := req.Header.Get("If-None-Match"); got != "" {
				t.Fatalf("first request If-None-Match = %q; want empty", got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Etag": []string{`"etag-1"`}},
				Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1.2.3"}`)),
			}, nil
		default:
			if got, want := req.Header.Get("If-None-Match"), `"etag-1"`; got != want {
				t.Fatalf("second request If-None-Match = %q; want %q", got, want)
			}
			return &http.Response{
				StatusCode: http.StatusNotModified,
				Header:     http.Header{},
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}
	})

	first, err := githubAPIGet(client, githubReleasesURL)
	if err != nil {
		t.Fatalf("first githubAPIGet: %v", err)
	}
	second, err := githubAPIGet(client, githubReleasesURL)
	if err != nil {
		t.Fatalf("second githubAPIGet: %v", err)
	}

	if string(first) != string(second) {
		t.Fatalf("304 should return cached body; got %q, want %q", second, first)
	}
	if calls != 2 {
		t.Fatalf("calls = %d; want 2", calls)
	}
}

func TestGitHubAPIGetRefreshesCacheOnNewETag(t *testing.T) {
	stubGitHubTokens(t)
	stubGitHubAPICachePath(t)

	calls := 0
	client := newFakeGitHubClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Etag": []string{`"etag-1"`}},
				Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v1.0.0"}`)),
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Etag": []string{`"etag-2"`}},
			Body:       io.NopCloser(strings.NewReader(`{"tag_name":"v2.0.0"}`)),
		}, nil
	})

	if _, err := githubAPIGet(client, githubReleasesURL); err != nil {
		t.Fatalf("first githubAPIGet: %v", err)
	}
	second, err := githubAPIGet(client, githubReleasesURL)
	if err != nil {
		t.Fatalf("second githubAPIGet: %v", err)
	}
	if got, want := string(second), `{"tag_name":"v2.0.0"}`; got != want {
		t.Fatalf("body = %q; want %q", got, want)
	}

	// A third call must present the refreshed ETag.
	client304 := newFakeGitHubClient(func(req *http.Request) (*http.Response, error) {
		if got, want := req.Header.Get("If-None-Match"), `"etag-2"`; got != want {
			t.Fatalf("If-None-Match = %q; want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusNotModified,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})
	third, err := githubAPIGet(client304, githubReleasesURL)
	if err != nil {
		t.Fatalf("third githubAPIGet: %v", err)
	}
	if got, want := string(third), `{"tag_name":"v2.0.0"}`; got != want {
		t.Fatalf("cached body = %q; want %q", got, want)
	}
}

func TestGitHubAPIUserAgentRejectsUnsafeVersion(t *testing.T) {
	oldVersion := version.Version
	version.Version = "1.2.3\r\nInjected: true\x00"
	t.Cleanup(func() { version.Version = oldVersion })

	got := githubAPIUserAgent()
	if got != "wendy" {
		t.Fatalf("githubAPIUserAgent = %q; want %q", got, "wendy")
	}
	if strings.Contains(got, "Injected") {
		t.Fatalf("githubAPIUserAgent contains injected header content: %q", got)
	}
}

func TestGitHubAPIUserAgentAllowsHTTPTokenVersion(t *testing.T) {
	oldVersion := version.Version
	version.Version = "1.2.3-dev+build_5"
	t.Cleanup(func() { version.Version = oldVersion })

	if got, want := githubAPIUserAgent(), "wendy/1.2.3-dev+build_5"; got != want {
		t.Fatalf("githubAPIUserAgent = %q; want %q", got, want)
	}
}
