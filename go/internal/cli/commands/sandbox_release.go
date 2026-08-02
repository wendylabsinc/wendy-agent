package commands

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	controlPlaneReleaseRepo  = "wendylabsinc/wendy-sandbox"
	controlPlaneReleaseTag   = "control-plane-latest"
	controlPlaneReleaseAsset = "control-plane.tar.gz"
)

type sandboxReleaseAsset struct {
	Name string `json:"name"`
	// URL is the api.github.com asset endpoint
	// (https://api.github.com/repos/{owner}/{repo}/releases/assets/{id}). This is
	// the only download URL that works for a private repo — see
	// findControlPlaneAsset.
	URL string `json:"url"`
	// BrowserDownloadURL is the objects.githubusercontent.com URL. Kept for error
	// messages/diagnostics only; it cannot be fetched for a private repo.
	BrowserDownloadURL string `json:"browser_download_url"`
}

type sandboxRelease struct {
	TagName string                `json:"tag_name"`
	Assets  []sandboxReleaseAsset `json:"assets"`
}

// fetchControlPlaneRelease resolves the control-plane-latest release. Uses the
// tags endpoint, not /releases/latest — that endpoint excludes prereleases,
// and control-plane-latest is published as one (see control-plane-release.yml
// in the wendylabsinc/wendy-sandbox repo).
//
// wendylabsinc/wendy-sandbox is a private repo, so every request here (metadata
// and asset download alike) must carry the GITHUB_TOKEN that
// newGitHubAPIGetRequest attaches. GitHub masks private-repo permission errors
// as 404, so a missing token looks identical to a missing tag — hence the
// combined error message below.
func fetchControlPlaneRelease(ctx context.Context) (*sandboxRelease, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", controlPlaneReleaseRepo, controlPlaneReleaseTag)
	req, err := newGitHubAPIGetRequest(url)
	if err != nil {
		return nil, fmt.Errorf("building control-plane release request: %w", err)
	}
	req = req.WithContext(ctx)
	client := newGitHubAPIClient(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching control-plane release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return nil, fmt.Errorf("fetching control-plane release: status %d — either the %s tag isn't published yet, or GITHUB_TOKEN is missing/lacks access to the private %s repo", resp.StatusCode, controlPlaneReleaseTag, controlPlaneReleaseRepo)
		}
		return nil, fmt.Errorf("fetching control-plane release: unexpected status %d", resp.StatusCode)
	}
	var rel sandboxRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decoding control-plane release response: %w", err)
	}
	return &rel, nil
}

// findControlPlaneAsset returns the asset's API URL
// (https://api.github.com/repos/{owner}/{repo}/releases/assets/{id}), NOT its
// browser_download_url. Do not "simplify" this back to browser_download_url:
// wendylabsinc/wendy-sandbox is private, and its browser download URL requires a
// signed short-lived redirect that an API token cannot mint, so an
// unauthenticated fetch just 404s. The asset API URL, requested with
// `Accept: application/octet-stream` and the GITHUB_TOKEN Authorization header,
// is GitHub's documented path for downloading a private release asset.
func findControlPlaneAsset(rel *sandboxRelease) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == controlPlaneReleaseAsset {
			return a.URL, nil
		}
	}
	return "", fmt.Errorf("control-plane release %s has no %s asset", rel.TagName, controlPlaneReleaseAsset)
}

// downloadAndExtractControlPlaneRelease streams the release tarball straight
// into destDir without buffering the whole download in memory. assetAPIURL must
// be the api.github.com asset endpoint (see findControlPlaneAsset) so the
// request carries GITHUB_TOKEN — required for the private source repo.
func downloadAndExtractControlPlaneRelease(ctx context.Context, assetAPIURL, destDir string) error {
	req, err := newGitHubAPIGetRequest(assetAPIURL)
	if err != nil {
		return fmt.Errorf("building control-plane download request: %w", err)
	}
	// Overrides the JSON Accept newGitHubAPIGetRequest sets: this endpoint returns
	// the asset bytes only when asked for octet-stream.
	req.Header.Set("Accept", "application/octet-stream")
	req = req.WithContext(ctx)
	client := newGitHubAPIClient(5 * time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("downloading control-plane release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading control-plane release: unexpected status %d", resp.StatusCode)
	}
	return extractControlPlaneTarGz(resp.Body, destDir)
}

// extractControlPlaneTarGz unpacks a control-plane release tarball (whose
// entries are all prefixed "control-plane/") into destDir, stripping that
// prefix so destDir itself becomes the control-plane directory.
func extractControlPlaneTarGz(r io.Reader, destDir string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("opening control-plane tarball: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	wrote := false
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading control-plane tarball: %w", err)
		}
		rel := strings.TrimPrefix(hdr.Name, "control-plane/")
		if rel == "" || rel == hdr.Name {
			continue // the bare "control-plane" dir entry, or something outside it
		}
		target := filepath.Join(destDir, rel)
		// Prevent path traversal: ensure target stays within destDir.
		if !strings.HasPrefix(target, filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("control-plane tarball entry %q escapes destination directory", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("creating %s: %w", filepath.Dir(target), err)
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("writing %s: %w", target, err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return fmt.Errorf("writing %s: %w", target, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("closing %s: %w", target, err)
			}
			wrote = true
		}
	}
	if !wrote {
		return fmt.Errorf("control-plane tarball contained no regular files")
	}
	return nil
}
