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
	Name               string `json:"name"`
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
		return nil, fmt.Errorf("fetching control-plane release: unexpected status %d (tag %s not published yet?)", resp.StatusCode, controlPlaneReleaseTag)
	}
	var rel sandboxRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decoding control-plane release response: %w", err)
	}
	return &rel, nil
}

func findControlPlaneAsset(rel *sandboxRelease) (string, error) {
	for _, a := range rel.Assets {
		if a.Name == controlPlaneReleaseAsset {
			return a.BrowserDownloadURL, nil
		}
	}
	return "", fmt.Errorf("control-plane release %s has no %s asset", rel.TagName, controlPlaneReleaseAsset)
}

// downloadAndExtractControlPlaneRelease streams the release tarball straight
// into destDir without buffering the whole download in memory.
func downloadAndExtractControlPlaneRelease(ctx context.Context, downloadURL, destDir string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("building control-plane download request: %w", err)
	}
	client := &http.Client{Timeout: 5 * time.Minute}
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
