# `wendy sandbox` CLI command Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `wendy sandbox {install,start,stop,status,uninstall}` so the Wendy Sandbox native macOS app's "Setup" button (currently a stub) has a real command to invoke: install/run the local control-plane dev-sandbox backend as a launchd-managed background service.

**Architecture:** `wendy sandbox install` downloads the latest control-plane release tarball (published by the companion `control-plane-release.yml` workflow in `wendy-sandbox`), unpacks it, runs `npm ci --omit=dev`, reads-or-generates the same admin-credentials JSON file the Swift app uses, writes a launchd `LaunchAgent` plist, and loads it. `start`/`stop`/`status`/`uninstall` are thin `launchctl` wrappers.

**Tech Stack:** Go 1.26, cobra, stdlib only (`net/http`, `archive/tar`, `compress/gzip`, `text/template`, `os/exec`) — no new go.mod dependencies.

**Depends on:** `wendy-sandbox` repo plan `desktop-native/docs/superpowers/plans/2026-08-02-control-plane-release-workflow.md` (must be merged and have produced at least one `control-plane-latest` release before this plan's manual-verification task can succeed — the unit-testable tasks don't need it).

## Global Constraints

- Repo/tag to fetch releases from: `wendylabsinc/wendy-sandbox`, tag `control-plane-latest`, asset `control-plane.tar.gz`. Note: this is a **prerelease** tag, so it must be fetched via `GET /repos/<repo>/releases/tags/<tag>`, NOT `/releases/latest` (GitHub's `/latest` endpoint explicitly excludes prereleases and would 404).
- Admin credentials file: `~/Library/Application Support/WendySandboxNative/admin-credentials.json`, JSON shape `{"user": "...", "password": "..."}` (exact keys, no snake_case — matches `AdminCredentialStore` in `desktop-native/Sources/WendySandbox/AdminCredentials.swift`). Password format when generated fresh: 18 random bytes, base64 URL-safe, no padding (matches `AdminCredentialStore.randomPassword`).
- Fixed port: `8787` (matches `AppConfig.preferredPort` in `desktop-native`).
- launchd LaunchAgent label: `sh.wendy.sandbox-control-plane`.
- Install directory: `~/.wendy/sandbox/control-plane/`.
- Package: all new files go in `package commands` (`go/internal/cli/commands/`) — NOT `providers`, because the GitHub API helpers this needs (`newGitHubAPIClient`, `newGitHubAPIGetRequest` in `github.go`) live in `commands`, and `providers` cannot import `commands` (commands already imports providers, e.g. for `apple_container_setup.go` — importing the other way would be circular).
- Every task must leave `make vet`, `gofmt -l -s .` (empty output), and `make test` (from `go/`) green.

---

### Task 1: Release fetch + download/extract

**Files:**
- Create: `go/internal/cli/commands/sandbox_release.go`
- Test: `go/internal/cli/commands/sandbox_release_test.go`

**Interfaces:**
- Produces: `fetchControlPlaneRelease(ctx context.Context) (*sandboxRelease, error)`, `findControlPlaneAsset(rel *sandboxRelease) (url string, err error)`, `downloadAndExtractControlPlaneRelease(ctx context.Context, downloadURL, destDir string) error`, `extractControlPlaneTarGz(r io.Reader, destDir string) error` (exposed for the test to exercise without real HTTP).
- Consumes (existing, same package): `newGitHubAPIClient(timeout time.Duration) *http.Client`, `newGitHubAPIGetRequest(rawURL string) (*http.Request, error)` (both in `github.go`).

- [ ] **Step 1: Write the failing tests**

```go
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
		"control-plane/package.json":     `{"name":"sandbox-control-plane"}`,
		"control-plane/dist/index.js":    "console.log('hi')",
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

func TestFindControlPlaneAsset_ReturnsMatchingAssetURL(t *testing.T) {
	rel := &sandboxRelease{
		TagName: "control-plane-latest",
		Assets: []sandboxReleaseAsset{
			{Name: "other-file.txt", BrowserDownloadURL: "https://example.com/other"},
			{Name: "control-plane.tar.gz", BrowserDownloadURL: "https://example.com/control-plane.tar.gz"},
		},
	}
	url, err := findControlPlaneAsset(rel)
	if err != nil {
		t.Fatalf("findControlPlaneAsset: %v", err)
	}
	if url != "https://example.com/control-plane.tar.gz" {
		t.Errorf("url = %q", url)
	}
}

func TestFindControlPlaneAsset_MissingAssetErrors(t *testing.T) {
	rel := &sandboxRelease{TagName: "control-plane-latest", Assets: nil}
	if _, err := findControlPlaneAsset(rel); err == nil {
		t.Fatal("expected error when the release has no matching asset")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `go/`): `go test ./internal/cli/commands/... -run TestExtractControlPlaneTarGz -run TestFindControlPlaneAsset -v`
Expected: FAIL to build — none of `extractControlPlaneTarGz`, `sandboxRelease`, `sandboxReleaseAsset`, `findControlPlaneAsset` exist yet.

- [ ] **Step 3: Implement**

```go
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
	TagName string                 `json:"tag_name"`
	Assets  []sandboxReleaseAsset  `json:"assets"`
}

