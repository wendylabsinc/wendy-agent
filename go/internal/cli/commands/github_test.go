package commands

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

func TestNewGitHubAPIGetRequestWithoutToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")

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
