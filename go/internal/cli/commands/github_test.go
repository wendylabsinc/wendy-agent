package commands

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func clearGitHubTokenEnv(t *testing.T) {
	t.Helper()
	for _, name := range githubTokenEnvVars {
		t.Setenv(name, "")
	}
}

func withGitHubLatestReleaseURL(t *testing.T, url string) {
	t.Helper()
	old := githubLatestReleaseURL
	githubLatestReleaseURL = url
	t.Cleanup(func() { githubLatestReleaseURL = old })
}

func TestGitHubAPIRequestTokenSelection(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "wendy token wins",
			env: map[string]string{
				"WENDY_GITHUB_TOKEN": "wendy-token",
				"GH_TOKEN":           "gh-token",
				"GITHUB_TOKEN":       "github-token",
			},
			want: "Bearer wendy-token",
		},
		{
			name: "gh token second",
			env: map[string]string{
				"GH_TOKEN":     "gh-token",
				"GITHUB_TOKEN": "github-token",
			},
			want: "Bearer gh-token",
		},
		{
			name: "github token last",
			env: map[string]string{
				"GITHUB_TOKEN": "github-token",
			},
			want: "Bearer github-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearGitHubTokenEnv(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			req, err := newGitHubAPIRequest("https://api.github.com/repos/wendylabsinc/wendy-agent/releases/latest")
			if err != nil {
				t.Fatalf("newGitHubAPIRequest returned error: %v", err)
			}

			if got := req.Header.Get("Authorization"); got != tt.want {
				t.Fatalf("Authorization header = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGitHubAPIRequestHeadersWithoutToken(t *testing.T) {
	clearGitHubTokenEnv(t)

	req, err := newGitHubAPIRequest("https://api.github.com/repos/wendylabsinc/wendy-agent/releases/latest")
	if err != nil {
		t.Fatalf("newGitHubAPIRequest returned error: %v", err)
	}

	checks := map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"User-Agent":           "wendy",
	}
	for header, want := range checks {
		if got := req.Header.Get(header); got != want {
			t.Fatalf("%s header = %q, want %q", header, got, want)
		}
	}
	if got := req.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization header = %q, want empty", got)
	}
}

func TestCheckLatestReleaseUsesAuthenticatedGitHubRequest(t *testing.T) {
	clearGitHubTokenEnv(t)
	t.Setenv("WENDY_GITHUB_TOKEN", "wendy-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{
			"Accept":               "application/vnd.github+json",
			"X-GitHub-Api-Version": "2022-11-28",
			"User-Agent":           "wendy",
			"Authorization":        "Bearer wendy-token",
		}
		for header, want := range checks {
			if got := r.Header.Get(header); got != want {
				t.Errorf("%s header = %q, want %q", header, got, want)
			}
		}
		fmt.Fprint(w, `{"tag_name":"v1.2.3"}`)
	}))
	defer srv.Close()
	withGitHubLatestReleaseURL(t, srv.URL)

	got, err := checkLatestRelease()
	if err != nil {
		t.Fatalf("checkLatestRelease returned error: %v", err)
	}
	if got != "v1.2.3" {
		t.Fatalf("latest release = %q, want %q", got, "v1.2.3")
	}
}

func TestFetchAgentReleaseReturnsNonOKStatus(t *testing.T) {
	clearGitHubTokenEnv(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rate limited", http.StatusForbidden)
	}))
	defer srv.Close()
	withGitHubLatestReleaseURL(t, srv.URL)

	_, err := fetchAgentRelease(false)
	if err == nil {
		t.Fatal("fetchAgentRelease returned nil error, want status error")
	}
	if !strings.Contains(err.Error(), "GitHub API returned status 403") {
		t.Fatalf("fetchAgentRelease error = %q, want status 403", err)
	}
}