// fetchControlPlaneRelease resolves the control-plane-latest release. Uses the
// tags endpoint, not /releases/latest — that endpoint excludes prereleases,
// and control-plane-latest is published as one (see control-plane-release.yml).
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run (from `go/`): `go test ./internal/cli/commands/... -run TestExtractControlPlaneTarGz -run TestFindControlPlaneAsset -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/sandbox_release.go go/internal/cli/commands/sandbox_release_test.go
git commit -m "feat: add control-plane release fetch/download/extract"
```

---

### Task 2: Admin credentials read-or-generate

**Files:**
- Create: `go/internal/cli/commands/sandbox_credentials.go`
- Test: `go/internal/cli/commands/sandbox_credentials_test.go`

**Interfaces:**
- Produces: `sandboxAdminCredentials{User, Password string}`, `readOrGenerateSandboxCredentialsAt(path string) (sandboxAdminCredentials, error)` (the injectable, testable core), `readOrGenerateSandboxCredentials() (sandboxAdminCredentials, error)` (resolves the real path and calls the above), `sandboxCredentialsPath() (string, error)`.

- [ ] **Step 1: Write the failing tests**

```go
package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestReadOrGenerateSandboxCredentialsAt_GeneratesWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-credentials.json")

	creds, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatalf("readOrGenerateSandboxCredentialsAt: %v", err)
	}
	if creds.User != "admin" {
		t.Errorf("User = %q, want admin", creds.User)
	}
	if len(creds.Password) == 0 {
		t.Error("Password is empty")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading persisted file: %v", err)
	}
	var onDisk sandboxAdminCredentials
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatalf("unmarshalling persisted file: %v", err)
	}
	if onDisk != creds {
		t.Errorf("persisted %+v != returned %+v", onDisk, creds)
	}
}

func TestReadOrGenerateSandboxCredentialsAt_ReadsExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-credentials.json")
	want := sandboxAdminCredentials{User: "someone", Password: "existing-secret"}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatalf("readOrGenerateSandboxCredentialsAt: %v", err)
	}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestReadOrGenerateSandboxCredentialsAt_IsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "admin-credentials.json")

	first, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := readOrGenerateSandboxCredentialsAt(path)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Errorf("second call generated a different password: %+v != %+v", first, second)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `go/`): `go test ./internal/cli/commands/... -run TestReadOrGenerateSandboxCredentials -v`
Expected: FAIL to build — `sandboxAdminCredentials`/`readOrGenerateSandboxCredentialsAt` don't exist.

- [ ] **Step 3: Implement**

```go
package commands

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// sandboxAdminCredentials is the exact JSON shape desktop-native's
// AdminCredentialStore reads/writes (Sources/WendySandbox/AdminCredentials.swift)
// — plain "user"/"password" keys, no key transformation.
type sandboxAdminCredentials struct {
	User     string `json:"user"`
	Password string `json:"password"`
}

func sandboxCredentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, "Library", "Application Support", "WendySandboxNative", "admin-credentials.json"), nil
}

func readOrGenerateSandboxCredentials() (sandboxAdminCredentials, error) {
	path, err := sandboxCredentialsPath()
	if err != nil {
		return sandboxAdminCredentials{}, err
	}
	return readOrGenerateSandboxCredentialsAt(path)
}

