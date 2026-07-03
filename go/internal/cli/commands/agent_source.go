package commands

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// agentManifest indexes wendy-agent versions published to GCS. It mirrors the
// master.json conventions in manifest.go; Latest points at the newest stable
// version and LatestNightly at the newest prerelease.
type agentManifest struct {
	Latest        string                          `json:"latest"`
	LatestNightly string                          `json:"latest_nightly"`
	Versions      map[string]agentManifestVersion `json:"versions"`
}

type agentManifestVersion struct {
	IsNightly bool                             `json:"is_nightly"`
	Artifacts map[string]agentManifestArtifact `json:"artifacts"` // key = GOARCH, e.g. "amd64"
}

type agentManifestArtifact struct {
	Path      string `json:"path"`       // bucket-relative, joined as gcsBaseURL + "/" + Path
	Checksum  string `json:"checksum"`   // sha256 hex of the .tar.gz
	SizeBytes int64  `json:"size_bytes"`
}

func fetchAgentManifestFrom(baseURL string) (*agentManifest, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(baseURL + "/agent/manifest.json")
	if err != nil {
		return nil, fmt.Errorf("fetching agent manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("agent manifest returned status %d", resp.StatusCode)
	}
	var m agentManifest
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("decoding agent manifest: %w", err)
	}
	return &m, nil
}

func fetchAgentManifest() (*agentManifest, error) {
	return fetchAgentManifestFrom(gcsBaseURL)
}

// extractAgentFromTarGz reads a gzipped tar stream and returns the bytes of the
// file whose name ends in "wendy-agent".
func extractAgentFromTarGz(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("opening gzip reader: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("reading tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeReg && strings.HasSuffix(hdr.Name, "wendy-agent") {
			data, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("reading binary from tar: %w", err)
			}
			return data, nil
		}
	}
	return nil, fmt.Errorf("wendy-agent binary not found in tarball")
}

func agentVersionFromManifest(m *agentManifest, nightly bool) (string, error) {
	v := m.Latest
	if nightly {
		v = m.LatestNightly
	}
	if v == "" {
		if nightly {
			return "", fmt.Errorf("agent manifest has no latest_nightly version")
		}
		return "", fmt.Errorf("agent manifest has no latest version")
	}
	return v, nil
}

// resolveAgentVersion returns the latest wendy-agent version tag for the channel,
// preferring the GCS manifest and falling back to GitHub releases on any GCS miss.
func resolveAgentVersion(nightly bool) (version, source string, err error) {
	if m, mErr := fetchAgentManifest(); mErr == nil {
		if v, vErr := agentVersionFromManifest(m, nightly); vErr == nil {
			return v, "gcs", nil
		} else {
			fmt.Fprintf(os.Stderr, "GCS agent manifest lacks a version (%v); falling back to GitHub\n", vErr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "GCS agent manifest fetch failed (%v); falling back to GitHub\n", mErr)
	}

	rel, err := fetchAgentRelease(nightly)
	if err != nil {
		return "", "", err
	}
	return rel.TagName, "github", nil
}
