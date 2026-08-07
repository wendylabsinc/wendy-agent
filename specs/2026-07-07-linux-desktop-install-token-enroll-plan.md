# Linux Desktop Install with Token Pre-Enrollment — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a "Linux Desktop" entry to the `wendy install` picker that prints `agent.sh` install instructions carrying a short-lived enrollment token, and teach `wendy-agent` to self-enroll from that token on first startup.

**Architecture:** The CLI mints a short-lived asset enrollment token (`CreateAssetEnrollmentToken`, TTL 1h) and prints a `curl … | WENDY_ENROLLMENT_TOKEN=… WENDY_CLOUD_HOST=… bash` one-liner. `agent.sh` persists those env vars to `/etc/wendy-agent/enrollment.json` (0600). On startup the agent reads that file and calls its existing `StartProvisioning` pipeline (key-gen → CSR → `IssueCertificate` → save), deriving org/asset from the token's claims, then deletes the file.

**Tech Stack:** Go 1.x, cobra, gRPC (agentpb / cloudpb), bash (`agent.sh`), Go `testing`.

## Global Constraints

- Enrollment token TTL: **3600 seconds (1 hour)**, passed as `CreateAssetEnrollmentTokenRequest.TtlSeconds`.
- Env vars: exactly **`WENDY_ENROLLMENT_TOKEN`** and **`WENDY_CLOUD_HOST`**. The agent derives org/asset from token claims; do not add more env vars.
- Enrollment handoff file: **`/etc/wendy-agent/enrollment.json`**, mode **`0600`**, JSON `{"token": "...", "cloudHost": "..."}`, consumed-and-deleted by the agent.
- `agent.sh` (`go/internal/cli/assets/docs/agent.sh`) is the published `install.wendy.dev/agent.sh` source; new logic must be inert unless `WENDY_ENROLLMENT_TOKEN` is set (backward-safe).
- `WENDY_CLOUD_HOST` value is the auth session's `auth.CloudGRPC` verbatim (e.g. `cloud.wendy.dev:443`); the agent's `certificateServiceAddr` handles host/port normalization.
- Follow existing package patterns: stubbable package-level func vars for tests (as in `os_install_enroll.go`), and the `newTestProvisioningService` / `startFakeCloudServer` harness for agent tests.

---

### Task 1: Shared asset-token claim parser

Extract the JWT-payload claim decoding currently embedded in the CLI's `enrollmentTokenCommonName` into a shared package so both the CLI and the agent decode `org_id`/`asset_id` the same way.

**Files:**
- Create: `go/internal/shared/enrolltoken/enrolltoken.go`
- Create: `go/internal/shared/enrolltoken/enrolltoken_test.go`
- Modify: `go/internal/cli/commands/auth.go` (`enrollmentTokenCommonName` uses the shared parser)

**Interfaces:**
- Produces:
  - `type Claims struct { OrganizationID int32; AssetID int32; UserID string; Type string }`
  - `func Parse(token string) (Claims, error)` — decodes the base64url JSON payload (second dot-separated segment); errors on `<2` segments, bad base64, or bad JSON.
  - `func ParseAsset(token string) (orgID, assetID int32, err error)` — calls `Parse`, requires `Type == "asset_enrollment"` with non-zero `org_id` and `asset_id`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/shared/enrolltoken/enrolltoken_test.go`:

```go
package enrolltoken

import (
	"encoding/base64"
	"testing"
)

// makeToken builds a fake JWT-shaped token: "<header>.<payloadJSON>.<sig>".
func makeToken(t *testing.T, payloadJSON string) string {
	t.Helper()
	seg := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return "header." + seg + ".sig"
}

func TestParseAsset_Valid(t *testing.T) {
	tok := makeToken(t, `{"type":"asset_enrollment","org_id":7,"asset_id":42}`)
	orgID, assetID, err := ParseAsset(tok)
	if err != nil {
		t.Fatalf("ParseAsset: %v", err)
	}
	if orgID != 7 || assetID != 42 {
		t.Fatalf("got org=%d asset=%d, want 7/42", orgID, assetID)
	}
}

func TestParseAsset_RejectsUserToken(t *testing.T) {
	tok := makeToken(t, `{"type":"user_enrollment","org_id":1,"user_id":"u-1"}`)
	if _, _, err := ParseAsset(tok); err == nil {
		t.Fatal("expected error for user token, got nil")
	}
}

func TestParseAsset_Malformed(t *testing.T) {
	if _, _, err := ParseAsset("not-a-token"); err == nil {
		t.Fatal("expected error for malformed token, got nil")
	}
}