// readOrGenerateSandboxCredentialsAt reads path if it already holds valid
// credentials (written by either the Swift app or a prior CLI run), or
// generates and persists a fresh one — so whichever side runs first defines
// the shared secret and the other always reads it back.
func readOrGenerateSandboxCredentialsAt(path string) (sandboxAdminCredentials, error) {
	if data, err := os.ReadFile(path); err == nil {
		var creds sandboxAdminCredentials
		if err := json.Unmarshal(data, &creds); err == nil && creds.User != "" && creds.Password != "" {
			return creds, nil
		}
	}
	password, err := generateSandboxPassword()
	if err != nil {
		return sandboxAdminCredentials{}, fmt.Errorf("generating admin password: %w", err)
	}
	creds := sandboxAdminCredentials{User: "admin", Password: password}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return sandboxAdminCredentials{}, fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	data, err := json.Marshal(creds)
	if err != nil {
		return sandboxAdminCredentials{}, err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return sandboxAdminCredentials{}, fmt.Errorf("writing %s: %w", path, err)
	}
	return creds, nil
}

// generateSandboxPassword matches AdminCredentialStore.randomPassword in
// desktop-native: 18 random bytes, base64 URL-safe, no padding.
func generateSandboxPassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run (from `go/`): `go test ./internal/cli/commands/... -run TestReadOrGenerateSandboxCredentials -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/sandbox_credentials.go go/internal/cli/commands/sandbox_credentials_test.go
git commit -m "feat: read-or-generate shared admin credentials for the sandbox control plane"
```

---

### Task 3: launchd plist generation + launchctl wrappers

**Files:**
- Create: `go/internal/cli/commands/sandbox_launchd.go`
- Test: `go/internal/cli/commands/sandbox_launchd_test.go`

**Interfaces:**
- Produces: `sandboxLaunchAgentLabel` (const `"sh.wendy.sandbox-control-plane"`), `sandboxPlistParams{Label, WorkDir, LogPath, Port, AdminUser, AdminPassword, DataDir string}`, `renderSandboxPlist(p sandboxPlistParams) (string, error)`, `sandboxLaunchctlPlistPath() (string, error)`, `loadSandboxLaunchAgent(ctx context.Context, plistPath string) error`, `unloadSandboxLaunchAgent(ctx context.Context) error`, `sandboxLaunchAgentStatus(ctx context.Context) (running bool, err error)`.
- Consumes: none new (stdlib `text/template`, `os/exec` only).

- [ ] **Step 1: Write the failing tests**

```go
package commands

import (
	"strings"
	"testing"
)

func TestRenderSandboxPlist_SubstitutesAllFields(t *testing.T) {
	xml, err := renderSandboxPlist(sandboxPlistParams{
		Label: "sh.wendy.sandbox-control-plane", WorkDir: "/tmp/cp", LogPath: "/tmp/cp.log",
		Port: "8787", AdminUser: "admin", AdminPassword: "s3cr3t", DataDir: "/tmp/cp-data",
	})
	if err != nil {
		t.Fatalf("renderSandboxPlist: %v", err)
	}
	for _, want := range []string{
		"<string>sh.wendy.sandbox-control-plane</string>",
		"<string>/tmp/cp</string>",
		"<string>/tmp/cp.log</string>",
		"<string>8787</string>",
		"<string>admin</string>",
		"<string>s3cr3t</string>",
		"<string>/tmp/cp-data</string>",
		"<key>KeepAlive</key>",
		"<true/>",
	} {
		if !strings.Contains(xml, want) {
			t.Errorf("rendered plist missing %q\nfull output:\n%s", want, xml)
		}
	}
}

func TestRenderSandboxPlist_EscapesXMLSpecialCharacters(t *testing.T) {
	xml, err := renderSandboxPlist(sandboxPlistParams{
		Label: "sh.wendy.sandbox-control-plane", WorkDir: "/tmp/cp", LogPath: "/tmp/cp.log",
		Port: "8787", AdminUser: "admin", AdminPassword: `a&b<c>d`, DataDir: "/tmp/cp-data",
	})
	if err != nil {
		t.Fatalf("renderSandboxPlist: %v", err)
	}
	if strings.Contains(xml, "a&b<c>d") {
		t.Error("rendered plist contains un-escaped XML special characters in the password")
	}
	if !strings.Contains(xml, "a&amp;b&lt;c&gt;d") {
		t.Errorf("rendered plist missing escaped password\nfull output:\n%s", xml)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run (from `go/`): `go test ./internal/cli/commands/... -run TestRenderSandboxPlist -v`
Expected: FAIL to build — `renderSandboxPlist`/`sandboxPlistParams` don't exist.

- [ ] **Step 3: Implement**

```go
package commands

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const sandboxLaunchAgentLabel = "sh.wendy.sandbox-control-plane"

