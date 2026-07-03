package commands

import (
	"encoding/json"
	"fmt"
	"net/http"
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
