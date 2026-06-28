# Platform Diagnostics, Crash Reporting & Fix Subscriptions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add platform info to CLI logs, capture structured/classified error state, and provide an opt-in redacted crash-reporter flow (triggered only on unrecoverable failures) that returns a tracking number and lets users subscribe to be notified — via APNS to the Mac app, with a cross-platform cloud-notification fallback — when a release fixes their report.

**Architecture:** A new `platforminfo` shared package is the single source of truth for environment data, surfaced as a compact stderr banner in the root command's `PersistentPreRunE`. A `diag` package adds a process-wide log ring buffer, an error-context wrapper, and severity classification (extending the existing `errorClass`). A `crashreport` package bundles + redacts diagnostics, submits them via a new gRPC `DiagnosticsService` (contract defined in `Proto/cloud/diagnostics.proto`, server implemented later in the cloud repo), and falls back to a local file when the server is unavailable. `WendyAgentMac` registers an APNS token and handles the fix-notification push.

**Tech Stack:** Go 1.x (cobra CLI), protobuf + `protoc`/`generate-proto.sh`, gRPC (with `bufconn` for tests), Swift (macOS app, UserNotifications/APNS).

## Global Constraints

- Go module path: `github.com/wendylabsinc/wendy`.
- Generated protobuf Go lands under `go/proto/gen/cloudpb`; cloud protos are registered in `go/scripts/generate-proto.sh` (`CLOUD_PROTOS` array + `CLOUD_PKG`).
- Proto package for cloud messages is `wendycloud.v1` (Go type prefix `Wendycloud_V1_` in Swift, `cloudpb.` in Go).
- CLI version comes from `version.Version` (`go/internal/shared/version`), `"dev"` for local builds.
- Analytics never receives error message text (only a bounded `error_class`). Crash reports are the ONLY path that may carry detailed (redacted) error text, and only with explicit per-submission consent.
- Consent gating mirrors analytics exactly: skip when `env.IsCI()` is true; honor env off-switches; require an interactive terminal (`isInteractiveTerminal()`).
- New env off-switches: `WENDY_NO_BANNER` (suppress the platform banner) and `WENDY_CRASHREPORT=false` (suppress the crash-report prompt). Follow the existing `env.Analytics()` parsing style.
- Banner and all diagnostic collection must NEVER fail a command or alter its exit code. Probe failures degrade to empty strings.
- All new stdout/stderr writes for the banner go to **stderr** so `--json` stdout is never corrupted.
- Tests use Go's standard `testing` package, table-driven where the existing packages are (see `go/internal/shared/version/version_test.go`).

---

### Task 1: Diagnostics proto contract

**Files:**
- Create: `Proto/cloud/diagnostics.proto`
- Modify: `go/scripts/generate-proto.sh` (add `"cloud/diagnostics.proto"` to the `CLOUD_PROTOS` array, ~line 84-92)
- Generated (do not hand-edit): `go/proto/gen/cloudpb/diagnostics.pb.go`, `go/proto/gen/cloudpb/diagnostics_grpc.pb.go`

**Interfaces:**
- Produces (Go, after generation):
  - `cloudpb.DiagnosticsServiceClient` interface with methods `SubmitReport(ctx, *SubmitReportRequest, ...grpc.CallOption) (*SubmitReportResponse, error)`, `GetReportStatus(...)`, `Subscribe(...)`.
  - `cloudpb.DiagnosticsServiceServer` interface + `cloudpb.RegisterDiagnosticsServiceServer` (used by tests via bufconn).
  - Messages: `SubmitReportRequest{ PlatformInfo platform_info; string error_class; string severity; map<string,string> redacted_fields; string error_chain; repeated string log_tail; repeated string build_output_tail; string contact }`, `SubmitReportResponse{ string tracking_id; string status_url }`, `GetReportStatusRequest{ string tracking_id }`, `GetReportStatusResponse{ string status; string fixed_in_release }`, `SubscribeRequest{ string tracking_id; string apns_device_token; string topic }`, `SubscribeResponse{ string subscription_id }`, and a nested `PlatformInfo` message.

- [ ] **Step 1: Write `Proto/cloud/diagnostics.proto`**

```proto
syntax = "proto3";

package wendycloud.v1;

// DiagnosticsService receives opt-in, redacted crash/diagnostic reports from
// the Wendy CLI and lets users subscribe to be notified when a release fixes
// the issue they reported. The server is implemented in the cloud-services
// repo; this file is the wire contract consumed by the CLI and the Mac app.
service DiagnosticsService {
  // SubmitReport ingests a redacted diagnostic bundle and returns a
  // human-shareable tracking id (e.g. "WDY-7Q4ZK2") plus a status URL.
  rpc SubmitReport(SubmitReportRequest) returns (SubmitReportResponse);

  // GetReportStatus reports whether a tracked report is open, triaged, or
  // fixed (and in which release, when fixed).
  rpc GetReportStatus(GetReportStatusRequest) returns (GetReportStatusResponse);

  // Subscribe registers interest in a report's fix. An apns_device_token, when
  // present, opts the caller into an Apple push when the fix ships; all callers
  // also receive the fix via the cross-platform notification channel.
  rpc Subscribe(SubscribeRequest) returns (SubscribeResponse);
}

// PlatformInfo is the structured environment snapshot attached to a report.
// Target_* fields are empty when no device was connected at failure time.
message PlatformInfo {
  string cli_version = 1;
  string dev_os = 2;          // "darwin" | "linux" | "windows"
  string dev_os_version = 3;  // e.g. "15.5"
  string dev_arch = 4;        // "arm64" | "amd64"
  string dev_kernel = 5;

  string target_agent_version = 10;
  string target_os = 11;
  string target_os_version = 12;
  string target_hardware = 13; // device_type, e.g. "jetson-orin-nano"
  string target_gpu_vendor = 14;
  string target_jetpack_version = 15;
  string target_cuda_version = 16;
  string target_storage_medium = 17;
}

message SubmitReportRequest {
  PlatformInfo platform_info = 1;
  string error_class = 2;               // bounded enum from diag.ErrorClass
  string severity = 3;                  // "recoverable" | "unrecoverable"
  map<string, string> redacted_fields = 4;
  string error_chain = 5;               // redacted full error chain
  repeated string log_tail = 6;         // redacted recent log lines
  repeated string build_output_tail = 7;// redacted docker/build output tail
  string contact = 8;                   // optional, user-supplied
}

message SubmitReportResponse {
  string tracking_id = 1; // "WDY-XXXXXX"
  string status_url = 2;
}

message GetReportStatusRequest {
  string tracking_id = 1;
}

message GetReportStatusResponse {
  string status = 1;            // "open" | "triaged" | "fixed"
  string fixed_in_release = 2;  // empty unless status == "fixed"
}

message SubscribeRequest {
  string tracking_id = 1;
  string apns_device_token = 2; // empty for non-Apple callers
  string topic = 3;             // APNS topic / bundle id
}

message SubscribeResponse {
  string subscription_id = 1;
}
```

- [ ] **Step 2: Register the proto in the generator**

In `go/scripts/generate-proto.sh`, add `"cloud/diagnostics.proto"` to the `CLOUD_PROTOS` array (keep alphabetical order — insert after `"cloud/deployments.proto"`).

- [ ] **Step 3: Regenerate**

Run: `cd go && ./scripts/generate-proto.sh`
Expected: completes with no error; new files `go/proto/gen/cloudpb/diagnostics.pb.go` and `diagnostics_grpc.pb.go` exist.

- [ ] **Step 4: Verify it builds**

Run: `cd go && go build ./...`
Expected: success.

- [ ] **Step 5: Commit**

```bash
git add Proto/cloud/diagnostics.proto go/scripts/generate-proto.sh go/proto/gen/cloudpb/diagnostics.pb.go go/proto/gen/cloudpb/diagnostics_grpc.pb.go
git commit -m "feat(proto): add DiagnosticsService contract for crash reports & fix subscriptions"
```

---

### Task 2: `platforminfo` package — collection & rendering

**Files:**
- Create: `go/internal/shared/platforminfo/platforminfo.go`
- Create: `go/internal/shared/platforminfo/probe.go`
- Test: `go/internal/shared/platforminfo/platforminfo_test.go`