type sandboxPlistParams struct {
	Label         string
	WorkDir       string
	LogPath       string
	Port          string
	AdminUser     string
	AdminPassword string
	DataDir       string
}

const sandboxPlistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/bin/env</string>
		<string>node</string>
		<string>dist/index.js</string>
	</array>
	<key>WorkingDirectory</key>
	<string>{{.WorkDir}}</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PORT</key>
		<string>{{.Port}}</string>
		<key>DRIVER</key>
		<string>docker</string>
		<key>PUBLIC_HOST</key>
		<string>localhost</string>
		<key>ADMIN_USER</key>
		<string>{{.AdminUser}}</string>
		<key>ADMIN_PASSWORD</key>
		<string>{{.AdminPassword}}</string>
		<key>DATA_DIR</key>
		<string>{{.DataDir}}</string>
		<key>PATH</key>
		<string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
	</dict>
	<key>StandardOutPath</key>
	<string>{{.LogPath}}</string>
	<key>StandardErrorPath</key>
	<string>{{.LogPath}}</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
</dict>
</plist>
`

// renderSandboxPlist fills the launchd plist template. Fields are XML-escaped
// before substitution since AdminPassword is base64url in practice but this
// keeps the function correct regardless.
func renderSandboxPlist(p sandboxPlistParams) (string, error) {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	p.Label, p.WorkDir, p.LogPath, p.Port = esc(p.Label), esc(p.WorkDir), esc(p.LogPath), esc(p.Port)
	p.AdminUser, p.AdminPassword, p.DataDir = esc(p.AdminUser), esc(p.AdminPassword), esc(p.DataDir)

	tmpl, err := template.New("sandbox-plist").Parse(sandboxPlistTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, p); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func sandboxLaunchctlPlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", sandboxLaunchAgentLabel+".plist"), nil
}

func sandboxLaunchdTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), sandboxLaunchAgentLabel)
}

func loadSandboxLaunchAgent(ctx context.Context, plistPath string) error {
	cmd := exec.CommandContext(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), plistPath)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %w", plistPath, err)
	}
	return nil
}

func unloadSandboxLaunchAgent(ctx context.Context) error {
	target := sandboxLaunchdTarget()
	cmd := exec.CommandContext(ctx, "launchctl", "bootout", target)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl bootout %s: %w", target, err)
	}
	return nil
}

// sandboxLaunchAgentStatus reports whether the LaunchAgent is registered with
// launchd. `launchctl print` exits non-zero when the service isn't loaded —
// that's the normal "not installed" case, not an error to surface.
func sandboxLaunchAgentStatus(ctx context.Context) (bool, error) {
	target := sandboxLaunchdTarget()
	cmd := exec.CommandContext(ctx, "launchctl", "print", target)
	if err := cmd.Run(); err != nil {
		return false, nil
	}
	return true, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run (from `go/`): `go test ./internal/cli/commands/... -run TestRenderSandboxPlist -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/sandbox_launchd.go go/internal/cli/commands/sandbox_launchd_test.go
