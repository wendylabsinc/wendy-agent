package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// agentManifestVersion.Artifacts is keyed by manifestArtifactKey(osName, arch):
// bare GOARCH ("amd64", "arm64") for the legacy Linux tarballs, and
// "darwin-<arch>" (e.g. "darwin-arm64") for the macOS app-bundle zip.
type agentManifestVersion struct {
	IsNightly bool                             `json:"is_nightly"`
	Artifacts map[string]agentManifestArtifact `json:"artifacts"`
}

type agentManifestArtifact struct {
	Path      string `json:"path"`     // bucket-relative, joined as gcsBaseURL + "/" + Path
	Checksum  string `json:"checksum"` // sha256 hex of the artifact (.tar.gz on linux, .zip on darwin)
	SizeBytes int64  `json:"size_bytes"`
}

// manifestArtifactKey returns the key under agentManifestVersion.Artifacts for
// osName/arch: the bare arch for every platform except darwin, which is keyed
// "darwin-<arch>" to distinguish the macOS app-bundle zip from the legacy
// Linux tarballs.
func manifestArtifactKey(osName, arch string) string {
	if strings.EqualFold(osName, "darwin") {
		return "darwin-" + arch
	}
	return arch
}

// agentPlatformLabel renders osName/arch for user-facing messages and error
// wording: "macos/<arch>" for darwin, "linux/<arch>" otherwise.
func agentPlatformLabel(osName, arch string) string {
	if strings.EqualFold(osName, "darwin") {
		return "macos/" + arch
	}
	return "linux/" + arch
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
		}
	}

	rel, err := fetchAgentRelease(nightly)
	if err != nil {
		return "", "", err
	}
	return rel.TagName, "github", nil
}

// downloadAgentArtifactFromGCS downloads and verifies the agent artifact for
// osName/arch from the manifest, returning the raw payload bytes. For darwin
// the payload is the app-bundle zip, returned verbatim (the Swift agent
// extracts/verifies it); for every other OS it's the extracted ELF binary
// pulled out of the .tar.gz.
func downloadAgentArtifactFromGCS(baseURL string, m *agentManifest, osName, arch string, nightly bool) ([]byte, string, error) {
	version, err := agentVersionFromManifest(m, nightly)
	if err != nil {
		return nil, "", err
	}
	ver, ok := m.Versions[version]
	if !ok {
		return nil, "", fmt.Errorf("agent manifest missing version entry %q", version)
	}
	art, ok := ver.Artifacts[manifestArtifactKey(osName, arch)]
	if !ok || art.Path == "" {
		return nil, "", fmt.Errorf("agent manifest has no %s artifact for version %s", agentPlatformLabel(osName, arch), version)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(baseURL + "/" + art.Path)
	if err != nil {
		return nil, "", fmt.Errorf("downloading agent tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("agent tarball returned status %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("reading agent tarball: %w", err)
	}
	if art.Checksum != "" {
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != art.Checksum {
			return nil, "", fmt.Errorf("agent tarball checksum mismatch: manifest %s, got %s", art.Checksum, got)
		}
	}
	if strings.EqualFold(osName, "darwin") {
		return raw, version, nil
	}
	bin, err := extractAgentFromTarGz(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	return bin, version, nil
}

// resolveAgentArtifact returns the wendy-agent release artifact for
// osName/arch, preferring GCS (to avoid GitHub rate limits) and falling back
// to GitHub releases on any GCS miss. On darwin the payload is the raw
// app-bundle zip; on every other OS it's the extracted ELF binary. version is
// the resolved version tag; source is "gcs" or "github".
func resolveAgentArtifact(osName, arch string, nightly bool) (payload []byte, version, source string, err error) {
	if m, mErr := fetchAgentManifest(); mErr == nil {
		if bin, ver, dErr := downloadAgentArtifactFromGCS(gcsBaseURL, m, osName, arch, nightly); dErr == nil {
			return bin, ver, "gcs", nil
		}
	}

	rel, err := fetchAgentRelease(nightly)
	if err != nil {
		return nil, "", "", err
	}
	asset, err := matchAgentReleaseAsset(rel.Assets, osName, arch, rel.TagName)
	if err != nil {
		return nil, "", "", err
	}
	raw, err := downloadReleaseAssetBytes(*asset)
	if err != nil {
		return nil, "", "", err
	}
	if strings.EqualFold(osName, "darwin") {
		return raw, rel.TagName, "github", nil
	}
	bin, err := extractAgentFromTarGz(bytes.NewReader(raw))
	if err != nil {
		return nil, "", "", err
	}
	return bin, rel.TagName, "github", nil
}

// matchAgentReleaseAsset finds the release asset for osName/arch: darwin
// matches "wendy-agent-macos-<arch>-" + ".zip", everything else matches the
// legacy "wendy-agent-linux-<arch>-" + ".tar.gz" prefix/suffix.
func matchAgentReleaseAsset(assets []githubReleaseAsset, osName, arch, tagName string) (*githubReleaseAsset, error) {
	var assetPrefix, assetSuffix string
	if strings.EqualFold(osName, "darwin") {
		assetPrefix = fmt.Sprintf("wendy-agent-macos-%s-", arch)
		assetSuffix = ".zip"
	} else {
		assetPrefix = fmt.Sprintf("wendy-agent-linux-%s-", arch)
		assetSuffix = ".tar.gz"
	}
	for i := range assets {
		a := assets[i]
		if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, assetSuffix) {
			return &a, nil
		}
	}
	return nil, fmt.Errorf("no asset for %s in release %s", agentPlatformLabel(osName, arch), tagName)
}