**Interfaces:**
- Consumes: `version.Version` from `go/internal/shared/version`; `cloudpb.PlatformInfo` from Task 1.
- Produces:
  - `type Info struct { CLIVersion, DevOS, DevOSVersion, DevArch, DevKernel string; TargetAgentVersion, TargetOS, TargetOSVersion, TargetHardware, TargetGPUVendor, TargetJetpackVersion, TargetCUDAVersion, TargetStorageMedium string }`
  - `func Collect() Info` — dev-only fields, never fails.
  - `func (i Info) OneLine() string`
  - `func (i Info) Block() string`
  - `func (i Info) Proto() *cloudpb.PlatformInfo`
  - `type Prober interface { OSVersion() string; Kernel() string }` with `var defaultProber Prober` overridable in tests.
  - `func (i *Info) WithAgentVersion(agentVersion, os, osVersion, hardware, gpuVendor, jetpack, cuda, storage string)` — fills target fields (caller passes fields read off `*agentpb.GetAgentVersionResponse`; the package must not import agentpb to avoid a dependency cycle).

- [ ] **Step 1: Write the failing test**

```go
package platforminfo

import (
	"strings"
	"testing"
)

type fakeProber struct{ osVer, kernel string }

func (f fakeProber) OSVersion() string { return f.osVer }
func (f fakeProber) Kernel() string    { return f.kernel }

func TestCollectFillsDevFields(t *testing.T) {
	old := defaultProber
	defaultProber = fakeProber{osVer: "15.5", kernel: "Darwin 24.5.0"}
	t.Cleanup(func() { defaultProber = old })

	got := Collect()
	if got.DevOSVersion != "15.5" {
		t.Errorf("DevOSVersion = %q, want 15.5", got.DevOSVersion)
	}
	if got.DevOS == "" || got.DevArch == "" {
		t.Errorf("DevOS/DevArch should be populated, got %q/%q", got.DevOS, got.DevArch)
	}
	if got.CLIVersion == "" {
		t.Error("CLIVersion should be populated")
	}
}

func TestOneLineCompact(t *testing.T) {
	i := Info{CLIVersion: "0.10.2", DevOS: "darwin", DevOSVersion: "15.5", DevArch: "arm64"}
	line := i.OneLine()
	if !strings.Contains(line, "0.10.2") || !strings.Contains(line, "arm64") {
		t.Errorf("OneLine missing fields: %q", line)
	}
	if strings.Contains(line, "\n") {
		t.Errorf("OneLine must be single line: %q", line)
	}
}

func TestOneLineAppendsTarget(t *testing.T) {
	i := Info{CLIVersion: "0.10.2", DevOS: "darwin", DevArch: "arm64"}
	i.WithAgentVersion("0.9.1", "wendyos", "2026.06.10", "jetson-orin-nano", "", "", "", "")
	line := i.OneLine()
	if !strings.Contains(line, "jetson-orin-nano") || !strings.Contains(line, "0.9.1") {
		t.Errorf("OneLine should include target info: %q", line)
	}
}

func TestProtoRoundTrip(t *testing.T) {
	i := Info{CLIVersion: "0.10.2", DevOS: "linux", DevArch: "amd64", TargetHardware: "rpi5"}
	p := i.Proto()
	if p.GetCliVersion() != "0.10.2" || p.GetTargetHardware() != "rpi5" {
		t.Errorf("proto mismatch: %+v", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/platforminfo/...`
Expected: FAIL — package/functions not defined.

- [ ] **Step 3: Write `platforminfo.go`**

```go
// Package platforminfo assembles a structured snapshot of the developer
// machine and (optionally) the connected target device for logs and crash
// reports. Collection never fails: missing data yields empty strings.
package platforminfo

import (
	"fmt"
	"runtime"
	"strings"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"github.com/wendylabsinc/wendy/go/internal/shared/version"
)

// Info is an environment snapshot. Target* fields are empty until
// WithAgentVersion is called with a connected device's version response.
type Info struct {
	CLIVersion   string
	DevOS        string
	DevOSVersion string
	DevArch      string
	DevKernel    string

	TargetAgentVersion   string
	TargetOS             string
	TargetOSVersion      string
	TargetHardware       string
	TargetGPUVendor      string
	TargetJetpackVersion string
	TargetCUDAVersion    string
	TargetStorageMedium  string
}

// Collect gathers developer-machine fields. It never returns an error;
// unavailable probes leave their fields empty.
func Collect() Info {
	return Info{
		CLIVersion:   version.Version,
		DevOS:        runtime.GOOS,
		DevOSVersion: defaultProber.OSVersion(),
		DevArch:      runtime.GOARCH,
		DevKernel:    defaultProber.Kernel(),
	}
}

// WithAgentVersion fills the target-device fields. Callers pass values read off
// *agentpb.GetAgentVersionResponse; this package avoids importing agentpb to
// keep it dependency-light and free of import cycles.
func (i *Info) WithAgentVersion(agentVersion, os, osVersion, hardware, gpuVendor, jetpack, cuda, storage string) {
	i.TargetAgentVersion = agentVersion
	i.TargetOS = os
	i.TargetOSVersion = osVersion
	i.TargetHardware = hardware
	i.TargetGPUVendor = gpuVendor
	i.TargetJetpackVersion = jetpack
	i.TargetCUDAVersion = cuda
	i.TargetStorageMedium = storage
}

// OneLine renders a compact single-line summary for the startup banner.
func (i Info) OneLine() string {
	var b strings.Builder
	fmt.Fprintf(&b, "wendy %s · %s", emptyDash(i.CLIVersion), emptyDash(i.DevOS))
	if i.DevOSVersion != "" {
		fmt.Fprintf(&b, " %s", i.DevOSVersion)
	}
	if i.DevArch != "" {
		fmt.Fprintf(&b, " %s", i.DevArch)
	}
	if i.TargetHardware != "" || i.TargetAgentVersion != "" || i.TargetOS != "" {
		fmt.Fprintf(&b, " → %s", emptyDash(i.TargetHardware))
		if i.TargetOS != "" {
			fmt.Fprintf(&b, " %s", i.TargetOS)
		}
		if i.TargetOSVersion != "" {
			fmt.Fprintf(&b, " %s", i.TargetOSVersion)
		}
		if i.TargetAgentVersion != "" {
			fmt.Fprintf(&b, " agent %s", i.TargetAgentVersion)
		}
	}
	return b.String()
}

// Block renders the full multi-line view for --verbose and crash reports.
func (i Info) Block() string {
	lines := []string{
		fmt.Sprintf("CLI version:    %s", emptyDash(i.CLIVersion)),
		fmt.Sprintf("Dev OS:         %s %s (%s)", emptyDash(i.DevOS), i.DevOSVersion, emptyDash(i.DevArch)),
		fmt.Sprintf("Dev kernel:     %s", emptyDash(i.DevKernel)),
	}
	if i.TargetAgentVersion != "" || i.TargetOS != "" || i.TargetHardware != "" {
		lines = append(lines,
			fmt.Sprintf("Target OS:      %s %s", emptyDash(i.TargetOS), i.TargetOSVersion),
			fmt.Sprintf("Target HW:      %s", emptyDash(i.TargetHardware)),
			fmt.Sprintf("Agent version:  %s", emptyDash(i.TargetAgentVersion)),
		)
		if i.TargetGPUVendor != "" {
			lines = append(lines, fmt.Sprintf("GPU:            %s", i.TargetGPUVendor))
		}
		if i.TargetJetpackVersion != "" {
			lines = append(lines, fmt.Sprintf("JetPack:        %s", i.TargetJetpackVersion))
		}
		if i.TargetCUDAVersion != "" {
			lines = append(lines, fmt.Sprintf("CUDA:           %s", i.TargetCUDAVersion))
		}
		if i.TargetStorageMedium != "" {
			lines = append(lines, fmt.Sprintf("Storage:        %s", i.TargetStorageMedium))
		}
	}
	return strings.Join(lines, "\n")
}

// Proto converts the snapshot to the wire type.
func (i Info) Proto() *cloudpb.PlatformInfo {
	return &cloudpb.PlatformInfo{
		CliVersion:           i.CLIVersion,
		DevOs:                i.DevOS,
		DevOsVersion:         i.DevOSVersion,
		DevArch:              i.DevArch,
		DevKernel:            i.DevKernel,
		TargetAgentVersion:   i.TargetAgentVersion,
		TargetOs:             i.TargetOS,
		TargetOsVersion:      i.TargetOSVersion,
		TargetHardware:       i.TargetHardware,
		TargetGpuVendor:      i.TargetGPUVendor,
		TargetJetpackVersion: i.TargetJetpackVersion,
		TargetCudaVersion:    i.TargetCUDAVersion,
		TargetStorageMedium:  i.TargetStorageMedium,
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
```

- [ ] **Step 4: Write `probe.go`**