git commit -m "feat: add launchd plist generation and launchctl wrappers for the sandbox control plane"
```

---

### Task 4: `wendy sandbox` cobra command + registration

**Files:**
- Create: `go/internal/cli/commands/sandbox.go`
- Modify: `go/internal/cli/commands/root.go`

**Interfaces:**
- Consumes (Tasks 1-3, same package): `fetchControlPlaneRelease`, `findControlPlaneAsset`, `downloadAndExtractControlPlaneRelease`, `readOrGenerateSandboxCredentials`, `sandboxPlistParams`, `renderSandboxPlist`, `sandboxLaunchctlPlistPath`, `loadSandboxLaunchAgent`, `unloadSandboxLaunchAgent`, `sandboxLaunchAgentStatus`, `sandboxLaunchAgentLabel`.
- Produces: `newSandboxCmd() *cobra.Command`.

There's no unit-testable "failing test first" step for this task — it's cobra command wiring plus an install/start/stop/status/uninstall orchestration function that shells out to real system state (network, npm, launchd). Verification is: it builds, `wendy sandbox --help` shows the expected subcommands, and Task 5's manual verification exercises the real behavior end-to-end.

- [ ] **Step 1: Write `sandbox.go`**

```go
package commands

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newSandboxCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage the local Wendy Sandbox control plane",
		Long: "Install and run the local control-plane service that Wendy Sandbox\n" +
			"(the native macOS app) uses for session containers, terminal, and the sim viewer.",
	}
	var purge bool
	uninstallCmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Unload and remove the local control plane",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSandboxUninstall(context.Background(), cmd, purge)
		},
	}
	uninstallCmd.Flags().BoolVar(&purge, "purge", false, "also remove the cached install directory")

	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Install and start the local control plane (safe to re-run)",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxInstall(context.Background(), cmd)
			},
		},
		&cobra.Command{
			Use:   "start",
			Short: "Start the local control plane",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxStart(context.Background(), cmd)
			},
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Stop the local control plane",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxStop(context.Background(), cmd)
			},
		},
		&cobra.Command{
			Use:   "status",
			Short: "Report whether the local control plane is running",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runSandboxStatus(context.Background(), cmd)
			},
		},
		uninstallCmd,
	)
	return cmd
}

func runSandboxInstall(ctx context.Context, cmd *cobra.Command) error {
	if _, err := exec.LookPath("node"); err != nil {
		return fmt.Errorf("node is required but not found on PATH; run: brew install node")
	}
	if _, err := exec.LookPath("npm"); err != nil {
		return fmt.Errorf("npm is required but not found on PATH; run: brew install node")
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	installDir := filepath.Join(home, ".wendy", "sandbox", "control-plane")

	cmd.Println("Fetching latest control-plane release…")
	rel, err := fetchControlPlaneRelease(ctx)
	if err != nil {
		return err
	}
	assetURL, err := findControlPlaneAsset(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", installDir, err)
	}
	if err := downloadAndExtractControlPlaneRelease(ctx, assetURL, installDir); err != nil {
		return err
	}

	cmd.Println("Installing dependencies…")
	npmCmd := exec.CommandContext(ctx, "npm", "ci", "--omit=dev")
	npmCmd.Dir = installDir
	npmCmd.Stdout, npmCmd.Stderr = os.Stdout, os.Stderr
	if err := npmCmd.Run(); err != nil {
		return fmt.Errorf("npm ci --omit=dev in %s: %w", installDir, err)
	}

	creds, err := readOrGenerateSandboxCredentials()
	if err != nil {
		return err
	}

	dataDir := filepath.Join(home, "Library", "Application Support", "WendySandboxNative", "control-plane-data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dataDir, err)
	}
	logPath := filepath.Join(home, "Library", "Logs", "wendy-sandbox-control-plane.log")

	plist, err := renderSandboxPlist(sandboxPlistParams{
		Label: sandboxLaunchAgentLabel, WorkDir: installDir, LogPath: logPath,
		Port: "8787", AdminUser: creds.User, AdminPassword: creds.Password, DataDir: dataDir,
	})
	if err != nil {
		return err
	}
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(plistPath), err)
	}
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", plistPath, err)
	}

	// Unload first (ignore errors — it may not be loaded yet) so a re-run of
	// install picks up a new plist/version instead of launchd keeping the old one.
	_ = unloadSandboxLaunchAgent(ctx)
	if err := loadSandboxLaunchAgent(ctx, plistPath); err != nil {
		return err
	}

	cmd.Println("control-plane installed and running on http://localhost:8787")
	cmd.Println("Check status any time with: wendy sandbox status")
	return nil
}

func runSandboxStart(ctx context.Context, cmd *cobra.Command) error {
	target := sandboxLaunchdTarget()
	if err := exec.CommandContext(ctx, "launchctl", "kickstart", "-k", target).Run(); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w (not installed yet? run: wendy sandbox install)", target, err)
	}
	cmd.Println("control-plane started.")
	return nil
}

func runSandboxStop(ctx context.Context, cmd *cobra.Command) error {
	target := sandboxLaunchdTarget()
	if err := exec.CommandContext(ctx, "launchctl", "kill", "SIGTERM", target).Run(); err != nil {
		return fmt.Errorf("launchctl kill SIGTERM %s: %w", target, err)
	}
	cmd.Println("control-plane stopped.")
	return nil
}

