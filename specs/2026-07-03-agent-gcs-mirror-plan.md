# wendy-agent GCS mirror — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish `wendy-agent` linux tarballs to `gs://wendyos-images-public` in CI, and make the CLI download the agent from GCS by default (falling back to GitHub), removing the GitHub API/release dependency from the hot path.

**Architecture:** A new `agent/manifest.json` on the existing public GCS bucket indexes agent versions and holds `latest`/`latest_nightly` pointers. The CLI gains two GCS-first/GitHub-fallback resolvers (`resolveAgentVersion`, `resolveAgentBinary`) that replace the duplicated `fetchAgentRelease → match asset → downloadAgentBinary` dance across seven call sites. CI uploads the tarballs with `gcloud storage cp` and merges the manifest with a tested `jq` filter, in one serialized job.

**Tech Stack:** Go 1.x (CLI, `net/http`, `archive/tar`, `compress/gzip`, `crypto/sha256`, `net/http/httptest` for tests), GitHub Actions, `gcloud storage`, `jq`, `bash`.

## Global Constraints

- Bucket: `wendyos-images-public`; CLI base URL const already exists: `gcsBaseURL = "https://storage.googleapis.com/wendyos-images-public"` (`internal/cli/commands/manifest.go:12`). Reuse it — do not introduce a second base URL.
- Version string format: `YYYY.MM.DD-HHMMSS`, no `v` prefix (same value as git tag / GitHub release).
- Bucket object paths in JSON are bucket-relative (no leading slash), consumed as `gcsBaseURL + "/" + path` — match the existing OS-image manifest convention.
- Agent tarball basename and internal layout are unchanged from today's GitHub artifacts: `wendy-agent-linux-<arch>-<version>.tar.gz` containing `wendy-agent-linux-<arch>/wendy-agent`.
- GCS is preferred; on ANY GCS failure the CLI falls back to the existing GitHub path with identical resulting behavior. GitHub publishing and the GitHub download functions are retained, not deleted.
- Go tests live beside code as `*_test.go` in `package commands`; use `net/http/httptest` for HTTP (see `manifest_test.go` for style).
- GitHub Action version pins in this repo (copy verbatim): `actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8`, `google-github-actions/auth@7c6bc770dae815cd3e89ee6cdf493a5fab2cc093 # v3`, `google-github-actions/setup-gcloud@aa5489c8933f4cc7a4f7d45035b3b1440c9c10db # v3`.
- CI GCP identity comes from repository **variables** (not secrets): `vars.GCP_WORKLOAD_IDENTITY_PROVIDER`, `vars.GCP_SERVICE_ACCOUNT`, `vars.GCP_PROJECT_ID`.

---

## Prerequisite (manual ops — not code)

- [ ] Confirm the WIF service account (`vars.GCP_SERVICE_ACCOUNT`, already used by the `publish-linux-repos` job) has object create/read/overwrite on `gs://wendyos-images-public` (e.g. `roles/storage.objectAdmin` on the bucket). Without it, Task 8's job fails at `gcloud storage cp`. This is a Google Cloud IAM change made by a repo admin; it is not part of any code task.

---

## Task 1: Agent manifest types + GCS fetch

Adds the manifest schema and its fetcher. New file keeps agent logic separate from the OS-image manifest code in `manifest.go`.

**Files:**
- Create: `go/internal/cli/commands/agent_source.go`
- Test: `go/internal/cli/commands/agent_source_test.go`

**Interfaces:**
- Consumes: `gcsBaseURL` (from `manifest.go`).
- Produces:
  - `type agentManifest struct { Latest string; LatestNightly string; Versions map[string]agentManifestVersion }`
  - `type agentManifestVersion struct { IsNightly bool; Artifacts map[string]agentManifestArtifact }`
  - `type agentManifestArtifact struct { Path string; Checksum string; SizeBytes int64 }`
  - `func fetchAgentManifestFrom(baseURL string) (*agentManifest, error)` — fetch + decode `baseURL + "/agent/manifest.json"`.
  - `func fetchAgentManifest() (*agentManifest, error)` — thin wrapper calling `fetchAgentManifestFrom(gcsBaseURL)`.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/commands/agent_source_test.go
package commands

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const sampleAgentManifest = `{
  "latest": "2026.07.01-120000",
  "latest_nightly": "2026.07.03-093000",
  "versions": {
    "2026.07.03-093000": {
      "is_nightly": true,
      "artifacts": {
        "amd64": {"path": "agent/2026.07.03-093000/wendy-agent-linux-amd64-2026.07.03-093000.tar.gz", "checksum": "abc123", "size_bytes": 42},
        "arm64": {"path": "agent/2026.07.03-093000/wendy-agent-linux-arm64-2026.07.03-093000.tar.gz", "checksum": "def456", "size_bytes": 43}
      }
    },
    "2026.07.01-120000": {
      "is_nightly": false,
      "artifacts": {
        "arm64": {"path": "agent/2026.07.01-120000/wendy-agent-linux-arm64-2026.07.01-120000.tar.gz", "checksum": "aaa", "size_bytes": 10}
      }
    }
  }
}`

func TestFetchAgentManifestDecodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/manifest.json" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write([]byte(sampleAgentManifest))
	}))
	defer srv.Close()

	m, err := fetchAgentManifestFrom(srv.URL)
	if err != nil {
		t.Fatalf("fetchAgentManifestFrom: %v", err)
	}
	if m.Latest != "2026.07.01-120000" || m.LatestNightly != "2026.07.03-093000" {
		t.Fatalf("pointers: latest=%q nightly=%q", m.Latest, m.LatestNightly)
	}
	v, ok := m.Versions["2026.07.03-093000"]
	if !ok || !v.IsNightly {
		t.Fatalf("nightly version entry missing or not flagged nightly: %+v", v)
	}
	if v.Artifacts["amd64"].Path == "" || v.Artifacts["amd64"].Checksum != "abc123" || v.Artifacts["amd64"].SizeBytes != 42 {
		t.Fatalf("amd64 artifact wrong: %+v", v.Artifacts["amd64"])
	}
}

func TestFetchAgentManifest404IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := fetchAgentManifestFrom(srv.URL); err == nil {
		t.Fatal("expected error on 404, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestFetchAgentManifest -v`
Expected: FAIL — `undefined: fetchAgentManifestFrom` (compile error).

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/commands/agent_source.go
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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestFetchAgentManifest -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/agent_source.go go/internal/cli/commands/agent_source_test.go
git commit -m "feat(cli): add wendy-agent GCS manifest types and fetcher"
```

---

## Task 2: Extract shared tar-gz agent extraction

Factor the tar walk out of `downloadAgentBinary` so both the GitHub and GCS paths share it.

**Files:**
- Modify: `go/internal/cli/commands/device.go` (`downloadAgentBinary` at `:1788`)
- Modify: `go/internal/cli/commands/agent_source.go` (add `extractAgentFromTarGz`)
- Test: `go/internal/cli/commands/agent_source_test.go`

**Interfaces:**
- Produces: `func extractAgentFromTarGz(r io.Reader) ([]byte, error)` — reads a gzipped tar, returns bytes of the entry whose name ends in `wendy-agent`.
- Modifies: `downloadAgentBinary(asset githubReleaseAsset) ([]byte, error)` now delegates its tar walk to `extractAgentFromTarGz`.

- [ ] **Step 1: Write the failing test**

```go
// append to agent_source_test.go
import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	// (keep existing imports)
)

func makeAgentTarGz(t *testing.T, innerName string, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	hdr := &tar.Header{Name: innerName, Mode: 0o755, Size: int64(len(payload)), Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractAgentFromTarGz(t *testing.T) {
	payload := []byte("ELF-ish-binary")
	tgz := makeAgentTarGz(t, "wendy-agent-linux-amd64/wendy-agent", payload)
	got, err := extractAgentFromTarGz(bytes.NewReader(tgz))
	if err != nil {
		t.Fatalf("extractAgentFromTarGz: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %q", got)
	}
}

func TestExtractAgentFromTarGzMissing(t *testing.T) {
	tgz := makeAgentTarGz(t, "some-dir/not-the-agent", []byte("x"))
	if _, err := extractAgentFromTarGz(bytes.NewReader(tgz)); err == nil {
		t.Fatal("expected error when wendy-agent absent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestExtractAgentFromTarGz -v`
Expected: FAIL — `undefined: extractAgentFromTarGz`.

- [ ] **Step 3: Add `extractAgentFromTarGz` and rewire `downloadAgentBinary`**

Add to `agent_source.go` (add `"archive/tar"`, `"compress/gzip"`, `"io"`, `"strings"` to its imports):

```go
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
```

Then replace the body of `downloadAgentBinary` in `device.go` (lines 1788-1827) with:

```go
func downloadAgentBinary(asset githubReleaseAsset) ([]byte, error) {
	client := &http.Client{Timeout: 5 * time.Minute}

	resp, err := client.Get(asset.BrowserDownloadURL)
	if err != nil {
		return nil, fmt.Errorf("downloading asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned status %d", resp.StatusCode)
	}

	return extractAgentFromTarGz(resp.Body)
}
```

Note: `device.go` still imports `archive/tar` and `compress/gzip` elsewhere? If `go build` reports them unused after this edit, remove `"archive/tar"` and `"compress/gzip"` from `device.go`'s import block (Step 4 will surface it).

- [ ] **Step 4: Run tests + build to verify**

Run: `cd go && go test ./internal/cli/commands/ -run TestExtractAgentFromTarGz -v && go build ./...`
Expected: PASS, and a clean build. If build fails with `"archive/tar" imported and not used` (or `compress/gzip`), delete those two lines from `device.go`'s imports and re-run.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/agent_source.go go/internal/cli/commands/agent_source_test.go go/internal/cli/commands/device.go
git commit -m "refactor(cli): share agent tar.gz extraction between download paths"
```

---

## Task 3: `resolveAgentVersion` (GCS-first, GitHub fallback)

Version-only resolver for the two call sites that just need the latest tag.

**Files:**
- Modify: `go/internal/cli/commands/agent_source.go`
- Test: `go/internal/cli/commands/agent_source_test.go`

**Interfaces:**
- Consumes: `fetchAgentManifestFrom`, `agentManifest`; GitHub fallback `fetchAgentRelease(nightly bool) (*githubReleaseFull, error)` (from `device.go:1732`).
- Produces:
  - `func agentVersionFromManifest(m *agentManifest, nightly bool) (string, error)` — returns `m.LatestNightly` (nightly) or `m.Latest`; error if empty.
  - `func resolveAgentVersion(nightly bool) (version, source string, err error)` — GCS manifest first (`source="gcs"`), GitHub fallback (`source="github"`, via `fetchAgentRelease(...).TagName`).

- [ ] **Step 1: Write the failing test** (pure selection logic; the fallback wiring is covered by build + Task 5 usage)

```go
// append to agent_source_test.go
func TestAgentVersionFromManifest(t *testing.T) {
	m := &agentManifest{Latest: "2026.07.01-120000", LatestNightly: "2026.07.03-093000"}

	v, err := agentVersionFromManifest(m, false)
	if err != nil || v != "2026.07.01-120000" {
		t.Fatalf("stable: v=%q err=%v", v, err)
	}
	v, err = agentVersionFromManifest(m, true)
	if err != nil || v != "2026.07.03-093000" {
		t.Fatalf("nightly: v=%q err=%v", v, err)
	}
}

func TestAgentVersionFromManifestEmptyIsError(t *testing.T) {
	if _, err := agentVersionFromManifest(&agentManifest{}, false); err == nil {
		t.Fatal("expected error when no stable version present")
	}
	if _, err := agentVersionFromManifest(&agentManifest{Latest: "x"}, true); err == nil {
		t.Fatal("expected error when no nightly version present")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestAgentVersionFromManifest -v`
Expected: FAIL — `undefined: agentVersionFromManifest`.

- [ ] **Step 3: Implement**

```go
// add to agent_source.go
import "fmt" // already present

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
```

Add `"os"` to `agent_source.go` imports.

- [ ] **Step 4: Run tests + build**

Run: `cd go && go test ./internal/cli/commands/ -run TestAgentVersionFromManifest -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/agent_source.go go/internal/cli/commands/agent_source_test.go
git commit -m "feat(cli): resolveAgentVersion with GCS-first, GitHub fallback"
```

---

## Task 4: `resolveAgentBinary` (GCS-first download + checksum, GitHub fallback)

Download resolver used by the five sites that need the binary bytes.

**Files:**
- Modify: `go/internal/cli/commands/agent_source.go`
- Test: `go/internal/cli/commands/agent_source_test.go`

**Interfaces:**
- Consumes: `fetchAgentManifestFrom`, `extractAgentFromTarGz`, `agentManifest`; GitHub fallback `fetchAgentRelease`, `downloadAgentBinary`, `githubReleaseAsset`.
- Produces:
  - `func downloadAgentFromGCS(baseURL string, m *agentManifest, arch string, nightly bool) (binary []byte, version string, err error)` — resolves version + arch artifact, GETs `baseURL + "/" + art.Path`, extracts via `extractAgentFromTarGz`, verifies sha256 against `art.Checksum`.
  - `func resolveAgentBinary(arch string, nightly bool) (binary []byte, version, source string, err error)` — GCS first via `downloadAgentFromGCS(gcsBaseURL, ...)`, GitHub fallback (fetch release → match `wendy-agent-linux-<arch>-*.tar.gz` → `downloadAgentBinary`).

- [ ] **Step 1: Write the failing test**

```go
// append to agent_source_test.go
import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	// keep existing
)

func TestDownloadAgentFromGCS(t *testing.T) {
	payload := []byte("agent-binary-bytes")
	tgz := makeAgentTarGz(t, "wendy-agent-linux-amd64/wendy-agent", payload)
	sum := sha256.Sum256(tgz)
	checksum := hex.EncodeToString(sum[:])

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/manifest.json":
			fmt.Fprintf(w, `{"latest":"v1","versions":{"v1":{"is_nightly":false,"artifacts":{"amd64":{"path":"agent/v1/a.tar.gz","checksum":%q,"size_bytes":%d}}}}}`, checksum, len(tgz))
		case "/agent/v1/a.tar.gz":
			w.Write(tgz)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	m, err := fetchAgentManifestFrom(srv.URL)
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	bin, ver, err := downloadAgentFromGCS(srv.URL, m, "amd64", false)
	if err != nil {
		t.Fatalf("downloadAgentFromGCS: %v", err)
	}
	if ver != "v1" || !bytes.Equal(bin, payload) {
		t.Fatalf("ver=%q bin=%q", ver, bin)
	}
}

func TestDownloadAgentFromGCSMissingArch(t *testing.T) {
	m := &agentManifest{Latest: "v1", Versions: map[string]agentManifestVersion{
		"v1": {Artifacts: map[string]agentManifestArtifact{"amd64": {Path: "p"}}},
	}}
	if _, _, err := downloadAgentFromGCS("http://unused", m, "arm64", false); err == nil {
		t.Fatal("expected error for missing arch")
	}
}

func TestDownloadAgentFromGCSChecksumMismatch(t *testing.T) {
	tgz := makeAgentTarGz(t, "wendy-agent-linux-amd64/wendy-agent", []byte("x"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/agent/v1/a.tar.gz" {
			w.Write(tgz)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	m := &agentManifest{Latest: "v1", Versions: map[string]agentManifestVersion{
		"v1": {Artifacts: map[string]agentManifestArtifact{"amd64": {Path: "agent/v1/a.tar.gz", Checksum: "deadbeef"}}},
	}}
	if _, _, err := downloadAgentFromGCS(srv.URL, m, "amd64", false); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestDownloadAgentFromGCS -v`
Expected: FAIL — `undefined: downloadAgentFromGCS`.

- [ ] **Step 3: Implement**

```go
// add to agent_source.go (imports: add "crypto/sha256", "encoding/hex", "io", "strings")

func downloadAgentFromGCS(baseURL string, m *agentManifest, arch string, nightly bool) ([]byte, string, error) {
	version, err := agentVersionFromManifest(m, nightly)
	if err != nil {
		return nil, "", err
	}
	ver, ok := m.Versions[version]
	if !ok {
		return nil, "", fmt.Errorf("agent manifest missing version entry %q", version)
	}
	art, ok := ver.Artifacts[arch]
	if !ok || art.Path == "" {
		return nil, "", fmt.Errorf("agent manifest has no linux/%s artifact for version %s", arch, version)
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
	bin, err := extractAgentFromTarGz(bytes.NewReader(raw))
	if err != nil {
		return nil, "", err
	}
	return bin, version, nil
}

// resolveAgentBinary returns the wendy-agent binary for linux/<arch>, preferring
// GCS (to avoid GitHub rate limits) and falling back to GitHub releases on any
// GCS miss. version is the resolved version tag; source is "gcs" or "github".
func resolveAgentBinary(arch string, nightly bool) (binary []byte, version, source string, err error) {
	if m, mErr := fetchAgentManifest(); mErr == nil {
		if bin, ver, dErr := downloadAgentFromGCS(gcsBaseURL, m, arch, nightly); dErr == nil {
			return bin, ver, "gcs", nil
		} else {
			fmt.Fprintf(os.Stderr, "GCS agent download failed (%v); falling back to GitHub\n", dErr)
		}
	} else {
		fmt.Fprintf(os.Stderr, "GCS agent manifest fetch failed (%v); falling back to GitHub\n", mErr)
	}

	rel, err := fetchAgentRelease(nightly)
	if err != nil {
		return nil, "", "", err
	}
	assetPrefix := fmt.Sprintf("wendy-agent-linux-%s-", arch)
	var matched *githubReleaseAsset
	for i := range rel.Assets {
		a := rel.Assets[i]
		if strings.HasPrefix(a.Name, assetPrefix) && strings.HasSuffix(a.Name, ".tar.gz") {
			matched = &a
			break
		}
	}
	if matched == nil {
		return nil, "", "", fmt.Errorf("no asset for linux/%s in release %s", arch, rel.TagName)
	}
	bin, err := downloadAgentBinary(*matched)
	if err != nil {
		return nil, "", "", err
	}
	return bin, rel.TagName, "github", nil
}
```

Add `"bytes"` to `agent_source.go` imports (used by `bytes.NewReader`).

- [ ] **Step 4: Run tests + build**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestDownloadAgentFromGCS|TestExtractAgent|TestFetchAgentManifest|TestAgentVersion' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/agent_source.go go/internal/cli/commands/agent_source_test.go
git commit -m "feat(cli): resolveAgentBinary downloads agent from GCS with GitHub fallback"
```

---

## Task 5: Wire version-only call sites to `resolveAgentVersion`

Replace the two `fetchAgentRelease(...).TagName`-for-comparison sites.

**Files:**
- Modify: `go/internal/cli/commands/device.go:193-200` (`device info --check-updates`)
- Modify: `go/internal/cli/commands/os_cmd.go:922-940` (`ensureAgentUpToDate`)

**Interfaces:**
- Consumes: `resolveAgentVersion(nightly bool) (version, source string, err error)` (Task 3).

- [ ] **Step 1: Edit `device.go`** — replace the `checkUpdates` block (around lines 193-200):

```go
			var latestVersion string
			if checkUpdates {
				v, _, err := resolveAgentVersion(prerelease)
				if err != nil {
					return fmt.Errorf("checking for updates: %w", err)
				}
				latestVersion = v
			}
```

- [ ] **Step 2: Edit `os_cmd.go`** — in `ensureAgentUpToDate`, replace the `release, err := fetchAgentRelease(nightly)` block and the three later `release.TagName` uses with a `latestVer` string:

```go
	latestVer, _, err := resolveAgentVersion(nightly)
	if err != nil {
		fmt.Printf("Could not check for agent updates: %v\n", err)
		return conn, nil
	}

	// For nightly builds, update whenever the device isn't already running that
	// exact tag — a semver comparison would incorrectly treat nightly pre-release
	// tags as older than a stable release of the same base version.
	alreadyCurrent := nightly && latestVer == agentVer ||
		!nightly && version.CompareVersions(latestVer, agentVer) <= 0
	if alreadyCurrent {
		fmt.Printf("Agent is up to date (%s)\n", agentVer)
		return conn, nil
	}

	fmt.Printf("Updating agent: %s → %s\n", agentVer, latestVer)
```

(Leave the rest of `ensureAgentUpToDate` — the `performAgentUpdate` call and `conn.Close()` — unchanged.)

- [ ] **Step 3: Build + vet**

Run: `cd go && go build ./... && go vet ./internal/cli/commands/`
Expected: clean (no `release` unused-variable errors; if any remain, they indicate a leftover `release.` reference to convert to `latestVer`).

- [ ] **Step 4: Run package tests**

Run: `cd go && go test ./internal/cli/commands/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/device.go go/internal/cli/commands/os_cmd.go
git commit -m "feat(cli): use GCS-first version resolver for agent update checks"
```

---

## Task 6: Wire download call sites to `resolveAgentBinary`

Replace the five `fetchAgentRelease → match asset → downloadAgentBinary` blocks.

**Files:**
- Modify: `go/internal/cli/commands/helpers.go:1437-1459` (`performAgentUpdate`)
- Modify: `go/internal/cli/commands/device.go:2113-2141` (`device update`)
- Modify: `go/internal/cli/commands/discover.go:805-826`
- Modify: `go/internal/cli/commands/cloud_discover.go:399-443`
- Modify: `go/internal/cli/commands/os_install.go:2003-2023` (hardcoded arm64)

**Interfaces:**
- Consumes: `resolveAgentBinary(arch string, nightly bool) (binary []byte, version, source string, err error)` (Task 4).

- [ ] **Step 1: Edit `helpers.go` `performAgentUpdate`** — replace lines 1437-1459 (from `fmt.Fprintf(os.Stderr, "Fetching latest release...\n")` through the `downloadAgentBinary` block) with:

```go
	fmt.Fprintf(os.Stderr, "Fetching agent for linux/%s...\n", arch)
	binaryData, version, source, err := resolveAgentBinary(arch, nightly)
	if err != nil {
		return fmt.Errorf("resolving agent binary: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Downloaded agent %s (from %s)\n", version, source)

	h := sha256.Sum256(binaryData)
	sha256Hash := hex.EncodeToString(h[:])

	fmt.Fprintf(os.Stderr, "Uploading to device...\n")
	return deviceUpdateUpload(ctx, conn.AgentService, binaryData, sha256Hash)
```

- [ ] **Step 2: Edit `device.go` `device update`** — replace the block at lines 2105-2141 (from the `releaseType`/`Fetching latest ... release` messages through the `downloadAgentBinary` block) with:

```go
				if !jsonOutput {
					releaseType := "stable"
					if nightly {
						releaseType = "nightly"
					}
					fmt.Println(tui.InfoMessage(fmt.Sprintf("Fetching latest %s agent for linux/%s...", releaseType, arch)))
				}

				var source, resolvedVer string
				binaryData, resolvedVer, source, err = resolveAgentBinary(arch, nightly)
				if err != nil {
					return fmt.Errorf("resolving agent binary: %w", err)
				}
				if !jsonOutput {
					fmt.Printf("%s %s %s\n", tui.Dim("Release:"), tui.Value(resolvedVer), tui.Dim("(from "+source+")"))
				}
```

Note: `binaryData` is already declared in the enclosing scope (it is assigned via `binaryData, err = downloadAgentBinary(...)` today), so use `=` for it. `resolvedVer` and `source` are new — the `var source, resolvedVer string` line above declares them; adjust to `:=` only if `binaryData` were new (it is not). If the compiler reports `binaryData` redeclared, change the assignment to list form matching the existing declaration.

- [ ] **Step 3: Edit `discover.go`** — replace the whole block from `release, err := fetchAgentRelease(false)` (line 805) through the `downloadAgentBinary` block (ending line 828, i.e. up to and including its closing `}`) with:

```go
		binaryData, _, _, err := resolveAgentBinary(arch, false)
		if err != nil {
			conn.Close()
			return discoverUpdateDoneMsg{deviceName: name, err: fmt.Errorf("resolving agent binary: %w", err)}
		}
```

(The following `h := sha256.Sum256(binaryData)` and upload lines stay unchanged.)

- [ ] **Step 4: Edit `cloud_discover.go`** — this site needs the version early (for the up-to-date check, before connecting) and the binary later. Two edits in the `return func() tea.Msg { ... }`:

  (a) Replace `release, err := fetchAgentRelease(false)` (line 399) and its error block with:

```go
		latestVer, _, err := resolveAgentVersion(false)
		if err != nil {
			return discoverUpdateDoneMsg{assetID: id, deviceName: name, err: fmt.Errorf("resolving agent version: %w", err)}
		}
```

  (b) In the up-to-date check, change `release.TagName` to `latestVer`:

```go
		if agentVer != "" && version.CompareVersions(latestVer, agentVer) <= 0 {
```

  (c) Replace the asset-match block (from `assetPrefix := ...` through the `downloadAgentBinary` block, i.e. lines 429-448) with:

```go
		binaryData, _, _, err := resolveAgentBinary(arch, false)
		if err != nil {
			conn.Close()
			return discoverUpdateDoneMsg{assetID: id, deviceName: name, err: fmt.Errorf("resolving agent binary: %w", err)}
		}
```

(The following `h := sha256.Sum256(binaryData)` and upload lines stay unchanged.)

- [ ] **Step 5: Edit `os_install.go`** — `provisionConfigPartition` is hardcoded to arm64 and prints the version. Replace the block from `release, err := fetchAgentRelease(false)` (line 2003) through the `downloadAgentBinary` block (line 2023) with:

```go
	fmt.Printf("Downloading wendy-agent for device...\n")
	agentBinary, agentVer, _, err := resolveAgentBinary("arm64", false)
	if err != nil {
		return fmt.Errorf("resolving agent binary: %w", err)
	}
	fmt.Printf("Using wendy-agent %s\n", agentVer)
```

(The following `return writeConfigPartition(d, agentBinary, ...)` line stays unchanged.)

- [ ] **Step 6: Build + vet, prune now-unused helpers if any**

Run: `cd go && go build ./... && go vet ./internal/cli/commands/`
Expected: clean build. `fetchAgentRelease` and `downloadAgentBinary` are still used inside `resolveAgentVersion`/`resolveAgentBinary`, so they remain. If `go vet`/build flags an unused import (e.g. `strings` in a site that no longer matches asset prefixes), remove it.

- [ ] **Step 7: Run full package tests**

Run: `cd go && go test ./internal/cli/commands/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/helpers.go go/internal/cli/commands/device.go go/internal/cli/commands/discover.go go/internal/cli/commands/cloud_discover.go go/internal/cli/commands/os_install.go
git commit -m "feat(cli): download agent via GCS-first resolver at all call sites"
```

---

## Task 7: CI manifest-merge jq filter + test

The manifest read-modify-write logic, isolated as a `jq` program so it is unit-testable without GCS.

**Files:**
- Create: `.github/scripts/agent-manifest-merge.jq`
- Create: `.github/scripts/agent-manifest-merge_test.sh`

**Interfaces:**
- Produces: a jq filter that reads the current manifest on stdin and takes `--arg version`, `--argjson entry` (the version object `{is_nightly, artifacts:{...}}`), `--argjson is_release` (bool); emits the merged manifest with the version spliced in and the correct pointer (`latest` or `latest_nightly`) updated.

- [ ] **Step 1: Write the failing test**

```bash
#!/usr/bin/env bash
# .github/scripts/agent-manifest-merge_test.sh
set -euo pipefail
cd "$(dirname "$0")"

FILTER=agent-manifest-merge.jq
fail=0
check() { # name expected actual
  if [ "$2" != "$3" ]; then echo "FAIL $1: expected [$2] got [$3]"; fail=1; else echo "ok $1"; fi
}

ENTRY='{"is_nightly":true,"artifacts":{"amd64":{"path":"agent/v2/a.tar.gz","checksum":"c","size_bytes":1}}}'

# Case 1: empty manifest, nightly publish
OUT=$(echo '{"versions":{}}' | jq -f "$FILTER" --arg version v2 --argjson entry "$ENTRY" --argjson is_release false)
check "nightly.latest_nightly" v2 "$(echo "$OUT" | jq -r .latest_nightly)"
check "nightly.latest_absent"  null "$(echo "$OUT" | jq -r '.latest // "null"')"
check "nightly.version_stored" "agent/v2/a.tar.gz" "$(echo "$OUT" | jq -r '.versions.v2.artifacts.amd64.path')"

# Case 2: existing manifest, stable publish preserves prior nightly pointer + versions
PRIOR='{"latest_nightly":"v2","versions":{"v2":'"$ENTRY"'}}'
SENTRY='{"is_nightly":false,"artifacts":{"arm64":{"path":"agent/v3/b.tar.gz","checksum":"d","size_bytes":2}}}'
OUT=$(echo "$PRIOR" | jq -f "$FILTER" --arg version v3 --argjson entry "$SENTRY" --argjson is_release true)
check "stable.latest"          v3 "$(echo "$OUT" | jq -r .latest)"
check "stable.keeps_nightly"   v2 "$(echo "$OUT" | jq -r .latest_nightly)"
check "stable.keeps_old_ver"   "agent/v2/a.tar.gz" "$(echo "$OUT" | jq -r '.versions.v2.artifacts.amd64.path')"
check "stable.new_ver"         "agent/v3/b.tar.gz" "$(echo "$OUT" | jq -r '.versions.v3.artifacts.arm64.path')"

exit $fail
```

Make it executable: `chmod +x .github/scripts/agent-manifest-merge_test.sh`.

- [ ] **Step 2: Run test to verify it fails**

Run: `.github/scripts/agent-manifest-merge_test.sh`
Expected: FAIL — `jq: error: Could not open agent-manifest-merge.jq`.

- [ ] **Step 3: Write the jq filter**

```jq
# .github/scripts/agent-manifest-merge.jq
# Splice a new agent version into the manifest and update the channel pointer.
# Inputs: $version (string), $entry (version object), $is_release (bool).
.versions = ((.versions // {}) + { ($version): $entry })
| if $is_release then .latest = $version else .latest_nightly = $version end
```

- [ ] **Step 4: Run test to verify it passes**

Run: `.github/scripts/agent-manifest-merge_test.sh`
Expected: all `ok`, exit 0.

- [ ] **Step 5: Commit**

```bash
git add .github/scripts/agent-manifest-merge.jq .github/scripts/agent-manifest-merge_test.sh
git commit -m "ci: add tested jq filter for agent GCS manifest merge"
```

---

## Task 8: CI upload script + `publish-agent-gcs` job

Wire the upload into `build.yml`.

**Files:**
- Create: `.github/scripts/publish-agent-gcs.sh`
- Modify: `.github/workflows/build.yml` (add the `publish-agent-gcs` job)

**Interfaces:**
- Consumes: `.github/scripts/agent-manifest-merge.jq` (Task 7); env `VERSION`, `IS_RELEASE`, `BUCKET`, `PROJECT`; downloaded tarballs under `agent-artifacts/`.

- [ ] **Step 1: Write the upload script**

```bash
#!/usr/bin/env bash
# .github/scripts/publish-agent-gcs.sh
# Uploads wendy-agent linux tarballs to gs://$BUCKET/agent/$VERSION/ and merges
# gs://$BUCKET/agent/manifest.json (read-modify-write with generation-match).
set -euo pipefail

: "${VERSION:?}" "${IS_RELEASE:?}" "${BUCKET:?}" "${PROJECT:?}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
gcloud config set project "$PROJECT" >/dev/null

# 1) Upload each tarball and build the per-arch artifacts JSON.
artifacts='{}'
shopt -s nullglob
tarballs=(agent-artifacts/wendy-agent-linux-*-"${VERSION}".tar.gz)
if [ ${#tarballs[@]} -eq 0 ]; then
  echo "no agent tarballs found for version ${VERSION}" >&2
  exit 1
fi
for f in "${tarballs[@]}"; do
  base="$(basename "$f")"                              # wendy-agent-linux-<arch>-<version>.tar.gz
  arch="$(echo "$base" | sed -E 's/^wendy-agent-linux-([^-]+)-.*/\1/')"
  dest="agent/${VERSION}/${base}"
  echo "Uploading $f -> gs://${BUCKET}/${dest}"
  gcloud storage cp "$f" "gs://${BUCKET}/${dest}"
  sum="$(sha256sum "$f" | cut -d' ' -f1)"
  size="$(stat -c%s "$f")"
  artifacts="$(echo "$artifacts" | jq --arg arch "$arch" --arg path "$dest" --arg sum "$sum" --argjson size "$size" \
    '. + {($arch): {path:$path, checksum:$sum, size_bytes:$size}}')"
done

is_nightly=true
[ "$IS_RELEASE" = "true" ] && is_nightly=false
entry="$(jq -n --argjson nightly "$is_nightly" --argjson arts "$artifacts" '{is_nightly:$nightly, artifacts:$arts}')"

# 2) Read-modify-write the manifest with a generation-match precondition, retry once.
merge_and_upload() {
  local gen current merged
  if gcloud storage cp "gs://${BUCKET}/agent/manifest.json" current.json 2>/dev/null; then
    gen="$(gcloud storage objects describe "gs://${BUCKET}/agent/manifest.json" --format='value(generation)')"
  else
    echo '{"versions":{}}' > current.json
    gen=0
  fi
  merged="$(jq -f "${SCRIPT_DIR}/agent-manifest-merge.jq" \
    --arg version "$VERSION" --argjson entry "$entry" \
    --argjson is_release "$([ "$IS_RELEASE" = "true" ] && echo true || echo false)" \
    current.json)"
  echo "$merged" > manifest.json
  gcloud storage cp manifest.json "gs://${BUCKET}/agent/manifest.json" \
    --cache-control=no-store --if-generation-match="$gen"
}

if ! merge_and_upload; then
  echo "manifest write conflicted; retrying once..." >&2
  merge_and_upload
fi
echo "Published agent ${VERSION} (is_release=${IS_RELEASE}) to gs://${BUCKET}/agent/"
```

Make it executable: `chmod +x .github/scripts/publish-agent-gcs.sh`.

- [ ] **Step 2: Lint the script**

Run: `bash -n .github/scripts/publish-agent-gcs.sh && shellcheck .github/scripts/publish-agent-gcs.sh || true`
Expected: `bash -n` exits 0 (syntax OK). Address any shellcheck errors (warnings acceptable).

- [ ] **Step 3: Add the `publish-agent-gcs` job to `build.yml`**

Insert after the `publish-linux-repos` job (before `release`), matching the file's 2-space indentation:

```yaml
  publish-agent-gcs:
    name: Publish agent to GCS
    runs-on: ubuntu-latest
    needs: [determine-version, build]
    if: |
      always() &&
      needs.determine-version.result == 'success' &&
      needs.build.result == 'success' &&
      (github.event_name == 'push' ||
       (github.event_name == 'workflow_dispatch' && inputs.publish == true))
    permissions:
      contents: read
      id-token: write
    steps:
      - uses: actions/checkout@9c091bb21b7c1c1d1991bb908d89e4e9dddfe3e0 # v7.0.0

      - name: Download agent tarballs
        uses: actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c # v8
        with:
          pattern: wendy-agent-linux-*.tar.gz
          merge-multiple: true
          path: agent-artifacts

      - name: Authenticate to GCP
        uses: google-github-actions/auth@7c6bc770dae815cd3e89ee6cdf493a5fab2cc093 # v3
        with:
          workload_identity_provider: ${{ vars.GCP_WORKLOAD_IDENTITY_PROVIDER }}
          service_account: ${{ vars.GCP_SERVICE_ACCOUNT }}

      - name: Set up Cloud SDK
        uses: google-github-actions/setup-gcloud@aa5489c8933f4cc7a4f7d45035b3b1440c9c10db # v3

      - name: Upload agent tarballs and update manifest
        env:
          VERSION: ${{ needs.determine-version.outputs.version }}
          IS_RELEASE: ${{ needs.determine-version.outputs.is_release }}
          BUCKET: wendyos-images-public
          PROJECT: ${{ vars.GCP_PROJECT_ID }}
        run: ./.github/scripts/publish-agent-gcs.sh
```

- [ ] **Step 4: Validate workflow YAML**

Run: `cd go && python3 -c "import yaml,sys; yaml.safe_load(open('../.github/workflows/build.yml'))" && echo "YAML OK"`
Expected: `YAML OK`. (If `actionlint` is available, run it on `build.yml` and address errors.)

- [ ] **Step 5: Commit**

```bash
git add .github/scripts/publish-agent-gcs.sh .github/workflows/build.yml
git commit -m "ci: publish wendy-agent tarballs and manifest to GCS"
```

---

## Task 9: Final verification

- [ ] **Step 1: Full build + package tests**

Run: `cd go && go build ./... && go test ./internal/cli/commands/`
Expected: clean build, tests PASS.

- [ ] **Step 2: Confirm GitHub fallback path intact**

Grep to verify the GitHub functions still exist and are referenced only from the resolvers:

Run: `cd go && grep -rn "fetchAgentRelease\|downloadAgentBinary" internal/cli/commands/ | grep -v _test.go`
Expected: definitions in `device.go`, and references only inside `resolveAgentVersion`/`resolveAgentBinary` in `agent_source.go` — no remaining direct call sites elsewhere.

- [ ] **Step 3: Run the CI script tests once more**

Run: `.github/scripts/agent-manifest-merge_test.sh && bash -n .github/scripts/publish-agent-gcs.sh`
Expected: all `ok`, exit 0.

---

## Post-merge verification (unverified until a real main build; note in PR)

- After the first push-to-main build, confirm `gs://wendyos-images-public/agent/manifest.json` exists with a populated `latest_nightly` and `agent/<version>/wendy-agent-linux-{amd64,arm64}-<version>.tar.gz` objects.
- Run `wendy device update --nightly` against a device and confirm stderr shows the GCS path (no "falling back to GitHub" line).
- After a `workflow_dispatch` with `publish=true`, confirm `latest` advances while `latest_nightly` is preserved.