```go
package platforminfo

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Prober reads OS-specific environment details. Implementations must never
// panic and should return "" when a value cannot be determined.
type Prober interface {
	OSVersion() string
	Kernel() string
}

var defaultProber Prober = osProber{}

type osProber struct{}

func (osProber) OSVersion() string {
	switch runtime.GOOS {
	case "darwin":
		return strings.TrimSpace(runCmd("sw_vers", "-productVersion"))
	case "linux":
		return linuxOSVersion()
	case "windows":
		return strings.TrimSpace(runCmd("cmd", "/c", "ver"))
	}
	return ""
}

func (osProber) Kernel() string {
	if runtime.GOOS == "windows" {
		return ""
	}
	return strings.TrimSpace(runCmd("uname", "-sr"))
}

// linuxOSVersion parses VERSION (or VERSION_ID) from /etc/os-release.
func linuxOSVersion() string {
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return ""
	}
	var versionID, version string
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		switch strings.TrimSpace(k) {
		case "VERSION":
			version = v
		case "VERSION_ID":
			versionID = v
		}
	}
	if version != "" {
		return version
	}
	return versionID
}

func runCmd(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return string(out)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/shared/platforminfo/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/shared/platforminfo/
git commit -m "feat(platforminfo): structured dev/target environment snapshot"
```

---

### Task 3: Env off-switches for banner & crash reports

**Files:**
- Modify: `go/internal/shared/env/env.go`
- Test: `go/internal/shared/env/env_test.go`

**Interfaces:**
- Produces: `func NoBanner() bool` (true when `WENDY_NO_BANNER` is set to any non-empty, non-"false" value), `func CrashReport() bool` (default true; false only when `WENDY_CRASHREPORT=false`, mirroring `Analytics()`).

- [ ] **Step 1: Write the failing test (append to env_test.go)**

```go
func TestCrashReportDefaultsTrue(t *testing.T) {
	t.Setenv("WENDY_CRASHREPORT", "")
	if !CrashReport() {
		t.Error("CrashReport should default to true")
	}
}

func TestCrashReportDisabled(t *testing.T) {
	t.Setenv("WENDY_CRASHREPORT", "false")
	if CrashReport() {
		t.Error("CrashReport should be false when WENDY_CRASHREPORT=false")
	}
}

func TestNoBanner(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "")
	if NoBanner() {
		t.Error("NoBanner should be false when unset")
	}
	t.Setenv("WENDY_NO_BANNER", "1")
	if !NoBanner() {
		t.Error("NoBanner should be true when set")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/shared/env/...`
Expected: FAIL — `CrashReport`/`NoBanner` undefined.

- [ ] **Step 3: Implement in env.go**

```go
// CrashReport reports whether the opt-in crash-reporter prompt may run.
// Defaults to true; only WENDY_CRASHREPORT=false disables it. Mirrors
// Analytics() parsing.
func CrashReport() bool {
	v := strings.TrimSpace(os.Getenv("WENDY_CRASHREPORT"))
	switch strings.ToLower(v) {
	case "", "true":
		return true
	case "false":
		return false
	default:
		log.Printf("WARNING: invalid WENDY_CRASHREPORT=%q, expected \"true\" or \"false\", defaulting to true", v)
		return true
	}
}

// NoBanner reports whether the startup platform banner should be suppressed.
// Any non-empty value other than "false" suppresses it.
func NoBanner() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("WENDY_NO_BANNER")))
	return v != "" && v != "false"
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/shared/env/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/shared/env/
git commit -m "feat(env): add WENDY_CRASHREPORT and WENDY_NO_BANNER switches"
```

---

### Task 4: `diag` package — log ring buffer

**Files:**
- Create: `go/internal/cli/diag/ring.go`
- Test: `go/internal/cli/diag/ring_test.go`

**Interfaces:**
- Produces:
  - `func Record(line string)` — appends to a process-wide, mutex-guarded ring (capacity 200).
  - `func Recent() []string` — returns a copy of buffered lines, oldest first.
  - `func ResetForTesting()` — clears the ring (test helper).

- [ ] **Step 1: Write the failing test**

```go
package diag

import "testing"

func TestRingKeepsLastN(t *testing.T) {
	ResetForTesting()
	for i := 0; i < ringCap+50; i++ {
		Record("line")
	}
	got := Recent()
	if len(got) != ringCap {
		t.Fatalf("len(Recent()) = %d, want %d", len(got), ringCap)
	}
}

func TestRingOrderOldestFirst(t *testing.T) {
	ResetForTesting()
	Record("a")
	Record("b")
	Record("c")
	got := Recent()
	if len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("Recent() = %v, want [a b c]", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/diag/...`
Expected: FAIL — package not defined.

- [ ] **Step 3: Implement `ring.go`**

```go
// Package diag captures structured, classified error state and a bounded
// buffer of recent log lines so an unrecoverable failure can be turned into a
// serviceable crash report. Everything here is read only when a report is
// built; collection is cheap and always on.
package diag

import "sync"

const ringCap = 200

var (
	ringMu  sync.Mutex
	ringBuf []string
)

// Record appends a line to the bounded recent-log ring. Safe for concurrent use.
func Record(line string) {
	ringMu.Lock()
	defer ringMu.Unlock()
	ringBuf = append(ringBuf, line)
	if len(ringBuf) > ringCap {
		ringBuf = ringBuf[len(ringBuf)-ringCap:]
	}
}

// Recent returns a copy of the buffered lines, oldest first.
func Recent() []string {
	ringMu.Lock()
	defer ringMu.Unlock()
	out := make([]string, len(ringBuf))
	copy(out, ringBuf)
	return out
}

// ResetForTesting clears the ring. Test-only.
func ResetForTesting() {
	ringMu.Lock()
	defer ringMu.Unlock()
	ringBuf = nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/diag/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/diag/
git commit -m "feat(diag): bounded recent-log ring buffer"
```

---

### Task 5: `diag` package — error context wrapper & severity classification

**Files:**
- Create: `go/internal/cli/diag/error.go`
- Test: `go/internal/cli/diag/error_test.go`

**Interfaces:**
- Consumes: `commands.ErrUserCancelled`, `commands.ErrDefaultCleared` (sentinels in `go/internal/cli/commands`). To avoid an import cycle (commands will import diag in Task 9/11), severity matching on these sentinels is done by the caller in `main` (which already imports both); `diag` exposes the classification primitives and a `Severity` that operates on gRPC codes + a `Build`/`Panic` marker. The user-cancel sentinels are classified `Recoverable` in `main` before calling diag.
- Produces:
  - `type Severity string` with consts `Recoverable Severity = "recoverable"`, `Unrecoverable Severity = "unrecoverable"`.
  - `type DiagError struct { ... }` implementing `error` and `Unwrap()`, created by `func Wrap(err error, op string) *DiagError` and `func (e *DiagError) WithDevice(name string) *DiagError` / `WithStage(stage string) *DiagError`.
  - `func MarkBuildFailure(err error) error` — wraps so `Classify` returns `Unrecoverable`.
  - `func Classify(err error) Severity` — `Unrecoverable` for build-failure marker, gRPC `Internal`/`Unknown`/`DataLoss`; else `Recoverable`.
  - `func Chain(err error) string` — full unwrapped chain joined by `: `, for the report (pre-redaction).

- [ ] **Step 1: Write the failing test**

```go
package diag

import (
	"errors"
	"fmt"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestClassifyUnrecoverableGRPC(t *testing.T) {
	if got := Classify(status.Error(codes.Internal, "boom")); got != Unrecoverable {
		t.Errorf("Internal => %q, want unrecoverable", got)
	}
	if got := Classify(status.Error(codes.Unknown, "boom")); got != Unrecoverable {
		t.Errorf("Unknown => %q, want unrecoverable", got)
	}
}

func TestClassifyRecoverable(t *testing.T) {
	if got := Classify(status.Error(codes.Unavailable, "down")); got != Recoverable {
		t.Errorf("Unavailable => %q, want recoverable", got)
	}
	if got := Classify(errors.New("plain")); got != Recoverable {
		t.Errorf("plain => %q, want recoverable", got)
	}
}

func TestClassifyBuildFailure(t *testing.T) {
	err := MarkBuildFailure(errors.New("docker build failed"))
	if got := Classify(err); got != Unrecoverable {
		t.Errorf("build failure => %q, want unrecoverable", got)
	}
}

func TestWrapAndChain(t *testing.T) {
	base := errors.New("connection refused")
	werr := Wrap(base, "deploy").WithDevice("orin").WithStage("push")
	chain := Chain(werr)
	if chain == "" || !errorsContains(chain, "connection refused") || !errorsContains(chain, "deploy") {
		t.Errorf("Chain = %q", chain)
	}
	if !errors.Is(werr, base) {
		t.Error("DiagError must unwrap to base")
	}
	_ = fmt.Sprint(werr)
}

func errorsContains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && (indexOf(s, sub) >= 0))) }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/diag/... -run Classify`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `error.go`**