func TestParseAsset_MissingIDs(t *testing.T) {
	tok := makeToken(t, `{"type":"asset_enrollment","org_id":0,"asset_id":0}`)
	if _, _, err := ParseAsset(tok); err == nil {
		t.Fatal("expected error for missing org/asset, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/enrolltoken/...`
Expected: FAIL — build error, package/functions not defined.

- [ ] **Step 3: Write minimal implementation**

Create `go/internal/shared/enrolltoken/enrolltoken.go`:

```go
// Package enrolltoken decodes the (unverified) claims payload of a Wendy
// enrollment token. It never validates the signature — that is the cloud's
// job at certificate-issuance time. It exists so the CLI and the agent derive
// org/asset identity from a token identically.
package enrolltoken

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Claims holds the fields Wendy embeds in an enrollment token payload.
type Claims struct {
	OrganizationID int32  `json:"org_id"`
	AssetID        int32  `json:"asset_id"`
	UserID         string `json:"user_id"`
	Type           string `json:"type"`
}

// Parse decodes the base64url JSON payload (the second dot-separated segment)
// of an enrollment token. It does not verify the signature.
func Parse(token string) (Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return Claims{}, fmt.Errorf("invalid enrollment token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("decoding token payload: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, fmt.Errorf("decoding token claims: %w", err)
	}
	return c, nil
}

// ParseAsset decodes an asset-enrollment token and returns its org and asset
// IDs. It errors on any other token type or missing IDs.
func ParseAsset(token string) (orgID, assetID int32, err error) {
	c, err := Parse(token)
	if err != nil {
		return 0, 0, err
	}
	if c.Type != "asset_enrollment" {
		return 0, 0, fmt.Errorf("not an asset enrollment token (type %q)", c.Type)
	}
	if c.OrganizationID == 0 || c.AssetID == 0 {
		return 0, 0, fmt.Errorf("asset enrollment token missing org_id or asset_id")
	}
	return c.OrganizationID, c.AssetID, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/shared/enrolltoken/...`
Expected: PASS.

- [ ] **Step 5: Refactor the CLI to use the shared parser**

In `go/internal/cli/commands/auth.go`, replace the body of `enrollmentTokenCommonName` (the function that splits the token, base64-decodes `parts[1]`, and unmarshals a local `claims` struct) with a call to the shared parser. Add the import `"github.com/wendylabsinc/wendy/go/internal/shared/enrolltoken"` and drop the now-unused `encoding/base64` import if nothing else in the file uses it:

```go
func enrollmentTokenCommonName(token string) (string, error) {
	claims, err := enrolltoken.Parse(token)
	if err != nil {
		return "", err
	}
	switch claims.Type {
	case "user_enrollment":
		if claims.UserID == "" {
			return "", fmt.Errorf("user enrollment token missing user_id")
		}
		return fmt.Sprintf("wendy/user/%s", claims.UserID), nil
	case "asset_enrollment":
		if claims.OrganizationID == 0 || claims.AssetID == 0 {
			return "", fmt.Errorf("asset enrollment token missing org_id or asset_id")
		}
		return fmt.Sprintf("wendy/%d/%d", claims.OrganizationID, claims.AssetID), nil
	default:
		return "", fmt.Errorf("unsupported enrollment token type %q", claims.Type)
	}
}
```

- [ ] **Step 6: Run the CLI auth tests to verify no regression**

Run: `cd go && go test ./internal/cli/commands/ -run TestEnrollmentTokenCommonName`
Expected: PASS (existing `TestEnrollmentTokenCommonName_*` cases still pass).

- [ ] **Step 7: Commit**

```bash
git add go/internal/shared/enrolltoken/ go/internal/cli/commands/auth.go
git commit -m "feat: shared enrollment-token claim parser (enrolltoken)"
```

---

### Task 2: CLI — render install instructions (pure function)

A pure, table-testable renderer for the printed instructions. No I/O, no cloud calls.

**Files:**
- Create: `go/internal/cli/commands/os_install_linux_desktop.go`
- Create: `go/internal/cli/commands/os_install_linux_desktop_test.go`

**Interfaces:**
- Produces:
  - `const linuxDesktopValue = "linux-desktop"`
  - `const linuxDesktopAgentURL = "https://install.wendy.dev/agent.sh"`
  - `func renderLinuxDesktopInstructions(token, cloudHost, orgName string, expiresAt time.Time) string` — when `token == ""` renders the plain (unenrolled) instructions; otherwise renders the enrolled instructions including `orgName` and a human expiry.

- [ ] **Step 1: Write the failing test**

Create `go/internal/cli/commands/os_install_linux_desktop_test.go`:

```go
//go:build darwin || linux || windows

package commands

import (
	"strings"
	"testing"
	"time"
)

func TestRenderLinuxDesktopInstructions_Plain(t *testing.T) {
	out := renderLinuxDesktopInstructions("", "", "", time.Time{})
	if !strings.Contains(out, "curl -fsSL https://install.wendy.dev/agent.sh | bash") {
		t.Fatalf("plain output missing curl command:\n%s", out)
	}
	if strings.Contains(out, "WENDY_ENROLLMENT_TOKEN") {
		t.Fatalf("plain output should not mention a token:\n%s", out)
	}
	if !strings.Contains(out, "wendy device enroll") {
		t.Fatalf("plain output should point at later enrollment:\n%s", out)
	}
}

func TestRenderLinuxDesktopInstructions_Enrolled(t *testing.T) {
	exp := time.Date(2026, 7, 7, 15, 4, 5, 0, time.UTC)
	out := renderLinuxDesktopInstructions("tok-abc", "cloud.wendy.dev:443", "Acme", exp)
	for _, want := range []string{
		"WENDY_ENROLLMENT_TOKEN=tok-abc",
		"WENDY_CLOUD_HOST=cloud.wendy.dev:443",
		"install.wendy.dev/agent.sh",
		"Acme",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("enrolled output missing %q:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestRenderLinuxDesktopInstructions`
Expected: FAIL — `renderLinuxDesktopInstructions` undefined.

- [ ] **Step 3: Write minimal implementation**

Create `go/internal/cli/commands/os_install_linux_desktop.go`:

```go
//go:build darwin || linux || windows

package commands

import (
	"fmt"
	"strings"
	"time"
)

const (
	linuxDesktopValue    = "linux-desktop"
	linuxDesktopAgentURL = "https://install.wendy.dev/agent.sh"
)

// renderLinuxDesktopInstructions returns the text printed when the user picks
// "Linux Desktop". With an empty token it prints the plain (unenrolled) docs
// command; with a token it prints the pre-enrollment one-liner.
func renderLinuxDesktopInstructions(token, cloudHost, orgName string, expiresAt time.Time) string {
	var b strings.Builder
	if token == "" {
		fmt.Fprintf(&b, "Install wendy-agent on your Linux machine:\n\n")
		fmt.Fprintf(&b, "  curl -fsSL %s | bash\n\n", linuxDesktopAgentURL)
		fmt.Fprintf(&b, "The device is discovered over your local network — run `wendy discover`.\n")
		fmt.Fprintf(&b, "To enroll it into an org later, run `wendy device enroll`\n")
		fmt.Fprintf(&b, "(or re-run `wendy install` while logged in for a pre-enrollment token).\n")
		return b.String()
	}
	fmt.Fprintf(&b, "Install wendy-agent on your Linux machine; it will enroll into %s automatically.\n\n", orgName)
	fmt.Fprintf(&b, "  curl -fsSL %s | \\\n", linuxDesktopAgentURL)
	fmt.Fprintf(&b, "    WENDY_ENROLLMENT_TOKEN=%s WENDY_CLOUD_HOST=%s bash\n\n", token, cloudHost)
	fmt.Fprintf(&b, "This enrollment token expires at %s (about 1 hour). Run the command before then.\n", expiresAt.Format(time.RFC1123))
	fmt.Fprintf(&b, "After it boots, run `wendy discover` to find the device.\n")
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/cli/commands/ -run TestRenderLinuxDesktopInstructions`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/os_install_linux_desktop.go go/internal/cli/commands/os_install_linux_desktop_test.go
git commit -m "feat: render Linux Desktop install instructions"
```

---

### Task 3: CLI — token minting + `installLinuxDesktop` handler + picker wiring

Wire the picker entry, route it to a handler that applies the pre-enroll gate, mints a short-lived token when enrolling, and prints the instructions.

**Files:**
- Modify: `go/internal/cli/commands/os_install_linux_desktop.go` (add `installLinuxDesktop`, `createLinuxDesktopToken`, `linuxDesktopTokenFn`)
- Modify: `go/internal/cli/commands/os_install.go` (picker item + routing)
- Modify: `go/internal/cli/commands/os_install_linux_desktop_test.go` (handler tests)

**Interfaces:**
- Consumes: `renderLinuxDesktopInstructions` (Task 2); `selectEnrollmentAuth`, `confirmPreEnroll`, `ackContinueUnenrolled`, `preEnrollOptions`, `preEnrollMode` (`os_install_enroll.go`); `resolveOrg` (`org_picker.go`); `cloudContext` (`cloud_tunnel.go`); `isInteractiveTerminal`; `config.Load`; `certs.LoadTLSConfig` (`os_provision.go` shows the dial pattern); `cloudpb.NewCertificateServiceClient`.
- Produces:
  - `func installLinuxDesktop(ctx context.Context, preOpts preEnrollOptions, deviceName string) error`
  - `var linuxDesktopTokenFn = createLinuxDesktopToken` (stub point for tests)
  - `func createLinuxDesktopToken(ctx context.Context, auth *config.AuthConfig, deviceName string, orgID int32) (token string, expiresAt time.Time, err error)`

- [ ] **Step 1: Write the failing test**

Append to `go/internal/cli/commands/os_install_linux_desktop_test.go`:

```go
func TestInstallLinuxDesktop_SkipMode_PrintsPlain(t *testing.T) {
	// preEnrollSkip must never mint a token, even if a token fn is present.
	called := false
	origTok := linuxDesktopTokenFn
	linuxDesktopTokenFn = func(_ context.Context, _ *config.AuthConfig, _ string, _ int32) (string, time.Time, error) {
		called = true
		return "should-not-be-used", time.Time{}, nil
	}
	t.Cleanup(func() { linuxDesktopTokenFn = origTok })

	out := captureStdout(t, func() {
		if err := installLinuxDesktop(context.Background(), preEnrollOptions{mode: preEnrollSkip}, ""); err != nil {
			t.Fatalf("installLinuxDesktop: %v", err)
		}
	})
	if called {
		t.Fatal("token fn must not be called in skip mode")
	}
	if !strings.Contains(out, "curl -fsSL https://install.wendy.dev/agent.sh | bash") {
		t.Fatalf("expected plain instructions:\n%s", out)
	}
}
```

Add these imports to the test file's import block: `"context"`, `"github.com/wendylabsinc/wendy/go/internal/shared/config"`.

If a `captureStdout` helper does not already exist in the package's test files, add it to this test file:

```go
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := osStdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	osStdout = w
	t.Cleanup(func() { osStdout = old })
	done := make(chan string, 1)
	go func() {
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if err != nil {
				break
			}
		}
		done <- sb.String()
	}()
	fn()
	w.Close()
	osStdout = old
	return <-done
}
```

Note: the handler must write via a package-level `osStdout` (default `os.Stdout`) so the test can capture it. Add `import "os"` to the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestInstallLinuxDesktop_SkipMode`
Expected: FAIL — `installLinuxDesktop`, `linuxDesktopTokenFn`, `osStdout` undefined.

- [ ] **Step 3: Write minimal implementation**

In `go/internal/cli/commands/os_install_linux_desktop.go`, extend the imports to:

```go
import (
	"context"
	"crypto/x509" // not needed here; remove if unused
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/wendylabsinc/wendy/go/internal/shared/certs"
	"github.com/wendylabsinc/wendy/go/internal/shared/config"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)
```

(Only import what you use — drop `crypto/x509` if the final code doesn't reference it.)

Add the stdout indirection and the handler + token minter:

```go
// osStdout is indirected so tests can capture the printed instructions.
var osStdout = os.Stdout

var linuxDesktopTokenFn = createLinuxDesktopToken

// installLinuxDesktop prints agent.sh install instructions for turning an
// existing Linux machine into a managed Wendy device. When the user is logged
// in and does not decline, it mints a short-lived enrollment token and prints
// the pre-enrollment one-liner. It never writes a drive or downloads an image.
func installLinuxDesktop(ctx context.Context, preOpts preEnrollOptions, deviceName string) error {
	interactive := isInteractiveTerminal()

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{} // treat an unreadable config as "not logged in"
	}

	enroll := false
	switch preOpts.mode {
	case preEnrollSkip:
		enroll = false
	case preEnrollForced:
		enroll = true
	case preEnrollAuto:
		if interactive && len(cfg.Auth) > 0 {
			ok, cErr := confirmPreEnroll()
			if cErr != nil {
				return cErr
			}
			enroll = ok
		}
	}

	var token, cloudHost, orgName string
	var expiresAt time.Time
	if enroll {
		auth, aErr := selectEnrollmentAuth(cfg, preOpts.cloudGRPC, interactive)
		if aErr != nil {
			if errorsIsUserCancelled(aErr) {
				return aErr
			}
			if !interactive {
				return fmt.Errorf("--pre-enroll: %w", aErr)
			}
			fmt.Fprintf(osStdout, "Cannot pre-enroll: %v\n", aErr)
			// fall through to plain instructions
		} else if auth != nil {
			org, oErr := resolveOrg(ctx, auth, false)
			if oErr != nil {
				if errorsIsUserCancelled(oErr) {
					return oErr
				}
				if !interactive {
					return fmt.Errorf("--pre-enroll: resolving organization: %w", oErr)
				}
				fmt.Fprintf(osStdout, "Cannot resolve organization: %v\n", oErr)
			} else {
				tok, exp, tErr := linuxDesktopTokenFn(ctx, auth, deviceName, org.ID)
				if tErr != nil {
					if !interactive {
						return fmt.Errorf("--pre-enroll: creating enrollment token: %w", tErr)
					}
					fmt.Fprintf(osStdout, "Could not create enrollment token: %v\n", tErr)
				} else {
					token, cloudHost, orgName, expiresAt = tok, auth.CloudGRPC, org.Name, exp
				}
			}
		}
	}

	fmt.Fprint(osStdout, renderLinuxDesktopInstructions(token, cloudHost, orgName, expiresAt))
	return nil
}

// createLinuxDesktopToken mints a short-lived asset enrollment token for org.
// The CLI does NOT issue a certificate — only the token is handed to the
// device, which self-enrolls. Mirrors the cloud dial in preEnrollDevice.
func createLinuxDesktopToken(ctx context.Context, auth *config.AuthConfig, deviceName string, orgID int32) (string, time.Time, error) {
	if len(auth.Certificates) == 0 {
		return "", time.Time{}, fmt.Errorf("auth session has no certificates; re-run 'wendy cloud login'")
	}
	cert := auth.Certificates[0]

	var transportOpt grpc.DialOption
	if strings.HasSuffix(auth.CloudGRPC, ":443") {
		tlsCfg, err := certs.LoadTLSConfig(cert.PemCertificate, cert.PemCertificateChain, cert.PemPrivateKey, "")
		if err != nil {
			return "", time.Time{}, fmt.Errorf("loading TLS config: %w", err)
		}
		transportOpt = grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg))
	} else {
		transportOpt = grpc.WithTransportCredentials(insecure.NewCredentials())
	}

	conn, err := grpc.NewClient(auth.CloudGRPC, transportOpt)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("connecting to cloud: %w", err)
	}
	defer conn.Close()

	if orgID == 0 {
		orgID = int32(cert.OrganizationID)
	}
	resp, err := cloudpb.NewCertificateServiceClient(conn).CreateAssetEnrollmentToken(cloudContext(ctx, auth), &cloudpb.CreateAssetEnrollmentTokenRequest{
		OrganizationId: orgID,
		Name:           deviceName,
		TtlSeconds:     3600,
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("creating enrollment token: %w", err)
	}
	var expiresAt time.Time
	if resp.GetExpiresAt() != nil {
		expiresAt = resp.GetExpiresAt().AsTime()
	}
	return resp.GetEnrollmentToken(), expiresAt, nil
}

// errorsIsUserCancelled reports whether err is the package cancel sentinel.
func errorsIsUserCancelled(err error) bool {
	return errors.Is(err, ErrUserCancelled)
}
```

Add `"errors"` to the import block (used by `errorsIsUserCancelled`). If an equivalent helper already exists in the package, use it instead of adding `errorsIsUserCancelled`.

- [ ] **Step 4: Wire the picker entry and routing in `os_install.go`**

In `runOSInstall`, inside the `if flagDeviceType == "" && prNumber == 0 {` block that adds ESP32 items (around lines 322–344), after the ESP32 loop append the Linux Desktop item:

```go
		items = append(items, tui.PickerItem{
			Name:        "Linux Desktop",
			Description: "Install wendy-agent on an existing Linux machine",
			Section:     "Linux Desktop",
			SortKey:     "2_linux_desktop",
			Value:       linuxDesktopValue,
		})
```

Then, immediately after the picker resolves `selected` and before `device := deviceMap[selected]` (just after the existing `if selected == thorDeviceType { … }` block around line 383–385), add:

```go
	if selected == linuxDesktopValue {
		return installLinuxDesktop(ctx, preOpts, deviceName)
	}
```

- [ ] **Step 5: Run the tests**

Run: `cd go && go test ./internal/cli/commands/ -run 'TestInstallLinuxDesktop|TestRenderLinuxDesktopInstructions'`
Expected: PASS.

- [ ] **Step 6: Build to confirm wiring compiles**

Run: `cd go && go build ./...`
Expected: no errors.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/os_install_linux_desktop.go go/internal/cli/commands/os_install_linux_desktop_test.go go/internal/cli/commands/os_install.go
git commit -m "feat: Linux Desktop picker entry with token pre-enrollment"
```

---

### Task 4: `agent.sh` — stage the enrollment file from env vars

Teach the installer to persist the token env vars to `/etc/wendy-agent/enrollment.json` after a successful install, and document the env vars.

**Files:**
- Modify: `go/internal/cli/assets/docs/agent.sh`

**Interfaces:**
- Consumes: `WENDY_ENROLLMENT_TOKEN`, `WENDY_CLOUD_HOST` env vars; `$SUDO` (already defined in the script).
- Produces: `/etc/wendy-agent/enrollment.json` (0600) when the token is set.

- [ ] **Step 1: Document the env vars in usage**

In the `usage()` heredoc's `Environment:` block (currently only `WENDY_VERSION`), add:

```
  WENDY_ENROLLMENT_TOKEN
                  Pre-enroll this device into a Wendy Cloud org on first start.
                  Obtain it from 'wendy install' → "Linux Desktop".
  WENDY_CLOUD_HOST
                  Wendy Cloud gRPC host (required when WENDY_ENROLLMENT_TOKEN is set).
```

- [ ] **Step 2: Add the enrollment-staging function**

After the `confirm()` function definition (near the other helper functions, before `ARCH=$(detect_arch)`), add:

```bash
# --- Stage a pre-enrollment token for the agent to self-enroll on startup ---
stage_enrollment() {
  local token="${WENDY_ENROLLMENT_TOKEN:-}"
  local cloud_host="${WENDY_CLOUD_HOST:-}"
  if [[ -z "$token" ]]; then
    return 0
  fi
  if [[ -z "$cloud_host" ]]; then
    echo "Warning: WENDY_ENROLLMENT_TOKEN is set but WENDY_CLOUD_HOST is not; skipping pre-enrollment." >&2
    return 0
  fi
  $SUDO mkdir -p /etc/wendy-agent
  # Write via a heredoc through tee so the token is not echoed to stdout.
  # The values are JSON-encoded assuming no embedded quotes/backslashes (tokens
  # are base64url + dots; cloud host is a hostname[:port]).
  printf '{"token":"%s","cloudHost":"%s"}\n' "$token" "$cloud_host" \
    | $SUDO tee /etc/wendy-agent/enrollment.json >/dev/null
  $SUDO chmod 600 /etc/wendy-agent/enrollment.json
  echo "Enrollment token staged; the device will enroll on startup."
  # Nudge an already-running (package-installed) agent to re-read it.
  if command -v systemctl &>/dev/null; then
    $SUDO systemctl try-restart wendy-agent >/dev/null 2>&1 || true
  fi
}
```

- [ ] **Step 3: Call it at the end of install (the shared verify tail)**

At the very end of the script, in the `# --- Verify ---` section, after the existing `if command -v "$BINARY_NAME" … fi` block and before the final newline, add:

```bash
stage_enrollment
```

- [ ] **Step 4: Syntax-check the script**

Run: `bash -n go/internal/cli/assets/docs/agent.sh`
Expected: no output (exit 0).

- [ ] **Step 5: Behavior test — file staged only when the token is set**

Run this inline check (uses a fake `sudo`/`systemctl`/`tee` PATH and a temp root to avoid touching the real `/etc`):

```bash
cd go/internal/cli/assets/docs
# Extract and source just the helpers we need in a subshell test.
tmp=$(mktemp -d)
cat > "$tmp/test.sh" <<'SH'
set -euo pipefail
SUDO=""
ROOT="$1"
# shim: rewrite /etc paths under $ROOT for the test
mkdir_p() { command mkdir -p "$ROOT$2"; }
# minimal reimplementation mirroring stage_enrollment, writing under $ROOT
stage_enrollment() {
  local token="${WENDY_ENROLLMENT_TOKEN:-}"
  local cloud_host="${WENDY_CLOUD_HOST:-}"
  [[ -z "$token" ]] && return 0
  [[ -z "$cloud_host" ]] && { echo "skip: no host" >&2; return 0; }
  command mkdir -p "$ROOT/etc/wendy-agent"
  printf '{"token":"%s","cloudHost":"%s"}\n' "$token" "$cloud_host" > "$ROOT/etc/wendy-agent/enrollment.json"
  chmod 600 "$ROOT/etc/wendy-agent/enrollment.json"
}
stage_enrollment
SH
# no token -> no file
( unset WENDY_ENROLLMENT_TOKEN WENDY_CLOUD_HOST; bash "$tmp/test.sh" "$tmp/a" )
test ! -f "$tmp/a/etc/wendy-agent/enrollment.json" && echo "PASS: no file without token"
# token+host -> file with expected contents
( WENDY_ENROLLMENT_TOKEN=tok WENDY_CLOUD_HOST=cloud.wendy.dev:443 bash "$tmp/test.sh" "$tmp/b" )
grep -q '"token":"tok"' "$tmp/b/etc/wendy-agent/enrollment.json" && echo "PASS: file staged with token"
rm -rf "$tmp"
```

Expected: prints `PASS: no file without token` and `PASS: file staged with token`.

(This validates the staging logic mirrored from `stage_enrollment`. The real function is exercised end-to-end during manual device testing — see the final verification note.)

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/assets/docs/agent.sh
git commit -m "feat(agent.sh): stage pre-enrollment token from env vars"
```

---

### Task 5: Agent — self-enroll from the staged enrollment file on startup

Consume `/etc/wendy-agent/enrollment.json` at startup and run the existing provisioning pipeline.

**Files:**
- Create: `go/internal/agent/services/enrollment_file.go`
- Create: `go/internal/agent/services/enrollment_file_test.go`
- Modify: `go/cmd/wendy-agent/main.go` (invoke the hook after the provisioning callbacks are wired)

**Interfaces:**
- Consumes: `enrolltoken.ParseAsset` (Task 1); `ProvisioningService.StartProvisioning`, `ProvisioningService.ProvisioningInfo`, `s.configPath`, `s.logger` (existing).
- Produces:
  - `func (s *ProvisioningService) ApplyEnrollmentFile(ctx context.Context)` — best-effort; reads, self-enrolls, deletes.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/services/enrollment_file_test.go`:

```go
package services

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

// assetToken builds a fake asset-enrollment token with the given org/asset.
func assetToken(t *testing.T, org, asset int32) string {
	t.Helper()
	payload := []byte(`{"type":"asset_enrollment","org_id":` +
		itoa(org) + `,"asset_id":` + itoa(asset) + `}`)
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}

func itoa(v int32) string {
	// small helper to avoid importing strconv in the token string build
	return func() string { return sprintfInt(v) }()
}

func TestApplyEnrollmentFile_EnrollsAndDeletes(t *testing.T) {
	svc, tmpDir := newTestProvisioningService(t) // fake cloud dialer returns a canned cert
	path := filepath.Join(tmpDir, "enrollment.json")
	tok := assetToken(t, 7, 42)
	if err := os.WriteFile(path, []byte(`{"token":"`+tok+`","cloudHost":"cloud.example:443"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	svc.ApplyEnrollmentFile(context.Background())

	if _, _, _, enrolled := svc.ProvisioningInfo(); !enrolled {
		t.Fatal("expected agent to be enrolled after ApplyEnrollmentFile")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected enrollment.json to be deleted, stat err = %v", err)
	}
}

func TestApplyEnrollmentFile_MalformedDeletesNoCrash(t *testing.T) {
	svc, tmpDir := newTestProvisioningService(t)
	path := filepath.Join(tmpDir, "enrollment.json")
	if err := os.WriteFile(path, []byte(`{"token":"garbage","cloudHost":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	svc.ApplyEnrollmentFile(context.Background())

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected malformed enrollment.json to be deleted, stat err = %v", err)
	}
	if _, _, _, enrolled := svc.ProvisioningInfo(); enrolled {
		t.Fatal("agent must not be enrolled from a malformed token")
	}
}

func TestApplyEnrollmentFile_AbsentIsNoop(t *testing.T) {
	svc, _ := newTestProvisioningService(t)
	svc.ApplyEnrollmentFile(context.Background()) // must not panic
	if _, _, _, enrolled := svc.ProvisioningInfo(); enrolled {
		t.Fatal("no enrollment file: agent must stay unenrolled")
	}
}

func sprintfInt(v int32) string {
	return itoaStd(int(v))
}
```

Add a tiny helper at the bottom of the test file (kept separate so the token
builder stays readable):

```go
func itoaStd(v int) string {
	return fmtSprint(v)
}
```

Simplify: instead of the indirection above, the implementer MAY replace
`assetToken`'s numeric formatting with `strconv.Itoa` directly and delete
`itoa`/`sprintfInt`/`itoaStd`/`fmtSprint`. The intent is only "build a token
string with these two integers". Use whichever is cleanest; the assertions are
what matter.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestApplyEnrollmentFile`
Expected: FAIL — `ApplyEnrollmentFile` undefined (and/or the token-helper compile errors, which you resolve by using `strconv.Itoa`).

- [ ] **Step 3: Write minimal implementation**

Create `go/internal/agent/services/enrollment_file.go`:

```go
package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/wendylabsinc/wendy/go/internal/shared/enrolltoken"
	"github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// enrollmentFileName is the handoff file agent.sh writes with a short-lived
// enrollment token for a Linux Desktop install.
const enrollmentFileName = "enrollment.json"

type stagedEnrollment struct {
	Token     string `json:"token"`
	CloudHost string `json:"cloudHost"`
}

// ApplyEnrollmentFile self-enrolls the agent from a token staged by agent.sh at
// <configPath>/enrollment.json, then deletes the file. It is best-effort and
// safe to call unconditionally at startup: absent file is a no-op; an
// already-enrolled agent just clears the file; a malformed or failed token is
// logged and the file removed (the 1h TTL self-limits it anyway).
func (s *ProvisioningService) ApplyEnrollmentFile(ctx context.Context) {
	path := filepath.Join(s.configPath, enrollmentFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		s.logger.Error("Failed to read enrollment file", zap.String("path", path), zap.Error(err))
		s.removeEnrollmentFile(path)
		return
	}

	if _, _, _, enrolled := s.ProvisioningInfo(); enrolled {
		s.logger.Info("Agent already enrolled; discarding staged enrollment token")
		s.removeEnrollmentFile(path)
		return
	}

	var req stagedEnrollment
	if err := json.Unmarshal(data, &req); err != nil {
		s.logger.Error("Failed to parse enrollment file, removing", zap.Error(err))
		s.removeEnrollmentFile(path)
		return
	}
	if req.Token == "" || req.CloudHost == "" {
		s.logger.Error("Enrollment file is incomplete, removing")
		s.removeEnrollmentFile(path)
		return
	}

	orgID, assetID, err := enrolltoken.ParseAsset(req.Token)
	if err != nil {
		s.logger.Error("Enrollment token is not a valid asset token, removing", zap.Error(err))
		s.removeEnrollmentFile(path)
		return
	}

	// Bounded retry to tolerate a slow boot-time network. The token's short TTL
	// caps how long a failing token stays useful, so we do not retry forever.
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		_, lastErr = s.StartProvisioning(ctx, &agentpb.StartProvisioningRequest{
			EnrollmentToken: req.Token,
			CloudHost:       req.CloudHost,
			OrganizationId:  orgID,
			AssetId:         assetID,
		})
		if lastErr == nil {
			break
		}
		s.logger.Warn("Self-enrollment attempt failed",
			zap.Int("attempt", attempt), zap.Error(lastErr))
		if attempt < 3 {
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	if lastErr != nil {
		s.logger.Error("Self-enrollment from staged token failed; run 'wendy device enroll' to retry",
			zap.Error(lastErr))
	} else {
		s.logger.Info("Self-enrolled from staged token",
			zap.Int32("org_id", orgID), zap.Int32("asset_id", assetID))
	}
	s.removeEnrollmentFile(path)
}

func (s *ProvisioningService) removeEnrollmentFile(path string) {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		s.logger.Warn("Failed to remove enrollment file", zap.String("path", path), zap.Error(err))
	}
}
```

Then clean up the test's token helper: replace the `itoa`/`sprintfInt`/`itoaStd`/`fmtSprint` scaffolding with `strconv.Itoa`, e.g.:

```go
import "strconv"

func assetToken(t *testing.T, org, asset int32) string {
	t.Helper()
	payload := []byte(`{"type":"asset_enrollment","org_id":` +
		strconv.Itoa(int(org)) + `,"asset_id":` + strconv.Itoa(int(asset)) + `}`)
	return "h." + base64.RawURLEncoding.EncodeToString(payload) + ".s"
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd go && go test ./internal/agent/services/ -run TestApplyEnrollmentFile`
Expected: PASS.

- [ ] **Step 5: Wire the hook into agent startup**

In `go/cmd/wendy-agent/main.go`, after the `provisioningSvc.OnProvisioned` and `provisioningSvc.OnUnprovisioned` callbacks are assigned (after the block ending around line 660, so a successful enroll flips the mDNS advertisement), add:

```go
	// Self-enroll from a token staged by agent.sh (Linux Desktop install).
	// Best-effort and non-blocking: a cloud outage must never delay the agent
	// coming up locally (mDNS discovery still works unenrolled).
	go provisioningSvc.ApplyEnrollmentFile(context.Background())
```

Ensure `context` is imported in `main.go` (it almost certainly already is; if not, add it).

- [ ] **Step 6: Build and run the full service + agent tests**

Run: `cd go && go build ./... && go test ./internal/agent/services/ -run 'TestApplyEnrollmentFile|TestStartProvisioning|TestIsProvisioned'`
Expected: build clean, tests PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/services/enrollment_file.go go/internal/agent/services/enrollment_file_test.go go/cmd/wendy-agent/main.go
git commit -m "feat(agent): self-enroll from staged enrollment token on startup"
```

---

### Task 6: Docs — mention pre-enrollment on the Linux Desktop page

Keep the published docs page consistent with the new env-var capability.

**Files:**
- Modify: `go/internal/cli/assets/docs/installation/wendy-agent-linux.mdx`

- [ ] **Step 1: Add a pre-enrollment note**

After the `## Installation` code block (the `curl … | bash` fence, around line 24), add:

```markdown
### Pre-enrolling into your org (optional)

Running `wendy install` and choosing **Linux Desktop** while logged in prints a
one-liner that carries a short-lived enrollment token:

```bash
curl -fsSL https://install.wendy.dev/agent.sh | \
  WENDY_ENROLLMENT_TOKEN=<token> WENDY_CLOUD_HOST=<cloud-host> bash
```

The agent enrolls into your organization on first startup — no `wendy device
enroll` step needed. The token expires after about an hour, so run the command
promptly. Without these variables the agent installs unenrolled and is
discovered over your local network as described above.
```

- [ ] **Step 2: Commit**

```bash
git add go/internal/cli/assets/docs/installation/wendy-agent-linux.mdx
git commit -m "docs: pre-enrollment token on the Linux Desktop page"
```

---

## Final Verification (manual / hardware)

These are not automated (they need a real Linux target and a live cloud session):

1. `wendy cloud login`, then `wendy install` → pick **Linux Desktop** → confirm
   the printed one-liner contains `WENDY_ENROLLMENT_TOKEN=` and `WENDY_CLOUD_HOST=`.
2. Run the one-liner on a Linux box; confirm `/etc/wendy-agent/enrollment.json`
   is created (0600) then disappears after the agent starts.
3. `wendy discover` / cloud dashboard shows the device enrolled in the org.
4. Re-run `wendy install` → Linux Desktop while **logged out**; confirm the plain
   `curl … | bash` command (no token) is printed.

---

## Self-Review

**Spec coverage:**
- Component 1 (CLI picker + handler + token mint, unauth fallback) → Tasks 2, 3. ✔
- Component 2 (agent.sh env vars + staging file) → Task 4. ✔
- Component 3 (agent startup self-enroll, shared claim parser, delete-after-use, bounded retry, non-blocking) → Tasks 1, 5. ✔
- Security (0600, short TTL, delete after attempt, no key in command) → enforced in Tasks 3/4/5. ✔
- Testing (CLI render, handler gate, shared parser, agent hook, agent.sh staging) → Tasks 1–5. ✔
- Docs parity → Task 6. ✔

**Placeholder scan:** No TBD/TODO; every code step shows full code. The one
flexible spot (Task 5 test token-builder scaffolding) is explicitly resolved to
`strconv.Itoa` in Step 3.

**Type consistency:** `linuxDesktopValue`, `renderLinuxDesktopInstructions(token,
cloudHost, orgName, expiresAt)`, `linuxDesktopTokenFn(ctx, auth, deviceName,
orgID) (string, time.Time, error)`, `enrolltoken.ParseAsset(token) (int32,
int32, error)`, and `ApplyEnrollmentFile(ctx)` are used consistently across
tasks. The staging file path (`/etc/wendy-agent/enrollment.json`) matches the
agent's `configPath` join in Task 5.
```
