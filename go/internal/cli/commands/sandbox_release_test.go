package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func makeControlPlaneTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, content := range files {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(content))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("WriteHeader(%s): %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("Write(%s): %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar Close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

func TestExtractControlPlaneTarGz_StripsTopLevelDirAndWritesFiles(t *testing.T) {
	data := makeControlPlaneTarGz(t, map[string]string{
		"control-plane/package.json":  `{"name":"sandbox-control-plane"}`,
		"control-plane/dist/index.js": "console.log('hi')",
	})
	dest := t.TempDir()

	if err := extractControlPlaneTarGz(bytes.NewReader(data), dest); err != nil {
		t.Fatalf("extractControlPlaneTarGz: %v", err)
	}

	pkg, err := os.ReadFile(filepath.Join(dest, "package.json"))
	if err != nil {
		t.Fatalf("reading extracted package.json: %v", err)
	}
	if string(pkg) != `{"name":"sandbox-control-plane"}` {
		t.Errorf("package.json content = %q", pkg)
	}
	idx, err := os.ReadFile(filepath.Join(dest, "dist", "index.js"))
	if err != nil {
		t.Fatalf("reading extracted dist/index.js: %v", err)
	}
	if string(idx) != "console.log('hi')" {
		t.Errorf("dist/index.js content = %q", idx)
	}
}

func TestExtractControlPlaneTarGz_EmptyTarballErrors(t *testing.T) {
	data := makeControlPlaneTarGz(t, map[string]string{})
	dest := t.TempDir()

	if err := extractControlPlaneTarGz(bytes.NewReader(data), dest); err == nil {
		t.Fatal("expected error for a tarball with no regular files, got nil")
	}
}

func TestExtractControlPlaneTarGz_RejectsPathTraversal(t *testing.T) {
	data := makeControlPlaneTarGz(t, map[string]string{
		"control-plane/../escape.txt": "malicious content",
		"control-plane/safe.txt":      "safe content",
	})
	dest := t.TempDir()

	if err := extractControlPlaneTarGz(bytes.NewReader(data), dest); err == nil {
		t.Fatal("expected error for path traversal attack, got nil")
	}

	// Verify no file was written outside destDir
	escapePath := filepath.Join(filepath.Dir(dest), "escape.txt")
	if _, err := os.Stat(escapePath); err == nil {
		t.Fatalf("path traversal file was created at %s", escapePath)
	}
}

// The API asset URL — not browser_download_url — is what works for a private
// repo, so findControlPlaneAsset must return the former.
func TestFindControlPlaneAsset_ReturnsAPIAssetURLNotBrowserURL(t *testing.T) {
	rel := &sandboxRelease{
		TagName: "control-plane-latest",
		Assets: []sandboxReleaseAsset{
			{
				Name:               "other-file.txt",
				URL:                "https://api.github.com/repos/wendylabsinc/wendy-sandbox/releases/assets/1",
				BrowserDownloadURL: "https://example.com/other",
			},
			{
				Name:               "control-plane.tar.gz",
				URL:                "https://api.github.com/repos/wendylabsinc/wendy-sandbox/releases/assets/2",
				BrowserDownloadURL: "https://example.com/control-plane.tar.gz",
			},
		},
	}
	url, err := findControlPlaneAsset(rel)
	if err != nil {
		t.Fatalf("findControlPlaneAsset: %v", err)
	}
	if url != "https://api.github.com/repos/wendylabsinc/wendy-sandbox/releases/assets/2" {
		t.Errorf("url = %q; want the api.github.com asset URL, not browser_download_url", url)
	}
}

// The asset API URL must be accepted by newGitHubAPIGetRequest's host check, or
// downloadAndExtractControlPlaneRelease can never authenticate.
func TestGitHubAPIAssetURLIsAcceptedByRequestBuilder(t *testing.T) {
	const assetURL = "https://api.github.com/repos/wendylabsinc/wendy-sandbox/releases/assets/42"
	req, err := newGitHubAPIGetRequest(assetURL)
	if err != nil {
		t.Fatalf("newGitHubAPIGetRequest(%q): %v", assetURL, err)
	}
	req.Header.Set("Accept", "application/octet-stream")
	if got := req.Header.Get("Accept"); got != "application/octet-stream" {
		t.Errorf("Accept = %q, want application/octet-stream", got)
	}
}

func TestFindControlPlaneAsset_MissingAssetErrors(t *testing.T) {
	rel := &sandboxRelease{TagName: "control-plane-latest", Assets: nil}
	if _, err := findControlPlaneAsset(rel); err == nil {
		t.Fatal("expected error when the release has no matching asset")
	}
}