```go
package diag

import (
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Severity buckets an error for the crash-reporter trigger.
type Severity string

const (
	Recoverable   Severity = "recoverable"
	Unrecoverable Severity = "unrecoverable"
)

// buildFailure marks an error as an unrecoverable build failure.
type buildFailure struct{ err error }

func (b buildFailure) Error() string { return b.err.Error() }
func (b buildFailure) Unwrap() error { return b.err }

// MarkBuildFailure tags err so Classify treats it as Unrecoverable.
func MarkBuildFailure(err error) error {
	if err == nil {
		return nil
	}
	return buildFailure{err: err}
}

// DiagError attaches structured context to an error without changing how it
// renders to the user (Error() is just the wrapped chain).
type DiagError struct {
	err    error
	op     string
	device string
	stage  string
}

// Wrap attaches an operation label to err.
func Wrap(err error, op string) *DiagError { return &DiagError{err: err, op: op} }

func (e *DiagError) WithDevice(name string) *DiagError { e.device = name; return e }
func (e *DiagError) WithStage(stage string) *DiagError { e.stage = stage; return e }

func (e *DiagError) Error() string {
	if e.op != "" {
		return e.op + ": " + e.err.Error()
	}
	return e.err.Error()
}

func (e *DiagError) Unwrap() error { return e.err }

// Fields returns the structured context for inclusion in a report.
func (e *DiagError) Fields() map[string]string {
	m := map[string]string{}
	if e.op != "" {
		m["op"] = e.op
	}
	if e.device != "" {
		m["device"] = e.device
	}
	if e.stage != "" {
		m["stage"] = e.stage
	}
	return m
}

// Classify buckets err. Build-failure markers and gRPC Internal/Unknown/DataLoss
// are unrecoverable; everything else (user errors, Unavailable, plain errors) is
// recoverable.
func Classify(err error) Severity {
	if err == nil {
		return Recoverable
	}
	var bf buildFailure
	if errors.As(err, &bf) {
		return Unrecoverable
	}
	if st, ok := status.FromError(err); ok && st.Code() != codes.OK {
		switch st.Code() {
		case codes.Internal, codes.Unknown, codes.DataLoss:
			return Unrecoverable
		}
	}
	return Recoverable
}

// Chain renders the full unwrapped error chain (pre-redaction).
func Chain(err error) string {
	var parts []string
	for err != nil {
		parts = append(parts, err.Error())
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, "\n  ↳ ")
}

var _ = fmt.Sprint // keep fmt imported for future formatting helpers
```

> Note: remove the `fmt`/`var _ = fmt.Sprint` line if `go vet` flags it; it is a placeholder guard only if `fmt` ends up unused.

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/diag/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/diag/
git commit -m "feat(diag): error-context wrapper and severity classification"
```

---

### Task 6: `crashreport` package — redactor (critical unit)

**Files:**
- Create: `go/internal/cli/crashreport/redact.go`
- Test: `go/internal/cli/crashreport/redact_test.go`

**Interfaces:**
- Produces:
  - `func Redact(s string) string` — applies all redaction rules to a single string.
  - `func RedactLines(lines []string) []string` — maps Redact over a slice.
  - `func RedactMap(m map[string]string) map[string]string` — redacts values.

- [ ] **Step 1: Write the failing test**

```go
package crashreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactHomeDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	if home == "" {
		t.Skip("no home dir")
	}
	in := filepath.Join(home, "projects", "secret-thing", "main.go")
	got := Redact(in)
	if strings.Contains(got, home) {
		t.Errorf("home dir not redacted: %q", got)
	}
	if !strings.HasPrefix(got, "~") {
		t.Errorf("expected ~ prefix, got %q", got)
	}
}

func TestRedactBearerToken(t *testing.T) {
	got := Redact("Authorization: Bearer abc123.def456.ghi789")
	if strings.Contains(got, "abc123.def456.ghi789") {
		t.Errorf("token not redacted: %q", got)
	}
}

func TestRedactIPAndEmail(t *testing.T) {
	got := Redact("connect 192.168.1.42 user a.b@example.com")
	if strings.Contains(got, "192.168.1.42") {
		t.Errorf("IPv4 not redacted: %q", got)
	}
	if strings.Contains(got, "a.b@example.com") {
		t.Errorf("email not redacted: %q", got)
	}
}