func runSandboxStatus(ctx context.Context, cmd *cobra.Command) error {
	running, err := sandboxLaunchAgentStatus(ctx)
	if err != nil {
		return err
	}
	if running {
		cmd.Printf("control-plane is installed and loaded (%s)\n", sandboxLaunchdTarget())
	} else {
		cmd.Println("control-plane is not installed; run: wendy sandbox install")
	}
	return nil
}

func runSandboxUninstall(ctx context.Context, cmd *cobra.Command, purge bool) error {
	if err := unloadSandboxLaunchAgent(ctx); err != nil {
		cmd.PrintErrln("warning:", err)
	}
	plistPath, err := sandboxLaunchctlPlistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", plistPath, err)
	}
	if purge {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		if err := os.RemoveAll(filepath.Join(home, ".wendy", "sandbox")); err != nil {
			return fmt.Errorf("removing install directory: %w", err)
		}
	}
	cmd.Println("control-plane uninstalled.")
	return nil
}
```

- [ ] **Step 2: Register in `root.go`**

Add `"sandbox"` to the `root.AddGroup(...)` call (near `root.go:113-118`):

```go
root.AddGroup(
    &cobra.Group{ID: "develop", Title: "Develop & Deploy:"},
    &cobra.Group{ID: "sandbox", Title: "Sandbox:"},
    &cobra.Group{ID: "manage", Title: "Manage:"},
    &cobra.Group{ID: "cloud", Title: "Cloud:"},
    &cobra.Group{ID: "settings", Title: "Settings:"},
)
```

Construct and assign the group near the other command constructions (~`root.go:120-149`):

```go
sandboxCmd := newSandboxCmd()
sandboxCmd.GroupID = "sandbox"
```

Add it to the `root.AddCommand(...)` call (~`root.go:211-242`) in display order alongside the other visible commands.

- [ ] **Step 3: Build and smoke-check help output**

Run (from `go/`):
```bash
go build ./... && go run ./cmd/wendy sandbox --help
```
Expected: shows `install`, `start`, `stop`, `status`, `uninstall` subcommands with the short descriptions above.

- [ ] **Step 4: Run the full test suite and formatting/vet gates**

Run (from `go/`):
```bash
gofmt -l -s .    # must print nothing
go vet ./...
make test
```
Expected: `gofmt -l -s .` empty, `go vet` clean, all tests pass (including Tasks 1-3's new tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/sandbox.go go/internal/cli/commands/root.go
git commit -m "feat: add wendy sandbox command group"
```

---

### Task 5: Manual verification

Requires the companion `wendy-sandbox` plan's workflow to have published at least one `control-plane-latest` release, and Docker Desktop running locally (control-plane's driver).

- [ ] **Step 1: Fresh install**

```bash
wendy sandbox install
curl -u "$(plutil -extract user raw -o - ~/Library/Application\ Support/WendySandboxNative/admin-credentials.json):$(plutil -extract password raw -o - ~/Library/Application\ Support/WendySandboxNative/admin-credentials.json)" http://localhost:8787/admin
```
Expected: install succeeds, the `curl` returns a 200/401 (proving something real is listening and using the same credentials the CLI wrote/read).

- [ ] **Step 2: status / stop / start**

```bash
wendy sandbox status   # reports loaded
wendy sandbox stop
wendy sandbox status   # still "loaded" (launchd keeps the registration even when the process is down) — confirm via `curl` above failing instead
wendy sandbox start
```

- [ ] **Step 3: Re-run install (idempotency)**

```bash
wendy sandbox install
```
Expected: succeeds again without error, credentials file is unchanged (same password as Step 1), LaunchAgent reloads cleanly.

- [ ] **Step 4: Uninstall**

```bash
wendy sandbox uninstall --purge
launchctl print gui/$(id -u)/sh.wendy.sandbox-control-plane   # should now report "could not find service"
ls ~/.wendy/sandbox   # should not exist
```

- [ ] **Step 5: End-to-end with desktop-native**

With `wendy-sandbox`'s `desktop-native` app built from the `decouple-control-plane` branch (or its merged result), launch the app with NO control plane running, click Setup once wired to actually invoke `wendy sandbox install` (this plan doesn't wire the button itself — that's a follow-up in `desktop-native` once this CLI command exists; for now, run `wendy sandbox install` manually from a terminal while the app is open) and confirm the app's gated routes come alive within a few seconds without a restart.
