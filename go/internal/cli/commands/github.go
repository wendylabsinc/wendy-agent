package commands

import (
	"context"
	"net/http"
	"os"
	"strings"
)

var (
	githubLatestReleaseURL = "https://api.github.com/repos/wendylabsinc/wendy-agent/releases/latest"
	githubReleasesURL      = "https://api.github.com/repos/wendylabsinc/wendy-agent/releases"
)

var githubTokenEnvVars = []string{"WENDY_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"}

func githubAPIToken() string {
	for _, name := range githubTokenEnvVars {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	return ""
}

func newGitHubAPIRequest(url string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "wendy")
	if token := githubAPIToken(); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	return req, nil
}

func doGitHubAPIGet(client *http.Client, url string) (*http.Response, error) {
	req, err := newGitHubAPIRequest(url)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}