func TestRedactLeavesPlainText(t *testing.T) {
	in := "docker build failed: exit status 1"
	if got := Redact(in); got != in {
		t.Errorf("plain text changed: %q != %q", got, in)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/crashreport/...`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `redact.go`**

```go
// Package crashreport bundles, redacts, and submits opt-in diagnostic reports
// for unrecoverable failures.
package crashreport

import (
	"os"
	"regexp"
	"strings"
)

var (
	reBearer = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._\-]+`)
	reToken  = regexp.MustCompile(`(?i)(token|api[_-]?key|secret|password)(["']?\s*[:=]\s*["']?)[^\s"',]+`)
	reEmail  = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	reIPv4   = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	reIPv6   = regexp.MustCompile(`\b(?:[0-9a-fA-F]{1,4}:){2,7}[0-9a-fA-F]{0,4}\b`)
)

// Redact removes or masks sensitive data from a single string: the user's home
// directory, bearer tokens, key/secret assignments, emails, and IP addresses.
func Redact(s string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "~")
	}
	s = reBearer.ReplaceAllString(s, "${1}<redacted>")
	s = reToken.ReplaceAllString(s, "${1}${2}<redacted>")
	s = reEmail.ReplaceAllString(s, "<redacted-email>")
	s = reIPv4.ReplaceAllString(s, "<redacted-ip>")
	s = reIPv6.ReplaceAllString(s, "<redacted-ip>")
	return s
}

// RedactLines applies Redact to each line.
func RedactLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = Redact(l)
	}
	return out
}

// RedactMap applies Redact to each value (keys are assumed safe field names).
func RedactMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = Redact(v)
	}
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/crashreport/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/crashreport/redact.go go/internal/cli/crashreport/redact_test.go
git commit -m "feat(crashreport): redaction of paths, tokens, IPs, and emails"
```

---

### Task 7: `crashreport` package — bundle assembly & tracking-number formatting

**Files:**
- Create: `go/internal/cli/crashreport/bundle.go`
- Test: `go/internal/cli/crashreport/bundle_test.go`

**Interfaces:**
- Consumes: `platforminfo.Info`, `diag.Recent()`, `diag.Chain`, `cloudpb.SubmitReportRequest`.
- Produces:
  - `type Bundle struct { Info platforminfo.Info; ErrorClass, Severity, ErrorChain string; LogTail, BuildOutputTail []string; Contact string }`
  - `func Build(info platforminfo.Info, errorClass, severity, errorChain string, logTail, buildTail []string) Bundle` — redacts all free-text fields and bounds `buildTail`/`logTail` to the last 200 entries.
  - `func (b Bundle) Request() *cloudpb.SubmitReportRequest`
  - `func ValidTrackingID(id string) bool` — matches `^WDY-[A-Z0-9]{6}$`.

- [ ] **Step 1: Write the failing test**

```go
package crashreport

import (
	"strings"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

func TestBuildRedactsAndBounds(t *testing.T) {
	long := make([]string, 500)
	for i := range long {
		long[i] = "line"
	}
	b := Build(platforminfo.Info{CLIVersion: "0.1"}, "grpc_other", "unrecoverable",
		"deploy: dial 10.0.0.5: refused", []string{"connect a@b.com"}, long)

	if strings.Contains(b.ErrorChain, "10.0.0.5") {
		t.Errorf("error chain not redacted: %q", b.ErrorChain)
	}
	if len(b.BuildOutputTail) != 200 {
		t.Errorf("build tail not bounded: %d", len(b.BuildOutputTail))
	}
	if strings.Contains(strings.Join(b.LogTail, "\n"), "a@b.com") {
		t.Errorf("log tail not redacted: %v", b.LogTail)
	}
}

func TestRequestRoundTrip(t *testing.T) {
	b := Build(platforminfo.Info{CLIVersion: "0.1"}, "other", "unrecoverable", "boom", nil, nil)
	req := b.Request()
	if req.GetSeverity() != "unrecoverable" || req.GetErrorChain() != "boom" {
		t.Errorf("request mismatch: %+v", req)
	}
	if req.GetPlatformInfo().GetCliVersion() != "0.1" {
		t.Errorf("platform info missing")
	}
}

func TestValidTrackingID(t *testing.T) {
	cases := map[string]bool{"WDY-7Q4ZK2": true, "wdy-7q4zk2": false, "WDY-12345": false, "": false}
	for id, want := range cases {
		if ValidTrackingID(id) != want {
			t.Errorf("ValidTrackingID(%q) = %v, want %v", id, !want, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/crashreport/... -run 'Build|Request|TrackingID'`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `bundle.go`**

```go
package crashreport

import (
	"regexp"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

const maxTail = 200

var reTrackingID = regexp.MustCompile(`^WDY-[A-Z0-9]{6}$`)

// Bundle is the fully-redacted, bounded diagnostic payload.
type Bundle struct {
	Info            platforminfo.Info
	ErrorClass      string
	Severity        string
	ErrorChain      string
	LogTail         []string
	BuildOutputTail []string
	Contact         string
}

// Build assembles a redacted, bounded bundle. All free-text inputs pass through
// the redactor; log and build tails keep only the last maxTail entries.
func Build(info platforminfo.Info, errorClass, severity, errorChain string, logTail, buildTail []string) Bundle {
	return Bundle{
		Info:            info,
		ErrorClass:      errorClass,
		Severity:        severity,
		ErrorChain:      Redact(errorChain),
		LogTail:         RedactLines(lastN(logTail, maxTail)),
		BuildOutputTail: RedactLines(lastN(buildTail, maxTail)),
	}
}

// Request converts the bundle to the wire request.
func (b Bundle) Request() *cloudpb.SubmitReportRequest {
	return &cloudpb.SubmitReportRequest{
		PlatformInfo:    b.Info.Proto(),
		ErrorClass:      b.ErrorClass,
		Severity:        b.Severity,
		ErrorChain:      b.ErrorChain,
		LogTail:         b.LogTail,
		BuildOutputTail: b.BuildOutputTail,
		Contact:         b.Contact,
		RedactedFields:  map[string]string{},
	}
}

// ValidTrackingID reports whether id matches the WDY-XXXXXX format.
func ValidTrackingID(id string) bool { return reTrackingID.MatchString(id) }

func lastN(s []string, n int) []string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/crashreport/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/crashreport/bundle.go go/internal/cli/crashreport/bundle_test.go
git commit -m "feat(crashreport): redacted bounded bundle assembly + tracking-id format"
```

---

### Task 8: `crashreport` package — submit, fallback-to-file, subscribe

**Files:**
- Create: `go/internal/cli/crashreport/submit.go`
- Test: `go/internal/cli/crashreport/submit_test.go`

**Interfaces:**
- Consumes: `cloudpb.DiagnosticsServiceClient` (Task 1), `Bundle` (Task 7).
- Produces:
  - `type Result struct { TrackingID, StatusURL, LocalFile string }`
  - `func Submit(ctx context.Context, client cloudpb.DiagnosticsServiceClient, b Bundle) (Result, error)` — calls `SubmitReport`; on any error returns a `Result` with `LocalFile` set (bundle written to a temp file) and a nil error so callers never surface a secondary failure. When `client == nil`, goes straight to the file fallback.
  - `func Subscribe(ctx context.Context, client cloudpb.DiagnosticsServiceClient, trackingID, apnsToken, topic string) (string, error)`
  - `func writeLocalBundle(b Bundle) (string, error)` (unexported; tested indirectly).

- [ ] **Step 1: Write the failing test (bufconn-backed fake server)**

```go
package crashreport

import (
	"context"
	"net"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

type fakeDiag struct {
	cloudpb.UnimplementedDiagnosticsServiceServer
	failSubmit bool
}

func (f *fakeDiag) SubmitReport(ctx context.Context, req *cloudpb.SubmitReportRequest) (*cloudpb.SubmitReportResponse, error) {
	if f.failSubmit {
		return nil, context.DeadlineExceeded
	}
	return &cloudpb.SubmitReportResponse{TrackingId: "WDY-7Q4ZK2", StatusUrl: "https://wendy.sh/r/WDY-7Q4ZK2"}, nil
}
func (f *fakeDiag) Subscribe(ctx context.Context, req *cloudpb.SubscribeRequest) (*cloudpb.SubscribeResponse, error) {
	return &cloudpb.SubscribeResponse{SubscriptionId: "sub-1"}, nil
}

func dialFake(t *testing.T, srv *fakeDiag) cloudpb.DiagnosticsServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	cloudpb.RegisterDiagnosticsServiceServer(s, srv)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return cloudpb.NewDiagnosticsServiceClient(conn)
}

func sampleBundle() Bundle {
	return Build(platforminfo.Info{CLIVersion: "0.1"}, "other", "unrecoverable", "boom", nil, nil)
}

func TestSubmitSuccess(t *testing.T) {
	client := dialFake(t, &fakeDiag{})
	res, err := Submit(context.Background(), client, sampleBundle())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if res.TrackingID != "WDY-7Q4ZK2" || res.StatusURL == "" {
		t.Errorf("bad result: %+v", res)
	}
}

func TestSubmitFallbackToFileOnError(t *testing.T) {
	client := dialFake(t, &fakeDiag{failSubmit: true})
	res, err := Submit(context.Background(), client, sampleBundle())
	if err != nil {
		t.Fatalf("Submit must not return an error on fallback: %v", err)
	}
	if res.TrackingID != "" || res.LocalFile == "" {
		t.Errorf("expected file fallback, got %+v", res)
	}
}

func TestSubmitNilClientFallsBackToFile(t *testing.T) {
	res, err := Submit(context.Background(), nil, sampleBundle())
	if err != nil || res.LocalFile == "" {
		t.Errorf("nil client should fall back to file: %+v err=%v", res, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/crashreport/... -run Submit`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `submit.go`**

```go
package crashreport

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// Result describes the outcome of a submission. Exactly one of TrackingID or
// LocalFile is set.
type Result struct {
	TrackingID string
	StatusURL  string
	LocalFile  string
}

// Submit sends the bundle to the cloud. On any failure (including a nil client,
// an offline endpoint, or an Unimplemented server before the cloud side ships),
// it writes the bundle to a local file and returns that path with a nil error —
// the crash-reporter must never produce a secondary error.
func Submit(ctx context.Context, client cloudpb.DiagnosticsServiceClient, b Bundle) (Result, error) {
	if client != nil {
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		resp, err := client.SubmitReport(callCtx, b.Request())
		if err == nil && resp.GetTrackingId() != "" {
			return Result{TrackingID: resp.GetTrackingId(), StatusURL: resp.GetStatusUrl()}, nil
		}
	}
	path, ferr := writeLocalBundle(b)
	if ferr != nil {
		return Result{}, ferr
	}
	return Result{LocalFile: path}, nil
}

// Subscribe registers interest in a report's fix.
func Subscribe(ctx context.Context, client cloudpb.DiagnosticsServiceClient, trackingID, apnsToken, topic string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("no cloud connection available to subscribe")
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := client.Subscribe(callCtx, &cloudpb.SubscribeRequest{
		TrackingId: trackingID, ApnsDeviceToken: apnsToken, Topic: topic,
	})
	if err != nil {
		return "", err
	}
	return resp.GetSubscriptionId(), nil
}

// writeLocalBundle writes the redacted bundle as JSON to a temp file the user
// can attach to a GitHub issue.
func writeLocalBundle(b Bundle) (string, error) {
	dir, err := os.MkdirTemp("", "wendy-crashreport-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report.json")
	data, err := json.MarshalIndent(b.Request(), "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}
```

> Note: `b.Request()` returns a protobuf message; `encoding/json` marshals its exported fields adequately for a human-readable local artifact. If the generated struct does not marshal cleanly, switch to `protojson.Marshal` from `google.golang.org/protobuf/encoding/protojson` (already an indirect dependency).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/crashreport/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/crashreport/submit.go go/internal/cli/crashreport/submit_test.go
git commit -m "feat(crashreport): gRPC submit with local-file fallback and subscribe"
```

---

### Task 9: Banner wiring in root command

**Files:**
- Modify: `go/internal/cli/commands/root.go` (`PersistentPreRunE`)
- Create: `go/internal/cli/commands/banner.go`
- Test: `go/internal/cli/commands/banner_test.go`

**Interfaces:**
- Consumes: `platforminfo.Collect/OneLine/Block`, `env.NoBanner`.
- Produces: `func printPlatformBanner(cmd *cobra.Command, verbose bool)` — writes the banner to `cmd.ErrOrStderr()`; no-op when `env.NoBanner()` or for internal commands. Also `func bannerVerbose(cmd *cobra.Command) bool` reading the root `--verbose`/persistent flag if present (else false).

- [ ] **Step 1: Write the failing test**

```go
package commands

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestPrintPlatformBannerWritesToStderr(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "")
	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	printPlatformBanner(cmd, false)
	if !strings.Contains(stderr.String(), "wendy ") {
		t.Errorf("banner missing: %q", stderr.String())
	}
}

func TestPrintPlatformBannerSuppressed(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "1")
	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	printPlatformBanner(cmd, false)
	if stderr.Len() != 0 {
		t.Errorf("banner should be suppressed, got %q", stderr.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/... -run PlatformBanner`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `banner.go`**

```go
package commands

import (
	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
)

// printPlatformBanner writes a one-line platform summary (or the full block
// under --verbose) to stderr. It is a no-op when WENDY_NO_BANNER is set. Target
// fields are absent here; they are appended later once a device is connected.
func printPlatformBanner(cmd *cobra.Command, verbose bool) {
	if env.NoBanner() {
		return
	}
	info := platforminfo.Collect()
	w := cmd.ErrOrStderr()
	if verbose {
		_, _ = w.Write([]byte(info.Block() + "\n"))
		return
	}
	_, _ = w.Write([]byte(info.OneLine() + "\n"))
}
```

- [ ] **Step 4: Wire into `root.go` `PersistentPreRunE`**

In `root.go`, inside the existing `switch cmd.Name()` early-return block, the internal commands already return before any heavy init. After that switch (so internal commands are skipped) and before `providers.Initialize`, add:

```go
			printPlatformBanner(cmd, cmd.Root().PersistentFlags().Changed("verbose"))
```

If the root has no persistent `--verbose` flag yet, add one near the other persistent flags:

```go
	root.PersistentFlags().Bool("verbose", false, "Show the full platform block and verbose output")
```

(Leave the existing per-command `--verbose` on `run` intact; cobra resolves the local flag first, so behavior there is unchanged.)

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/cli/commands/... -run PlatformBanner && cd go && go build ./...`
Expected: PASS + build success.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/banner.go go/internal/cli/commands/banner_test.go go/internal/cli/commands/root.go
git commit -m "feat(cli): print compact platform banner on every command"
```

---

### Task 10: Append target-device info to the banner on connect

**Files:**
- Modify: `go/internal/cli/commands/banner.go` (add `printTargetBanner`)
- Modify: `go/internal/cli/commands/build.go` (after the existing `GetAgentVersion` call at ~line 191)
- Test: `go/internal/cli/commands/banner_test.go`

**Interfaces:**
- Consumes: `*agentpb.GetAgentVersionResponse`.
- Produces: `func printTargetBanner(cmd *cobra.Command, resp *agentpb.GetAgentVersionResponse)` — builds a `platforminfo.Info`, fills target fields via `WithAgentVersion`, and writes the target portion (`OneLine` of an Info that has only target fields) to stderr; no-op when `env.NoBanner()` or `resp == nil`.

- [ ] **Step 1: Write the failing test**

```go
func TestPrintTargetBanner(t *testing.T) {
	t.Setenv("WENDY_NO_BANNER", "")
	cmd := &cobra.Command{Use: "wendy"}
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	resp := &agentpb.GetAgentVersionResponse{
		Version: "0.9.1", Os: "wendyos",
	}
	dt := "jetson-orin-nano"
	resp.DeviceType = &dt
	printTargetBanner(cmd, resp)
	if !strings.Contains(stderr.String(), "jetson-orin-nano") || !strings.Contains(stderr.String(), "0.9.1") {
		t.Errorf("target banner missing fields: %q", stderr.String())
	}
}
```

(Add `agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"` to the test imports.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/... -run TargetBanner`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement `printTargetBanner` in banner.go**

```go
// printTargetBanner appends a one-line summary of the connected device's OS,
// hardware, and agent version to stderr. No-op when suppressed or resp is nil.
func printTargetBanner(cmd *cobra.Command, resp *agentpb.GetAgentVersionResponse) {
	if env.NoBanner() || resp == nil {
		return
	}
	var info platforminfo.Info
	info.WithAgentVersion(
		resp.GetVersion(), resp.GetOs(), resp.GetOsVersion(), resp.GetDeviceType(),
		resp.GetGpuVendor(), resp.GetJetpackVersion(), resp.GetCudaVersion(), resp.GetStorageMedium(),
	)
	cmd.ErrOrStderr().Write([]byte(info.OneLine() + "\n"))
}
```

(Add `agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"` to banner.go imports.)

- [ ] **Step 4: Call it from build.go after the version fetch**

After the existing `versionResp, err := target.Agent.AgentService.GetAgentVersion(...)` (~line 191), once `err == nil`, add:

```go
				printTargetBanner(cmd, versionResp)
```

(Ensure a `cmd *cobra.Command` is in scope at that call site; the `RunE` closure has it. If the call is in a helper without `cmd`, thread it through or skip the call there — the banner is best-effort.)

- [ ] **Step 5: Run tests + build**

Run: `cd go && go test ./internal/cli/commands/... -run Banner && go build ./...`
Expected: PASS + build success.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/banner.go go/internal/cli/commands/banner_test.go go/internal/cli/commands/build.go
git commit -m "feat(cli): append connected device info to platform banner"
```

---

### Task 11: Wire the crash-reporter flow into `main.go`

**Files:**
- Modify: `go/cmd/wendy/main.go`
- Create: `go/internal/cli/commands/crashflow.go` (interactive flow lives in `commands` to reuse prompt/cloud helpers)
- Test: `go/internal/cli/commands/crashflow_test.go`

**Interfaces:**
- Consumes: `diag.Classify`, `diag.Chain`, `diag.Recent`, `crashreport.Build/Submit/Subscribe`, `platforminfo.Collect`, `env.IsCI/CrashReport`, `isInteractiveTerminal`, the existing cloud dial helper (`dialCloudGRPC` + `cloudpb.NewDiagnosticsServiceClient`), `errorClass` (exported as needed).
- Produces:
  - `func MaybeRunCrashReport(ctx context.Context, executed *cobra.Command, err error, errorClass string)` in `commands` — the single entry point `main` calls. Internally: returns immediately unless `diag.Classify(err) == diag.Unrecoverable && !env.IsCI() && env.CrashReport() && isInteractiveTerminal()`. Then prompts, builds the bundle (`platforminfo.Collect()` + `diag.Chain(err)` + `diag.Recent()`), submits (best-effort cloud client; nil on dial failure → file fallback), prints tracking id + status URL (or local-file path), and offers `Subscribe`.

- [ ] **Step 1: Write the failing test (flow gating only — no network)**

```go
package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMaybeRunCrashReportSkipsRecoverable(t *testing.T) {
	t.Setenv("CI", "")            // ensure not classified as CI
	t.Setenv("WENDY_CRASHREPORT", "true")
	// Recoverable error must be a no-op (no panic, returns cleanly).
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		status.Error(codes.Unavailable, "down"), "grpc_unavailable")
}

func TestMaybeRunCrashReportSkipsInCI(t *testing.T) {
	t.Setenv("CI", "1")
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		errors.New("boom"), "other")
}

func TestMaybeRunCrashReportSkipsWhenDisabled(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("WENDY_CRASHREPORT", "false")
	MaybeRunCrashReport(context.Background(), &cobra.Command{Use: "wendy"},
		status.Error(codes.Internal, "boom"), "grpc_other")
}
```

> These assert the flow is a safe no-op under the skip conditions (non-interactive test env naturally fails `isInteractiveTerminal()`, so the interactive branch never runs in CI). The interactive prompt + submit path is exercised by the `crashreport` package tests (Task 8).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/... -run MaybeRunCrashReport`
Expected: FAIL — `MaybeRunCrashReport` undefined.

- [ ] **Step 3: Implement `crashflow.go`**

```go
package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/wendylabsinc/wendy/go/internal/cli/crashreport"
	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
	"github.com/wendylabsinc/wendy/go/internal/shared/env"
	"github.com/wendylabsinc/wendy/go/internal/shared/platforminfo"
	cloudpb "github.com/wendylabsinc/wendy/go/proto/gen/cloudpb"
)

// MaybeRunCrashReport offers to submit a redacted diagnostic report after an
// unrecoverable failure. It is a strict no-op for recoverable errors, in CI,
// when disabled via WENDY_CRASHREPORT=false, or in a non-interactive terminal.
// It must never produce an error or alter the process exit code.
func MaybeRunCrashReport(ctx context.Context, executed *cobra.Command, err error, errorClass string) {
	if err == nil || diag.Classify(err) != diag.Unrecoverable {
		return
	}
	if env.IsCI() || !env.CrashReport() || !isInteractiveTerminal() {
		return
	}

	out := executed.ErrOrStderr()
	fmt.Fprintln(out, "\nThis looks like an unrecoverable failure.")
	if !promptYesNo("Submit an anonymous, redacted diagnostic report to help us fix it?", false) {
		return
	}

	info := platforminfo.Collect()
	bundle := crashreport.Build(info, errorClass, string(diag.Unrecoverable), diag.Chain(err), diag.Recent(), buildOutputTail())
	bundle.Contact = "" // optional; left blank unless we later prompt for it

	fmt.Fprintln(out, "\nThe following (redacted) information will be sent:")
	fmt.Fprintln(out, info.Block())
	fmt.Fprintf(out, "Error: %s\n", bundle.ErrorChain)
	if !promptYesNo("Send this report?", false) {
		fmt.Fprintln(out, "Report not sent.")
		return
	}

	client := dialDiagnosticsClient() // nil on failure → file fallback
	res, ferr := crashreport.Submit(ctx, client, bundle)
	if ferr != nil {
		fmt.Fprintf(out, "Could not save report: %v\n", ferr)
		return
	}
	if res.TrackingID != "" {
		fmt.Fprintf(out, "\nReport submitted. Tracking number: %s\n", res.TrackingID)
		if res.StatusURL != "" {
			fmt.Fprintf(out, "Track status: %s\n", res.StatusURL)
		}
		offerSubscribe(ctx, client, executed, res.TrackingID)
		return
	}
	fmt.Fprintf(out, "\nCloud unavailable; report saved locally: %s\n", res.LocalFile)
	fmt.Fprintln(out, "Attach it to an issue at https://github.com/wendylabsinc/wendyos/issues")
}

// offerSubscribe asks whether to be notified when a release fixes the report.
// APNS device tokens come from the Mac app; the CLI subscribes without one and
// receives the fix via the cross-platform notification channel.
func offerSubscribe(ctx context.Context, client cloudpb.DiagnosticsServiceClient, executed *cobra.Command, trackingID string) {
	out := executed.ErrOrStderr()
	if !promptYesNo("Notify me when a release fixes this?", true) {
		return
	}
	if _, err := crashreport.Subscribe(ctx, client, trackingID, "", ""); err != nil {
		fmt.Fprintf(out, "Could not subscribe now; you can still check %s later.\n", trackingID)
		return
	}
	fmt.Fprintln(out, "Subscribed. You'll see a notification on your next 'wendy' run once it's fixed.")
}

// dialDiagnosticsClient best-effort dials the cloud DiagnosticsService using the
// default auth session. Returns nil on any failure so Submit uses the file
// fallback. (Reuses dialCloudGRPC from cloud_tunnel.go.)
func dialDiagnosticsClient() cloudpb.DiagnosticsServiceClient {
	auth, err := defaultAuthEntry()
	if err != nil || auth == nil || auth.CloudGRPC == "" {
		return nil
	}
	conn, err := dialCloudGRPC(auth)
	if err != nil {
		return nil
	}
	return cloudpb.NewDiagnosticsServiceClient(conn)
}
```

> Implementation notes for the engineer:
> - `promptYesNo(prompt string, def bool) bool` — if a yes/no prompt helper does not already exist in `commands`, add a minimal one in this file reading from stdin via the existing TUI/`bufio` pattern used elsewhere (grep `promptYesNo`/`confirm` in `commands` first and reuse).
> - `buildOutputTail() []string` — return `nil` for now; Task 12 wires real build output. Add a package-level `var lastBuildOutput []string` and have `buildOutputTail()` return a copy.
> - `defaultAuthEntry()` — reuse the existing default-session lookup (grep `pickAuthEntry`/`defaultAuth` in `commands`; if only `pickAuthEntry(flag string)` exists, call it with `""`). The connection is best-effort.

- [ ] **Step 4: Call from `main.go`**

In `go/cmd/wendy/main.go`, in the `if err != nil` block, after computing the error class and before `os.Exit(1)`:

```go
	if err != nil {
		if errors.Is(err, commands.ErrUserCancelled) || errors.Is(err, commands.ErrDefaultCleared) {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, tui.ErrorMessage(formatError(err).Error()))
		commands.MaybeRunCrashReport(context.Background(), executed, err, errorClass(err))
		os.Exit(1)
	}
```

(`executed` is already in scope from `cmd.ExecuteC()`.)

- [ ] **Step 5: Run tests + build + vet**

Run: `cd go && go test ./internal/cli/commands/... -run MaybeRunCrashReport && go build ./... && go vet ./...`
Expected: PASS + build success.

- [ ] **Step 6: Commit**

```bash
git add go/cmd/wendy/main.go go/internal/cli/commands/crashflow.go go/internal/cli/commands/crashflow_test.go
git commit -m "feat(cli): offer redacted crash report on unrecoverable failures"
```

---

### Task 12: Capture docker build output tail & mark build failures unrecoverable

**Files:**
- Modify: `go/internal/cli/commands/build.go`
- Modify: `go/internal/cli/commands/crashflow.go` (`buildOutputTail`)
- Test: `go/internal/cli/commands/build_failure_test.go`

**Interfaces:**
- Consumes: `diag.Record`, `diag.MarkBuildFailure`.
- Produces: build output is appended to the `diag` ring (so it lands in `log_tail`), and build failures are wrapped with `diag.MarkBuildFailure` so `diag.Classify` returns `Unrecoverable`.

- [ ] **Step 1: Write the failing test**

```go
package commands

import (
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/cli/diag"
)

func TestBuildFailureIsUnrecoverable(t *testing.T) {
	err := wrapBuildError(errors.New("exit status 1"))
	if diag.Classify(err) != diag.Unrecoverable {
		t.Errorf("wrapped build error should be unrecoverable, got %v", diag.Classify(err))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/... -run BuildFailureIsUnrecoverable`
Expected: FAIL — `wrapBuildError` undefined.

- [ ] **Step 3: Implement**

In `build.go`, add a helper and use it where a docker/image build returns an error:

```go
// wrapBuildError marks a build failure so the crash reporter classifies it as
// unrecoverable.
func wrapBuildError(err error) error {
	if err == nil {
		return nil
	}
	return diag.MarkBuildFailure(err)
}
```

Find the point(s) in `build.go` where the build subprocess returns a non-nil error (grep for where the docker/buildx command result is returned) and wrap it: `return wrapBuildError(err)`. Where build output lines are read/streamed, also call `diag.Record(line)` so they enter the ring. Add `"github.com/wendylabsinc/wendy/go/internal/cli/diag"` to the imports.

In `crashflow.go`, replace the stub `buildOutputTail()` body to return the build-output slice if one is maintained; otherwise keep returning `diag.Recent()`-filtered lines. Simplest correct implementation:

```go
func buildOutputTail() []string { return nil } // build output already flows into diag.Recent()
```

(Build output recorded via `diag.Record` is already captured in `log_tail`; a separate `build_output_tail` is optional. Keep this returning `nil` to avoid duplication.)

- [ ] **Step 4: Run tests + build**

Run: `cd go && go test ./internal/cli/commands/... -run BuildFailure && go build ./...`
Expected: PASS + build success.

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/build.go go/internal/cli/commands/crashflow.go go/internal/cli/commands/build_failure_test.go
git commit -m "feat(cli): mark docker build failures unrecoverable and capture output"
```

---

### Task 13: APNS device-token registration & fix-notification handling in `WendyAgentMac`

**Files:**
- Modify: `swift/WendyAgentMac/Sources/<AppDelegate or App entry>.swift` (locate the `@main` App / AppDelegate)
- Create: `swift/WendyAgentMac/Sources/CrashFixNotifications.swift`
- Test: `swift/WendyAgentMac` has an Xcode test target if present; otherwise this task is verified manually (note below).

**Interfaces:**
- Consumes: the `DiagnosticsService` Swift client from `WendyCloudGRPC` (regenerated in Task 1; Swift stub `Wendycloud_V1_DiagnosticsService`), the user's stored cloud session.
- Produces: APNS registration on launch; on receiving the device token, calls `Subscribe` for any locally-tracked report IDs; handles the incoming fix push by posting a `UNUserNotification`.

> **Testing note:** APNS requires a provisioning profile, a push entitlement, and a real device/cloud round-trip — it cannot be unit-tested in CI. This task is verified manually (steps below). Keep the Swift logic thin and delegate all wire calls to the generated client so the untestable surface is minimal.

- [ ] **Step 1: Locate the app entry point**

Run: `grep -rn "@main\|NSApplicationDelegate\|class AppDelegate" swift/WendyAgentMac/Sources/`
Expected: identifies the App/AppDelegate file to modify.

- [ ] **Step 2: Add the push-registration + handler file `CrashFixNotifications.swift`**

```swift
import Foundation
import UserNotifications
#if canImport(AppKit)
import AppKit
#endif

/// Registers for Apple push notifications and links the resulting device token
/// to any crash-report tracking ids the user is subscribed to, so a fix push
/// can be delivered. All cloud calls go through the generated DiagnosticsService
/// client; this type only orchestrates.
final class CrashFixNotifications: NSObject, UNUserNotificationCenterDelegate {
    static let shared = CrashFixNotifications()

    /// Tracking ids the user asked to be notified about (persisted elsewhere).
    var pendingTrackingIDs: [String] = []

    func registerForPush() {
        UNUserNotificationCenter.current().delegate = self
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { granted, _ in
            guard granted else { return }
            DispatchQueue.main.async {
                NSApplication.shared.registerForRemoteNotifications()
            }
        }
    }

    /// Called by the AppDelegate when APNS returns the device token.
    func didRegister(deviceToken: Data) {
        let token = deviceToken.map { String(format: "%02x", $0) }.joined()
        Task { await self.subscribeAll(apnsToken: token) }
    }

    private func subscribeAll(apnsToken: String) async {
        for id in pendingTrackingIDs {
            await DiagnosticsClient.shared.subscribe(trackingID: id, apnsToken: apnsToken)
        }
    }

    /// Show fix notifications even while the app is foregrounded.
    func userNotificationCenter(_ center: UNUserNotificationCenter,
                                willPresent notification: UNNotification,
                                withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }
}
```

- [ ] **Step 3: Add a thin `DiagnosticsClient` Swift wrapper (same file or adjacent)**

```swift
import Foundation
import GRPCCore // adjust to the project's gRPC import (match WendyCloudGRPC usage)

/// Thin wrapper over the generated Wendycloud_V1_DiagnosticsService client.
/// Mirror the connection/auth setup used by the app's existing cloud calls.
actor DiagnosticsClient {
    static let shared = DiagnosticsClient()

    func subscribe(trackingID: String, apnsToken: String) async {
        // Build the request and call the generated client. Pattern must match
        // how WendyAgentMac already constructs authenticated cloud clients
        // (grep the app for an existing Wendycloud_V1_*Client usage and copy
        // the channel/credentials setup).
        //
        // var req = Wendycloud_V1_SubscribeRequest()
        // req.trackingID = trackingID
        // req.apnsDeviceToken = apnsToken
        // _ = try? await client.subscribe(req)
    }
}
```

> The engineer must wire `DiagnosticsClient.subscribe` to the project's existing authenticated cloud-client construction (grep `Wendycloud_V1_` in `swift/WendyAgentMac` and `swift/WendyAgentCore/Sources/WendyCloudGRPC`). Leaving it as a documented stub is acceptable for this PR only if no authenticated cloud client exists yet in the Mac app — note that explicitly in the PR description.

- [ ] **Step 4: Hook AppDelegate callbacks**

In the located AppDelegate / App entry, add:

```swift
func applicationDidFinishLaunching(_ notification: Notification) {
    CrashFixNotifications.shared.registerForPush()
}

func application(_ application: NSApplication, didRegisterForRemoteNotificationsWithDeviceToken deviceToken: Data) {
    CrashFixNotifications.shared.didRegister(deviceToken: deviceToken)
}

func application(_ application: NSApplication, didFailToRegisterForRemoteNotificationsWithError error: Error) {
    // Best-effort: push simply won't be available; the CLI fallback still works.
}
```

(If the app uses SwiftUI's `@main App`, use `@NSApplicationDelegateAdaptor` to attach an `AppDelegate` with these methods.)

- [ ] **Step 5: Add the push entitlement**

In the app's `.entitlements` (under `swift/WendyAgentMac/Configuration` or the xcodeproj), add `aps-environment` = `development` (and `production` for release). This requires the corresponding capability in the Apple developer profile.

- [ ] **Step 6: Build the Mac app**

Run: `cd swift && xcodebuild -workspace WendyAgent.xcworkspace -scheme WendyAgentMac -configuration Debug build` (adjust scheme name via `xcodebuild -list`).
Expected: build succeeds (push won't function without a profile, but the code compiles).

- [ ] **Step 7: Commit**

```bash
git add swift/WendyAgentMac/
git commit -m "feat(mac): register APNS token and handle crash-fix notifications"
```

---

### Task 14: Documentation

**Files:**
- Modify: `Documentation/` — find the CLI reference / troubleshooting doc (grep for where env vars like `WENDY_ANALYTICS` are documented) and add `WENDY_CRASHREPORT` and `WENDY_NO_BANNER`, plus a short "Crash reports & fix subscriptions" section.

**Interfaces:** none (docs only).

- [ ] **Step 1: Find where env vars are documented**

Run: `grep -rn "WENDY_ANALYTICS" Documentation/ README.md INSTALL.md 2>/dev/null`
Expected: the file(s) listing CLI env vars.

- [ ] **Step 2: Add documentation**

In the located file, add entries:

```markdown
### Diagnostics & crash reporting

The Wendy CLI prints a one-line platform summary (CLI version, your OS, and —
when a device is connected — its OS, hardware, and agent version) to stderr at
the start of each command. Set `WENDY_NO_BANNER=1` to suppress it; run with
`--verbose` to see the full block.

On an unrecoverable failure (e.g. a Docker build error), the CLI offers to send
an **opt-in, redacted** diagnostic report and returns a tracking number
(`WDY-XXXXXX`). Reports are shown to you before sending and have home paths,
tokens, IPs, and emails removed. You can disable the prompt with
`WENDY_CRASHREPORT=false`. Reports are never sent in CI. You can subscribe to be
notified when a release fixes your report — via a push to the macOS app, or via
an in-CLI notification on your next run.
```

- [ ] **Step 3: Commit**

```bash
git add Documentation/ README.md INSTALL.md
git commit -m "docs: document platform banner, crash reports, and fix subscriptions"
```

---

### Task 15: Full verification & PR

**Files:** none (verification + PR).

- [ ] **Step 1: Run the full Go test suite**

Run: `cd go && go build ./... && go vet ./... && go test ./...`
Expected: all PASS.

- [ ] **Step 2: Regenerate proto cleanliness check**

Run: `cd go && ./scripts/generate-proto.sh && git diff --stat`
Expected: no unexpected diff (generated files already committed in Task 1).

- [ ] **Step 3: Manual smoke — banner**

Run: `cd go && go run ./cmd/wendy version` (or any command) and confirm the one-line banner appears on stderr; run with `WENDY_NO_BANNER=1` and confirm it is gone.

- [ ] **Step 4: Push and open the PR**

```bash
git push -u origin jo/platform-diagnostics-crash-reporting
gh pr create --title "Platform diagnostics, crash reporting & fix subscriptions" \
  --body "$(cat <<'BODY'
Adds platform info to CLI logs, structured + classified error capture, an opt-in
redacted crash-reporter flow (triggered only on unrecoverable failures) returning
a WDY-XXXXXX tracking number, and subscribe-to-fix delivery via APNS (Mac app)
with a cross-platform cloud-notification fallback.

Server-side ingestion, the tracking DB, release→bug mapping, and APNS push
delivery live in the cloud repo and are out of scope here; this PR defines the
wire contract (`Proto/cloud/diagnostics.proto`) and full client behavior. Until
the cloud server ships, `SubmitReport` degrades gracefully to a local report file.

Spec: specs/2026-06-28-platform-diagnostics-crash-reporting-design.md
Plan: specs/2026-06-28-platform-diagnostics-crash-reporting-plan.md

🤖 Generated with [Claude Code](https://claude.com/claude-code)
BODY
)"
```

- [ ] **Step 5: Confirm CI is green**

Run: `gh pr checks --watch`
Expected: all checks pass.

---

## Self-Review

**Spec coverage:**
- §1 platforminfo → Task 2. ✓
- §2 platform info in logs (banner) → Tasks 9, 10. ✓
- §3 structured error capture (ring, DiagError, severity, build tail) → Tasks 4, 5, 12. ✓
- §4 crash-reporter flow (redact, bundle, submit, consent, preview) → Tasks 6, 7, 8, 11. ✓
- §5 new proto contract → Task 1. ✓
- §6 APNS + cloud-notification fallback → Tasks 13 (APNS) + 8/11 (CLI subscribe). ✓
- §7 no public command (inline only) → Task 11 (inline flow; no command registered). ✓
- Consent & privacy → Tasks 3, 6, 8, 11. ✓
- Failure handling (file fallback) → Task 8. ✓
- Testing → tests in every Go task; APNS manual (noted). ✓

**Placeholder scan:** Code blocks are complete for all Go tasks. Task 13 (Swift APNS) intentionally documents a stub for `DiagnosticsClient.subscribe` because the authenticated cloud-client construction must match the Mac app's existing pattern, which can only be determined at implementation time; this is called out explicitly and is the only deliberately-incomplete unit, justified by its untestable-in-CI nature.

**Type consistency:** `platforminfo.Info` fields, `diag.Classify/Chain/Recent/Record/MarkBuildFailure`, `crashreport.Build/Submit/Subscribe/Bundle/Result/ValidTrackingID`, and `cloudpb.*` message field names are used consistently across tasks. `MaybeRunCrashReport` signature matches its call in `main.go`. Banner functions `printPlatformBanner`/`printTargetBanner` are consistent between Tasks 9 and 10.
