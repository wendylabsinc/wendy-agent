# Remote Build Host — Implementation Plan (v1, Dockerfile path)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `wendy run --build-host <device>` builds the container image on another
WendyOS host and has that host push the finished image straight into the target
device's registry over the mesh, so neither the build nor the image bytes touch the
developer's machine.

**Architecture:** A new `WendyBuildService` on the agent with two RPCs —
`GetBuildCapabilities` (preflight) and `BuildImage` (streaming build). The CLI ships the
build context through the *existing* generic `QueryChunks`/`WriteChunks` sha256 blob
store, the agent reassembles it into a stable per-app directory and runs `buildctl`, and
the resulting image is exported with `push=true` through an agent-run loopback proxy that
adds the machine/CI client certificate and forwards to the target's registry over the
mesh. The CLI then issues the existing, unmodified `CreateContainer` registry deploy.

**Tech Stack:** Go 1.26, gRPC + protobuf (`scripts/generate-proto.sh`), containerd,
buildkitd (`unix:///run/buildkit/buildkitd.sock` on device), Cobra CLI, Bubble Tea TUI.

**Spec:** `specs/2026-08-08-remote-build-host-design.md`. Read it first — this plan
implements it and does not restate its reasoning.

## Scope

This plan covers the **Dockerfile / Containerfile / generated-Python-Dockerfile** path
end to end. The Stagefile path compiles to LLB and depends on PR #1606, which is not
merged; its tasks are in **Appendix A** and must not be started until #1606 lands. Task 2
provisions the `oneof` that Appendix A slots into, so no protocol change is needed later.

## Global Constraints

- **Branch:** `jo/remote-build-host`, worktree `~/git/wendy/wendyos-remote-build`, based
  on **`jo/fast`**, not `main`. `main` lacks `loadDockerIgnoreForBuild`,
  `prepareDockerBuildFile`, `compileStagefile`, and the whole `go/internal/stagefile`
  package — the Stagefile integration is unmerged. Building on `main` would mean
  reimplementing the ignore-precedence helper that already exists, guaranteeing a
  conflict. Retarget to `main` once the stagefile stack lands.
- **Pre-resolved lookups** (the three the plan flagged; verified before execution):
  - `chunk.Ref` is `{Hash [32]byte; Offset uint64; Len uint64}` — Task 6 must use
    `r.Len`, not `r.Length`, and `Offset` is `uint64`.
  - `isImageBuildFailure` matches `*imageBuildFailedError{err error}`
    (`ocilayers.go:697`). Same package, so Task 10 constructs it directly as
    `&imageBuildFailedError{err: ...}` — no new constructor.
  - The agent chunk store is `*containerd.Client` (`StageChunk`, `MissingChunks`). It has
    **no ordered-read path**: `AssembleLayerFromChunks` writes a content-store blob, which
    a build context is not. Task 9 therefore adds a small exported reader over the
    existing internal `chunkStream` (`internal/agent/containerd/chunkstore.go:116`), which
    already resolves and hash-verifies chunks in order.
- **No behaviour change when `--build-host` is absent.** Every existing local path —
  chunk-diff fast deploy, registry push, the `--detach` no-build fast path — must be
  byte-for-byte unaffected. A task that changes a local-path test is wrong.
- **The remote path must not require a local container builder.** No `ensureDockerDaemon`,
  `ensureBuildxBuilder`, `ensureAppleContainerSystemForBuilder`, or `solve.Address` on the
  `--build-host` path. This is the `neo → spark → robot` requirement from the spec.
- **Never trust the client.** Every validation the CLI performs must also run on the agent.
- **No silent fallback to a local build.** Every remote failure is an error naming the
  build host.
- Test command: `go test ./go/internal/cli/... ./go/internal/agent/... ./go/internal/shared/...`
- Proto regeneration: `cd go && make proto` (wraps `scripts/generate-proto.sh`).
- Module prefix: `github.com/wendylabsinc/wendy/go`.
- Proto v2 conventions: `package wendy.agent.services.v2;` with
  `option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2";`.

---

### Task 1: `--build-host` flag, persisted default, and `--builder` conflict

**Files:**
- Modify: `go/internal/shared/config/config.go` (add `DefaultBuildHost`)
- Create: `go/internal/cli/commands/buildhost.go`
- Create: `go/internal/cli/commands/buildhost_test.go`
- Modify: `go/internal/cli/commands/run.go:538-566` (flag registration on `newRunCmd`)
- Modify: `go/internal/cli/commands/build.go:214-217` (flag registration on `newBuildCmd`)

**Interfaces:**
- Consumes: `config.Load`, `config.Config`.
- Produces: `resolveBuildHostName(flagValue string) (string, error)` — returns the
  effective build host ("" when none), flag winning over config.
  `errBuilderWithBuildHost` — the sentinel returned when both flags are set.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/commands/buildhost_test.go
package commands

import (
	"errors"
	"testing"
)

func TestResolveBuildHostName_FlagBeatsConfig(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "spark-office", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("neo-lab")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "neo-lab" {
		t.Fatalf("got %q, want the flag value %q", got, "neo-lab")
	}
}

func TestResolveBuildHostName_FallsBackToConfig(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "spark-office", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "spark-office" {
		t.Fatalf("got %q, want the config default", got)
	}
}

func TestResolveBuildHostName_EmptyWhenUnset(t *testing.T) {
	loadBuildHostDefault = func() (string, error) { return "", nil }
	t.Cleanup(func() { loadBuildHostDefault = configBuildHostDefault })

	got, err := resolveBuildHostName("")
	if err != nil {
		t.Fatalf("resolveBuildHostName: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty so the local path stays untouched", got)
	}
}

func TestValidateBuildHostFlags_RejectsBuilderCombo(t *testing.T) {
	err := validateBuildHostFlags("spark-office", "docker")
	if !errors.Is(err, errBuilderWithBuildHost) {
		t.Fatalf("got %v, want errBuilderWithBuildHost", err)
	}
}

func TestValidateBuildHostFlags_AllowsEitherAlone(t *testing.T) {
	if err := validateBuildHostFlags("spark-office", ""); err != nil {
		t.Fatalf("build host alone must be allowed: %v", err)
	}
	if err := validateBuildHostFlags("", "docker"); err != nil {
		t.Fatalf("builder alone must be allowed: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run TestResolveBuildHostName -v`
Expected: FAIL — `undefined: resolveBuildHostName`.

- [ ] **Step 3: Add the config field**

In `go/internal/shared/config/config.go`, inside `type Config struct`, after
`DefaultDevice`:

```go
	// DefaultBuildHost is the device name `wendy run`/`wendy build` delegate the
	// image build to when --build-host is not passed. Per-developer, not
	// committed: the right build host depends on which network the developer is
	// on, not on the project.
	DefaultBuildHost string `json:"defaultBuildHost,omitempty"`
```

- [ ] **Step 4: Write the implementation**

```go
// go/internal/cli/commands/buildhost.go
package commands

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/config"
)

// errBuilderWithBuildHost is returned when --builder and --build-host are both
// given. --builder selects the LOCAL image builder, which the remote path never
// runs, so honouring one silently would mean ignoring the other.
var errBuilderWithBuildHost = errors.New("--builder selects a local image builder and cannot be combined with --build-host; drop one")

// loadBuildHostDefault is the seam tests replace, in the style docker.go's
// imageBuilderLookPath already establishes. Nothing here calls os.Setenv.
var loadBuildHostDefault = configBuildHostDefault

func configBuildHostDefault() (string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	return strings.TrimSpace(cfg.DefaultBuildHost), nil
}

// resolveBuildHostName returns the device that should build, most explicit
// signal first: the --build-host flag, then the persisted per-developer
// default. An empty result means "build locally" and must leave every existing
// local path untouched.
func resolveBuildHostName(flagValue string) (string, error) {
	if v := strings.TrimSpace(flagValue); v != "" {
		return v, nil
	}
	return loadBuildHostDefault()
}

// validateBuildHostFlags rejects flag combinations that cannot both be honoured.
func validateBuildHostFlags(buildHost, builder string) error {
	if strings.TrimSpace(buildHost) != "" && strings.TrimSpace(builder) != "" {
		return errBuilderWithBuildHost
	}
	return nil
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./go/internal/cli/commands/ -run 'TestResolveBuildHostName|TestValidateBuildHostFlags' -v`
Expected: PASS (5 tests).

- [ ] **Step 6: Register the flags**

In `go/internal/cli/commands/run.go`, add `buildHost string` to `runOptions`, and
alongside the existing `--builder` registration in `newRunCmd`:

```go
	cmd.Flags().StringVar(&opts.buildHost, "build-host", "", "WendyOS device to build the image on instead of this machine (e.g. a DGX Spark); the built image is pushed straight to the target device")
```

In `go/internal/cli/commands/build.go`, add `buildHost string` to `buildOptions` and the
same flag registration in `newBuildCmd`.

In both `RunE` bodies, as the first validation:

```go
			if err := validateBuildHostFlags(opts.buildHost, opts.builder); err != nil {
				return err
			}
```

- [ ] **Step 7: Verify nothing else broke**

Run: `go test ./go/internal/cli/commands/`
Expected: PASS. No existing test should change — `--build-host` defaults to empty.

- [ ] **Step 8: Commit**

```bash
git add go/internal/shared/config/config.go go/internal/cli/commands/buildhost.go \
        go/internal/cli/commands/buildhost_test.go go/internal/cli/commands/run.go \
        go/internal/cli/commands/build.go
git commit -m "feat(cli): --build-host flag and persisted default"
```

---

### Task 2: `WendyBuildService` proto

**Files:**
- Create: `Proto/wendy/agent/services/v2/build_service.proto`
- Generated (do not hand-edit): `go/proto/gen/agentpb/v2/build_service*.go`

**Interfaces:**
- Produces: `agentpbv2.WendyBuildServiceClient` / `...Server`,
  `GetBuildCapabilitiesRequest/Response`, `BuildImageRequest/BuildImageProgress`,
  `BuildSpec`, `DockerfileBuild`, `ChunkManifest`. Tasks 3, 4, 6, 7, 8, 9 all consume
  these names.

- [ ] **Step 1: Write the proto**

```protobuf
// Proto/wendy/agent/services/v2/build_service.proto
syntax = "proto3";

package wendy.agent.services.v2;

option go_package = "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2;agentpbv2";

// WendyBuildService lets a CLI delegate a container image build to this device.
//
// BuildImage is remote code execution by design — that is the feature — so the
// device must be explicitly opted in as a builder (see GetBuildCapabilities'
// builder_enabled) and every field of a BuildSpec is validated here rather than
// trusted from the client.
service WendyBuildService {
  rpc GetBuildCapabilities(GetBuildCapabilitiesRequest) returns (GetBuildCapabilitiesResponse);
  rpc BuildImage(stream BuildImageRequest) returns (stream BuildImageProgress);
}

message GetBuildCapabilitiesRequest {}

message GetBuildCapabilitiesResponse {
  // False when buildkitd is absent — notably on a Mac agent, where containers
  // run under Apple Container with no BuildKit underneath.
  bool buildkit_available = 1;
  string buildkit_version = 2;
  string os = 3;
  string cpu_architecture = 4;
  // Platforms this host builds without emulation, e.g. "linux/arm64".
  repeated string native_platforms = 5;
  // Platforms reachable only through binfmt/QEMU. Building these works but is
  // slow enough that the CLI says so rather than letting it look like a hang.
  repeated string emulated_platforms = 6;
  // False unless the device is configured as a build host. Default off.
  bool builder_enabled = 7;
}

// ChunkManifest reconstructs a byte stream from chunks already written through
// WendyContainerService.WriteChunks. Order is significant.
message ChunkManifest {
  repeated bytes chunk_hashes = 1;  // raw 32-byte sha256 digests, in order
  int64 total_size = 2;             // expected reassembled size, for validation
}

// DockerfileBuild builds through BuildKit's dockerfile.v0 frontend.
message DockerfileBuild {
  // Filename within the context, e.g. "Dockerfile" or "Dockerfile.generated".
  // Must not escape the context root.
  string dockerfile = 1;
  map<string, string> build_args = 2;
}

message BuildSpec {
  // App identifier; also selects the stable per-app context directory whose path
  // BuildKit uses as its local-source cache key.
  string app_id = 1;
  // Target platform, e.g. "linux/arm64".
  string platform = 2;
  // Registry reference to push the finished image to, e.g.
  // "robot-01.acme.cloud.wendy.dev:5000/myapp:latest". Validated against the
  // mesh-address + registry-port form; never used verbatim as a destination.
  string push_reference = 3;
  // The build context as an uncompressed tar, addressed by chunk.
  ChunkManifest context = 4;

  oneof definition {
    DockerfileBuild dockerfile_build = 5;
    // LlbBuild llb_build = 6;  // reserved for the Stagefile path (PR #1606)
  }
  reserved 6;
}

message BuildImageRequest {
  // The first message on the stream carries the spec; subsequent messages are
  // reserved for future incremental input and are currently rejected.
  BuildSpec spec = 1;
}

message BuildImageProgress {
  oneof event {
    // One line of BuildKit plain-mode progress output, forwarded verbatim so
    // the CLI's existing renderer can consume it unchanged.
    string log_line = 1;
    BuildImageResult result = 2;
  }
}

message BuildImageResult {
  // Digest of the pushed image, e.g. "sha256:...".
  string image_digest = 1;
}
```

- [ ] **Step 2: Generate and verify it compiles**

Run: `cd go && make proto && go build ./...`
Expected: `go/proto/gen/agentpb/v2/build_service.pb.go` and `_grpc.pb.go` appear; build
succeeds.

- [ ] **Step 3: Commit**

```bash
git add Proto/wendy/agent/services/v2/build_service.proto go/proto/gen/agentpb/v2/
git commit -m "feat(proto): WendyBuildService for remote image builds"
```

---

### Task 3: Agent `GetBuildCapabilities` and the builder opt-in gate

**Files:**
- Create: `go/internal/agent/services/build_service.go`
- Create: `go/internal/agent/services/build_service_test.go`
- Modify: `go/cmd/wendy-agent/main.go:483-501` (service registration)

**Interfaces:**
- Consumes: `agentpbv2` from Task 2.
- Produces: `services.NewBuildService(logger *zap.Logger, opts BuildServiceOptions) *BuildService`
  and `services.BuildServiceOptions{Enabled bool; BuildkitAddress string}`. Tasks 7 and 8
  extend this same type.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/agent/services/build_service_test.go
package services

import (
	"context"
	"testing"

	"go.uber.org/zap"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestGetBuildCapabilities_ReportsDisabledByDefault(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if resp.GetBuilderEnabled() {
		t.Fatal("a device must not report itself as a builder unless explicitly opted in")
	}
}

func TestGetBuildCapabilities_ReportsNoBuildkitWhenSocketAbsent(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{
		Enabled:         true,
		BuildkitAddress: "unix:///nonexistent/buildkitd.sock",
	})

	resp, err := svc.GetBuildCapabilities(context.Background(), &agentpbv2.GetBuildCapabilitiesRequest{})
	if err != nil {
		t.Fatalf("GetBuildCapabilities: %v", err)
	}
	if resp.GetBuildkitAvailable() {
		t.Fatal("buildkit must not be reported available when its socket does not exist")
	}
}

func TestBuildImage_RejectedWhenNotEnabled(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{Enabled: false})

	err := svc.BuildImage(&stubBuildStream{
		spec: &agentpbv2.BuildSpec{AppID: "app", Platform: "linux/arm64"},
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("got %v, want FailedPrecondition when the device is not a builder", err)
	}
}
```

Add the stream stub in the same file:

```go
type stubBuildStream struct {
	agentpbv2.WendyBuildService_BuildImageServer
	spec *agentpbv2.BuildSpec
	sent []*agentpbv2.BuildImageProgress
	recvd bool
}

func (s *stubBuildStream) Context() context.Context { return context.Background() }

func (s *stubBuildStream) Recv() (*agentpbv2.BuildImageRequest, error) {
	if s.recvd {
		return nil, io.EOF
	}
	s.recvd = true
	return &agentpbv2.BuildImageRequest{Spec: s.spec}, nil
}

func (s *stubBuildStream) Send(p *agentpbv2.BuildImageProgress) error {
	s.sent = append(s.sent, p)
	return nil
}
```

(import `io` alongside the others.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestGetBuildCapabilities -v`
Expected: FAIL — `undefined: NewBuildService`.

- [ ] **Step 3: Write the implementation**

```go
// go/internal/agent/services/build_service.go
package services

import (
	"context"
	"os"
	"runtime"
	"strings"

	"go.uber.org/zap"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DefaultBuildkitAddress is where buildkitd listens on a WendyOS device — the
// same socket the on-device buildctl path uses.
const DefaultBuildkitAddress = "unix:///run/buildkit/buildkitd.sock"

// BuildServiceOptions configures the build service. Enabled defaults to false:
// BuildImage runs client-supplied build instructions, so a device becomes a
// build host only by explicit configuration, never by being reachable.
type BuildServiceOptions struct {
	Enabled         bool
	BuildkitAddress string
}

type BuildService struct {
	agentpbv2.UnimplementedWendyBuildServiceServer
	logger *zap.Logger
	opts   BuildServiceOptions
}

func NewBuildService(logger *zap.Logger, opts BuildServiceOptions) *BuildService {
	if opts.BuildkitAddress == "" {
		opts.BuildkitAddress = DefaultBuildkitAddress
	}
	return &BuildService{logger: logger, opts: opts}
}

func (s *BuildService) GetBuildCapabilities(_ context.Context, _ *agentpbv2.GetBuildCapabilitiesRequest) (*agentpbv2.GetBuildCapabilitiesResponse, error) {
	available := buildkitSocketPresent(s.opts.BuildkitAddress)
	resp := &agentpbv2.GetBuildCapabilitiesResponse{
		BuildkitAvailable: available,
		Os:                runtime.GOOS,
		CpuArchitecture:   runtime.GOARCH,
		BuilderEnabled:    s.opts.Enabled,
	}
	if available {
		resp.NativePlatforms = []string{runtime.GOOS + "/" + runtime.GOARCH}
	}
	return resp, nil
}

// buildkitSocketPresent reports whether a unix-socket buildkitd address points
// at something that exists. A non-unix address is taken at face value: only the
// device socket form is checkable without dialing.
func buildkitSocketPresent(addr string) bool {
	path, ok := strings.CutPrefix(addr, "unix://")
	if !ok {
		return addr != ""
	}
	_, err := os.Stat(path)
	return err == nil
}

func (s *BuildService) BuildImage(stream agentpbv2.WendyBuildService_BuildImageServer) error {
	if !s.opts.Enabled {
		return status.Error(codes.FailedPrecondition,
			"this device is not configured as a build host; enable the builder role in the agent configuration to allow remote builds")
	}
	return status.Error(codes.Unimplemented, "build execution lands in a later task")
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./go/internal/agent/services/ -run 'TestGetBuildCapabilities|TestBuildImage_Rejected' -v`
Expected: PASS (3 tests).

- [ ] **Step 5: Register the service**

In `go/cmd/wendy-agent/main.go`, beside the other `agentpbv2.Register...` calls
(around line 499):

```go
		buildSvc := services.NewBuildService(logger, services.BuildServiceOptions{
			Enabled: buildHostEnabled(),
		})
		agentpbv2.RegisterWendyBuildServiceServer(srv, buildSvc)
```

and add, near the other config helpers in that file:

```go
// buildHostEnabled reports whether this device may accept remote builds.
// Default off: BuildImage executes client-supplied build instructions, so the
// role is opt-in rather than a property of being reachable.
func buildHostEnabled() bool {
	return os.Getenv("WENDY_BUILD_HOST_ENABLED") == "1"
}
```

- [ ] **Step 6: Verify**

Run: `cd go && go build ./... && go test ./go/internal/agent/services/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/services/build_service.go \
        go/internal/agent/services/build_service_test.go go/cmd/wendy-agent/main.go
git commit -m "feat(agent): WendyBuildService capabilities and builder opt-in gate"
```

---

### Task 4: CLI capability preflight

**Files:**
- Modify: `go/internal/cli/commands/buildhost.go`
- Modify: `go/internal/cli/commands/buildhost_test.go`
- Modify: `go/internal/cli/grpcclient/client.go:71-80,330-345` (add `BuildService`)

**Interfaces:**
- Consumes: `agentpbv2.GetBuildCapabilitiesResponse` (Task 2).
- Produces: `checkBuildHostCapabilities(host string, resp *agentpbv2.GetBuildCapabilitiesResponse, platform string) error`.
  Task 9 calls it after connecting to the build host.

- [ ] **Step 1: Write the failing test**

```go
// append to go/internal/cli/commands/buildhost_test.go
func TestCheckBuildHostCapabilities_NoBuildkit(t *testing.T) {
	err := checkBuildHostCapabilities("neo-lab", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: false,
		Os:                "darwin",
	}, "linux/arm64")
	if err == nil {
		t.Fatal("a host without buildkit must be refused")
	}
	if !strings.Contains(err.Error(), "neo-lab") {
		t.Fatalf("error must name the host, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_NotOptedIn(t *testing.T) {
	err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    false,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
	}, "linux/arm64")
	if err == nil {
		t.Fatal("a host that has not opted in must be refused")
	}
	if !strings.Contains(err.Error(), "spark-office") {
		t.Fatalf("error must name the host, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_PlatformUnsupported(t *testing.T) {
	err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
	}, "linux/amd64")
	if err == nil {
		t.Fatal("a platform that is neither native nor emulated must be refused")
	}
	if !strings.Contains(err.Error(), "linux/amd64") {
		t.Fatalf("error must name the platform, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_EmulatedIsAllowed(t *testing.T) {
	if err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:     true,
		BuildkitAvailable:  true,
		NativePlatforms:    []string{"linux/arm64"},
		EmulatedPlatforms:  []string{"linux/amd64"},
	}, "linux/amd64"); err != nil {
		t.Fatalf("an emulated platform must be allowed, got: %v", err)
	}
}

func TestCheckBuildHostCapabilities_NativePasses(t *testing.T) {
	if err := checkBuildHostCapabilities("spark-office", &agentpbv2.GetBuildCapabilitiesResponse{
		BuilderEnabled:    true,
		BuildkitAvailable: true,
		NativePlatforms:   []string{"linux/arm64"},
	}, "linux/arm64"); err != nil {
		t.Fatalf("a native platform must pass, got: %v", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run TestCheckBuildHostCapabilities -v`
Expected: FAIL — `undefined: checkBuildHostCapabilities`.

- [ ] **Step 3: Write the implementation**

```go
// append to go/internal/cli/commands/buildhost.go

// checkBuildHostCapabilities refuses a build host before any context is
// transferred. Each failure names the host, and none of them falls back to a
// local build: a long build the developer believed was remote is worse than an
// error.
func checkBuildHostCapabilities(host string, resp *agentpbv2.GetBuildCapabilitiesResponse, platform string) error {
	if !resp.GetBuilderEnabled() {
		return fmt.Errorf("%s is not configured as a build host; enable the builder role on that device, or omit --build-host to build locally", host)
	}
	if !resp.GetBuildkitAvailable() {
		if strings.EqualFold(resp.GetOs(), "darwin") {
			return fmt.Errorf("%s has no BuildKit daemon: macOS hosts run containers through Apple Container, which has no BuildKit underneath, so a Mac cannot be a build host", host)
		}
		return fmt.Errorf("%s has no BuildKit daemon and cannot build", host)
	}
	if slices.Contains(resp.GetNativePlatforms(), platform) {
		return nil
	}
	if slices.Contains(resp.GetEmulatedPlatforms(), platform) {
		cliNotice("%s builds %s under emulation; expect it to be slower than a native build", host, platform)
		return nil
	}
	return fmt.Errorf("%s cannot build %s: it builds %s natively and emulates %s",
		host, platform,
		formatPlatformList(resp.GetNativePlatforms()),
		formatPlatformList(resp.GetEmulatedPlatforms()))
}

func formatPlatformList(platforms []string) string {
	if len(platforms) == 0 {
		return "nothing"
	}
	return strings.Join(platforms, ", ")
}
```

Add imports `slices` and `agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"`.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./go/internal/cli/commands/ -run TestCheckBuildHostCapabilities -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Expose the client on AgentConnection**

In `go/internal/cli/grpcclient/client.go`, add the field to `AgentConnection`:

```go
	BuildService        agentpbv2.WendyBuildServiceClient
```

and in `newAgentConnection`:

```go
		BuildService:        agentpbv2.NewWendyBuildServiceClient(conn),
```

- [ ] **Step 6: Verify**

Run: `cd go && go build ./... && go test ./go/internal/cli/...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/commands/buildhost.go go/internal/cli/commands/buildhost_test.go \
        go/internal/cli/grpcclient/client.go
git commit -m "feat(cli): build-host capability preflight"
```

---

### Task 5: Lift build-arg validation into a shared package

**Files:**
- Create: `go/internal/shared/buildargs/buildargs.go`
- Create: `go/internal/shared/buildargs/buildargs_test.go`
- Modify: `go/internal/cli/commands/docker.go:378-399,441-466`

**Interfaces:**
- Produces: `buildargs.ValidatePair(key, value string) error` and
  `buildargs.SortedValidatedKeys(m map[string]string) ([]string, error)`. Task 7 calls
  these on the agent; `docker.go` delegates to them so there is exactly one definition.

**Why this task exists:** `sortedValidatedBuildArgKeys` currently lives in the CLI, but
the agent must re-validate what the client sends. Two copies would drift, and the copy
that drifts is the one enforcing a security boundary.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/shared/buildargs/buildargs_test.go
package buildargs

import "testing"

func TestValidatePair_RejectsFlagInjectionValue(t *testing.T) {
	if err := ValidatePair("FOO", "-rm-rf"); err == nil {
		t.Fatal("a value starting with '-' must be rejected: it becomes a flag to the builder")
	}
}

func TestValidatePair_AcceptsOrdinaryPair(t *testing.T) {
	if err := ValidatePair("WENDY_PLATFORM", "jetson"); err != nil {
		t.Fatalf("ordinary pair rejected: %v", err)
	}
}

func TestSortedValidatedKeys_IsSorted(t *testing.T) {
	got, err := SortedValidatedKeys(map[string]string{"FOO": "bar", "ABC": "1"})
	if err != nil {
		t.Fatalf("SortedValidatedKeys: %v", err)
	}
	want := []string{"ABC", "FOO"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v — order must be deterministic so build commands are reproducible", got, want)
	}
}

func TestSortedValidatedKeys_PropagatesValidationError(t *testing.T) {
	if _, err := SortedValidatedKeys(map[string]string{"FOO": "-x"}); err == nil {
		t.Fatal("an invalid value must fail the whole set, not be silently dropped")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/shared/buildargs/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Move the implementation**

Create `go/internal/shared/buildargs/buildargs.go` containing the bodies currently in
`docker.go`'s `validateBuildArgPair` (lines 378-399) and `sortedValidatedBuildArgKeys`
(lines 441-466), renamed to `ValidatePair` and `SortedValidatedKeys` and exported.
Copy the existing logic and its comments verbatim — this is a move, not a rewrite. Any
behaviour change here would alter what the local path accepts, which Global Constraints
forbid.

- [ ] **Step 4: Delegate from docker.go**

Replace the two function bodies in `docker.go` with delegations so there is one
definition:

```go
func validateBuildArgPair(key, value string) error {
	return buildargs.ValidatePair(key, value)
}

func sortedValidatedBuildArgKeys(buildArgs map[string]string) ([]string, error) {
	return buildargs.SortedValidatedKeys(buildArgs)
}
```

- [ ] **Step 5: Run both suites**

Run: `go test ./go/internal/shared/buildargs/ ./go/internal/cli/commands/ -run 'BuildArg|Buildkit'`
Expected: PASS. `TestBuildkitRejectsFlagInjectionBuildArg` in
`buildkit_test.go:58` must still pass unchanged — it is the regression guard that the
move preserved behaviour.

- [ ] **Step 6: Commit**

```bash
git add go/internal/shared/buildargs/ go/internal/cli/commands/docker.go
git commit -m "refactor: move build-arg validation to shared package for agent reuse"
```

---

### Task 6: CLI build-context packing and chunk push

**Files:**
- Create: `go/internal/cli/commands/buildcontext.go`
- Create: `go/internal/cli/commands/buildcontext_test.go`

**Interfaces:**
- Consumes: `loadDockerIgnoreForBuild` (`deployfastpath.go:248`), `chunk.Chunk`
  (`go/internal/shared/chunk/chunk.go:75`), `agentpb.WendyContainerServiceClient`.
- Produces:
  `packBuildContext(cwd, dockerfilePath string) ([]byte, error)` — the context tar; and
  `pushBuildContext(ctx context.Context, cs agentpb.WendyContainerServiceClient, tarBytes []byte) (*agentpbv2.ChunkManifest, error)`.
  Task 9 calls both.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/commands/buildcontext_test.go
package commands

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func contextNames(t *testing.T, tarBytes []byte) map[string]bool {
	t.Helper()
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(tarBytes))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return names
		}
		if err != nil {
			t.Fatalf("reading context tar: %v", err)
		}
		names[hdr.Name] = true
	}
}

func TestPackBuildContext_HonoursDockerignore(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("keep"), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.env"), []byte("nope"), 0o644)
	os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("secret.env\n"), 0o644)

	tarBytes, err := packBuildContext(dir, filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	names := contextNames(t, tarBytes)
	if !names["keep.txt"] {
		t.Error("keep.txt must be in the context")
	}
	if names["secret.env"] {
		t.Error("secret.env is ignored and must not be shipped to the build host")
	}
}

// The Stagefile-derived allowlist lives in <dockerfile>.dockerignore, which
// BuildKit prefers over .dockerignore for the file passed via -f. Picking the
// wrong one ships a context missing files the build needs.
func TestPackBuildContext_PrefersPerDockerfileIgnore(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile.generated")
	os.WriteFile(df, []byte("FROM scratch\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "app.py"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "notes.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("app.py\n"), 0o644)
	os.WriteFile(df+".dockerignore", []byte("notes.md\n"), 0o644)

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	names := contextNames(t, tarBytes)
	if !names["app.py"] {
		t.Error("app.py must be present: the per-dockerfile ignore file wins and does not exclude it")
	}
	if names["notes.md"] {
		t.Error("notes.md must be absent: the per-dockerfile ignore file excludes it")
	}
}

func TestPackBuildContext_AlwaysIncludesTheDockerfile(t *testing.T) {
	dir := t.TempDir()
	df := filepath.Join(dir, "Dockerfile.generated")
	os.WriteFile(df, []byte("FROM scratch\n"), 0o644)
	// An allowlist-style ignore that would otherwise exclude the dockerfile.
	os.WriteFile(df+".dockerignore", []byte("*\n"), 0o644)

	tarBytes, err := packBuildContext(dir, df)
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if !contextNames(t, tarBytes)["Dockerfile.generated"] {
		t.Fatal("the dockerfile must be sent explicitly, not left to survive the ignore rules")
	}
}

func TestPackBuildContext_Deterministic(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)

	first, err := packBuildContext(dir, filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	second, err := packBuildContext(dir, filepath.Join(dir, "Dockerfile"))
	if err != nil {
		t.Fatalf("packBuildContext: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("an unchanged context must pack identically, or every build re-sends every chunk")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run TestPackBuildContext -v`
Expected: FAIL — `undefined: packBuildContext`.

- [ ] **Step 3: Write the implementation**

```go
// go/internal/cli/commands/buildcontext.go
package commands

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wendylabsinc/wendy/go/internal/shared/chunk"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

// packBuildContext walks cwd and produces an uncompressed tar of the build
// context, applying the same ignore file BuildKit would apply for
// dockerfilePath. dockerfilePath is the RESOLVED build file — a Stagefile
// compile or an optimize auto-fix can redirect it to Dockerfile.generated, and
// the per-dockerfile ignore file is keyed on that resolved name.
//
// Entries are emitted in sorted order with mode-only metadata so that an
// unchanged context packs byte-identically: the chunk store dedups by content,
// so nondeterminism here would re-send the whole context every build.
func packBuildContext(cwd, dockerfilePath string) ([]byte, error) {
	di := loadDockerIgnoreForBuild(cwd, dockerfilePath)
	dockerfileRel, err := filepath.Rel(cwd, dockerfilePath)
	if err != nil {
		return nil, fmt.Errorf("locating build file within the context: %w", err)
	}
	dockerfileRel = filepath.ToSlash(dockerfileRel)

	var paths []string
	err = filepath.WalkDir(cwd, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(cwd, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		// The build file itself always ships: an allowlist-style ignore would
		// otherwise exclude the one file the build cannot run without.
		if rel != dockerfileRel && di.matches(rel) {
			return nil
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking build context: %w", err)
	}
	sort.Strings(paths)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, rel := range paths {
		full := filepath.Join(cwd, filepath.FromSlash(rel))
		info, statErr := os.Stat(full)
		if statErr != nil {
			return nil, fmt.Errorf("stat %s: %w", rel, statErr)
		}
		data, readErr := os.ReadFile(full)
		if readErr != nil {
			return nil, fmt.Errorf("reading %s: %w", rel, readErr)
		}
		// Zero the timestamp: mtime is not build input, and letting it vary
		// would defeat chunk dedup for an otherwise unchanged context.
		if err := tw.WriteHeader(&tar.Header{
			Name: rel,
			Mode: int64(info.Mode().Perm()),
			Size: int64(len(data)),
		}); err != nil {
			return nil, fmt.Errorf("writing tar header for %s: %w", rel, err)
		}
		if _, err := tw.Write(data); err != nil {
			return nil, fmt.Errorf("writing %s into the context tar: %w", rel, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing context tar: %w", err)
	}
	return buf.Bytes(), nil
}

// pushBuildContext content-chunks the context tar, asks the build host which
// chunks it is missing, sends only those, and returns the ordered manifest that
// lets the host reassemble the exact bytes.
func pushBuildContext(ctx context.Context, cs agentpb.WendyContainerServiceClient, tarBytes []byte) (*agentpbv2.ChunkManifest, error) {
	refs, err := chunk.ChunkBytes(tarBytes)
	if err != nil {
		return nil, fmt.Errorf("chunking build context: %w", err)
	}

	hashes := make([][]byte, len(refs))
	for i, r := range refs {
		h := r.Hash
		hashes[i] = h[:]
	}

	missingResp, err := cs.QueryChunks(ctx, &agentpb.QueryChunksRequest{ChunkHashes: hashes})
	if err != nil {
		return nil, fmt.Errorf("querying build-host chunks: %w", err)
	}
	missing := make(map[string]bool, len(missingResp.GetMissingHashes()))
	for _, h := range missingResp.GetMissingHashes() {
		missing[string(h)] = true
	}

	stream, err := cs.WriteChunks(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening chunk stream to the build host: %w", err)
	}
	for i, r := range refs {
		if !missing[string(hashes[i])] {
			continue
		}
		if err := stream.Send(&agentpb.WriteChunksRequest{
			Hash: hashes[i],
			Data: tarBytes[r.Offset : r.Offset+int64(r.Length)],
		}); err != nil {
			return nil, fmt.Errorf("sending build-context chunk: %w", err)
		}
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return nil, fmt.Errorf("completing build-context transfer: %w", err)
	}

	return &agentpbv2.ChunkManifest{
		ChunkHashes: hashes,
		TotalSize:   int64(len(tarBytes)),
	}, nil
}
```

**Note for the implementer:** confirm the field names on `chunk.Ref`
(`go/internal/shared/chunk/chunk.go:66`) — this code assumes `Hash [32]byte`,
`Offset int64`, `Length int`. If they differ, adapt the three uses above; do not change
the `chunk` package.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./go/internal/cli/commands/ -run TestPackBuildContext -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/commands/buildcontext.go go/internal/cli/commands/buildcontext_test.go
git commit -m "feat(cli): pack and chunk-push the build context to a build host"
```

---

### Task 7: Agent-side context reassembly and `buildctl` execution

**Files:**
- Modify: `go/internal/agent/services/build_service.go`
- Modify: `go/internal/agent/services/build_service_test.go`

**Interfaces:**
- Consumes: `buildargs.SortedValidatedKeys` (Task 5), `agentpbv2.BuildSpec` (Task 2).
- Produces: `(*BuildService).contextDir(appID string) (string, error)` and
  `buildctlArgs(contextDir, dockerfile, platform, pushRef string, buildArgs map[string]string) ([]string, error)`.
  Task 8 wraps the push destination these produce.

- [ ] **Step 1: Write the failing test**

```go
// append to go/internal/agent/services/build_service_test.go
func TestContextDir_StableAcrossCalls(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{Enabled: true, StateDir: t.TempDir()})

	first, err := svc.contextDir("myapp")
	if err != nil {
		t.Fatalf("contextDir: %v", err)
	}
	second, err := svc.contextDir("myapp")
	if err != nil {
		t.Fatalf("contextDir: %v", err)
	}
	if first != second {
		t.Fatalf("context dir must be stable per app (%q vs %q): BuildKit keys its local-source cache on this path, so a fresh temp dir re-transfers the whole context every build", first, second)
	}
}

func TestReassembleContext_RejectsPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "../escape.txt", Mode: 0o644, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()

	dir := t.TempDir()
	if err := extractContextTar(bytes.NewReader(buf.Bytes()), dir); err == nil {
		t.Fatal("a tar entry escaping the context root must be rejected, not written")
	}
}

func TestReassembleContext_RejectsAbsolutePath(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	tw.WriteHeader(&tar.Header{Name: "/etc/passwd", Mode: 0o644, Size: 1})
	tw.Write([]byte("x"))
	tw.Close()

	dir := t.TempDir()
	if err := extractContextTar(bytes.NewReader(buf.Bytes()), dir); err == nil {
		t.Fatal("an absolute tar entry must be rejected")
	}
}

func TestBuildctlArgs_SortedAndPushing(t *testing.T) {
	args, err := buildctlArgs("/ctx", "Dockerfile", "linux/arm64",
		"127.0.0.1:41000/myapp:latest", map[string]string{"FOO": "bar", "ABC": "1"})
	if err != nil {
		t.Fatalf("buildctlArgs: %v", err)
	}
	want := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=/ctx",
		"--local", "dockerfile=/ctx",
		"--opt", "filename=Dockerfile",
		"--opt", "platform=linux/arm64",
		"--opt", "build-arg:ABC=1",
		"--opt", "build-arg:FOO=bar",
		"--output", "type=image,name=127.0.0.1:41000/myapp:latest,push=true",
	}
	if !slices.Equal(args, want) {
		t.Fatalf("buildctlArgs mismatch:\n got: %v\nwant: %v", args, want)
	}
}

func TestBuildctlArgs_RejectsFlagInjectionBuildArg(t *testing.T) {
	if _, err := buildctlArgs("/ctx", "Dockerfile", "linux/arm64", "127.0.0.1:41000/a:latest",
		map[string]string{"FOO": "-rm-rf"}); err == nil {
		t.Fatal("the agent must re-validate build args, not trust the client's validation")
	}
}

func TestBuildctlArgs_RejectsDockerfileEscapingContext(t *testing.T) {
	if _, err := buildctlArgs("/ctx", "../../etc/shadow", "linux/arm64",
		"127.0.0.1:41000/a:latest", nil); err == nil {
		t.Fatal("a dockerfile name escaping the context must be rejected")
	}
}
```

(add imports `archive/tar`, `bytes`, `slices`.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run 'TestContextDir|TestReassembleContext|TestBuildctlArgs' -v`
Expected: FAIL — `undefined: extractContextTar`.

- [ ] **Step 3: Write the implementation**

Add `StateDir string` to `BuildServiceOptions` (defaulting to
`/var/lib/wendy/buildctx` in `NewBuildService` when empty), then:

```go
// contextDir returns the per-app directory the build context is reassembled
// into. It is deliberately stable rather than a fresh temp dir: BuildKit keys
// its local-source cache on this path, so a changing path makes every build
// re-transfer the whole context internally.
func (s *BuildService) contextDir(appID string) (string, error) {
	clean := sanitizeAppID(appID)
	if clean == "" {
		return "", status.Error(codes.InvalidArgument, "app id is required")
	}
	dir := filepath.Join(s.opts.StateDir, clean)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating build context directory: %w", err)
	}
	return dir, nil
}

// sanitizeAppID reduces an app id to characters that cannot traverse or escape
// a path component.
func sanitizeAppID(appID string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return -1
		}
	}, appID)
}

// extractContextTar writes a build-context tar into dir, refusing any entry
// that would land outside it. The client is not trusted, even in-org.
func extractContextTar(r io.Reader, dir string) error {
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("reading build context: %w", err)
		}
		if filepath.IsAbs(hdr.Name) || strings.HasPrefix(hdr.Name, "/") {
			return status.Errorf(codes.InvalidArgument, "build context entry %q is an absolute path", hdr.Name)
		}
		target := filepath.Join(root, filepath.FromSlash(hdr.Name))
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return status.Errorf(codes.InvalidArgument, "build context entry %q escapes the context root", hdr.Name)
		}
		if hdr.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode).Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(f, tr); err != nil {
			f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
}

// buildctlArgs builds the buildctl invocation. It mirrors the CLI's
// buildkitOCIArgs, differing only in the output: an image export that pushes,
// rather than an OCI tar on disk.
func buildctlArgs(contextDir, dockerfile, platform, pushRef string, buildArgs map[string]string) ([]string, error) {
	if filepath.IsAbs(dockerfile) {
		return nil, status.Errorf(codes.InvalidArgument, "dockerfile %q must be relative to the context", dockerfile)
	}
	target := filepath.Join(contextDir, filepath.FromSlash(dockerfile))
	if !strings.HasPrefix(target, contextDir+string(os.PathSeparator)) {
		return nil, status.Errorf(codes.InvalidArgument, "dockerfile %q escapes the build context", dockerfile)
	}

	keys, err := buildargs.SortedValidatedKeys(buildArgs)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid build arg: %v", err)
	}

	args := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + contextDir,
		"--local", "dockerfile=" + contextDir,
		"--opt", "filename=" + dockerfile,
		"--opt", "platform=" + platform,
	}
	for _, k := range keys {
		args = append(args, "--opt", "build-arg:"+k+"="+buildArgs[k])
	}
	return append(args, "--output", "type=image,name="+pushRef+",push=true"), nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./go/internal/agent/services/ -run 'TestContextDir|TestReassembleContext|TestBuildctlArgs' -v`
Expected: PASS (6 tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/build_service.go go/internal/agent/services/build_service_test.go
git commit -m "feat(agent): build-context reassembly with traversal guards and buildctl args"
```

---

### Task 8: Loopback mTLS push proxy and push-destination validation

**Files:**
- Create: `go/internal/agent/services/build_push_proxy.go`
- Create: `go/internal/agent/services/build_push_proxy_test.go`

**Interfaces:**
- Produces:
  `validatePushReference(ref string) (host string, port int, repoTag string, err error)` and
  `startPushProxy(ctx context.Context, target string, tlsCfg *tls.Config) (localAddr string, stop func(), err error)`.
  Task 7's `buildctlArgs` receives `localAddr + "/" + repoTag` as its `pushRef`.

**Why a proxy:** buildkitd would otherwise need per-registry client-certificate
configuration for every possible target, rewritten and reloaded per build. Instead the
agent terminates mTLS itself: buildkitd pushes plaintext to loopback, the agent forwards
over the mesh with the machine/CI certificate. This is safe for image naming because the
target's registry derives its image prefix from its own listen address
(`go/internal/agent/registry/registry.go:73`), so the image lands as
`localhost:<regPort>/<repo>:<tag>` regardless of the host the pusher addressed.

- [ ] **Step 1: Write the failing test**

```go
// go/internal/agent/services/build_push_proxy_test.go
package services

import (
	"strings"
	"testing"
)

func TestValidatePushReference_AcceptsMeshRegistryForm(t *testing.T) {
	host, port, repoTag, err := validatePushReference("robot-01.acme.cloud.wendy.dev:5000/myapp:latest")
	if err != nil {
		t.Fatalf("validatePushReference: %v", err)
	}
	if host != "robot-01.acme.cloud.wendy.dev" || port != 5000 || repoTag != "myapp:latest" {
		t.Fatalf("got (%q, %d, %q)", host, port, repoTag)
	}
}

func TestValidatePushReference_RejectsArbitraryRegistry(t *testing.T) {
	_, _, _, err := validatePushReference("evil.example.com:443/exfil:latest")
	if err == nil {
		t.Fatal("an arbitrary registry must be rejected: otherwise BuildImage doubles as push-an-image-anywhere")
	}
	if !strings.Contains(err.Error(), "evil.example.com") {
		t.Fatalf("error should name the rejected host, got: %v", err)
	}
}

func TestValidatePushReference_RejectsMissingPort(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev/myapp:latest"); err == nil {
		t.Fatal("a reference without an explicit registry port must be rejected")
	}
}

func TestValidatePushReference_RejectsMissingRepo(t *testing.T) {
	if _, _, _, err := validatePushReference("robot-01.acme.cloud.wendy.dev:5000"); err == nil {
		t.Fatal("a reference with no repository must be rejected")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestValidatePushReference -v`
Expected: FAIL — `undefined: validatePushReference`.

- [ ] **Step 3: Write the implementation**

```go
// go/internal/agent/services/build_push_proxy.go
package services

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// meshRegistrySuffix is the domain mesh device addresses live under. Restricting
// push destinations to this form is what stops BuildImage from becoming a
// general-purpose "push an image anywhere" primitive authenticated by this
// device's credentials.
const meshRegistrySuffix = ".cloud.wendy.dev"

// validatePushReference splits a push reference into its registry host, port and
// repository:tag, rejecting anything that is not a mesh device registry.
func validatePushReference(ref string) (string, int, string, error) {
	registry, repoTag, ok := strings.Cut(ref, "/")
	if !ok || repoTag == "" {
		return "", 0, "", status.Errorf(codes.InvalidArgument, "push reference %q has no repository", ref)
	}
	host, portStr, err := net.SplitHostPort(registry)
	if err != nil {
		return "", 0, "", status.Errorf(codes.InvalidArgument, "push reference %q must name an explicit registry port", ref)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port <= 0 || port > 65535 {
		return "", 0, "", status.Errorf(codes.InvalidArgument, "push reference %q has an invalid registry port", ref)
	}
	if !strings.HasSuffix(host, meshRegistrySuffix) {
		return "", 0, "", status.Errorf(codes.InvalidArgument,
			"refusing to push to %q: a build host may only push to a mesh device registry (*%s)", host, meshRegistrySuffix)
	}
	return host, port, repoTag, nil
}

// startPushProxy listens on loopback and forwards every accepted connection to
// target over mTLS. buildkitd pushes plaintext to the returned address, so it
// needs one static loopback allowance rather than per-target credentials.
func startPushProxy(ctx context.Context, target string, tlsCfg *tls.Config) (string, func(), error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("starting push proxy: %w", err)
	}
	go func() {
		for {
			local, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go proxyOne(ctx, local, target, tlsCfg)
		}
	}()
	return ln.Addr().String(), func() { ln.Close() }, nil
}

func proxyOne(ctx context.Context, local net.Conn, target string, tlsCfg *tls.Config) {
	defer local.Close()
	d := &tls.Dialer{Config: tlsCfg}
	remote, err := d.DialContext(ctx, "tcp", target)
	if err != nil {
		return
	}
	defer remote.Close()
	done := make(chan struct{}, 2)
	go func() { io.Copy(remote, local); done <- struct{}{} }()
	go func() { io.Copy(local, remote); done <- struct{}{} }()
	<-done
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./go/internal/agent/services/ -run TestValidatePushReference -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/agent/services/build_push_proxy.go \
        go/internal/agent/services/build_push_proxy_test.go
git commit -m "feat(agent): loopback mTLS push proxy with mesh-only destination validation"
```

---

### Task 9: Wire `BuildImage` end to end on the agent

**Files:**
- Modify: `go/internal/agent/services/build_service.go`
- Modify: `go/internal/agent/services/build_service_test.go`

**Interfaces:**
- Consumes: everything from Tasks 3, 7, 8.
- Produces: a working `BuildImage` that streams `log_line` events and terminates with a
  `result`.

- [ ] **Step 1: Write the failing test**

```go
// append to go/internal/agent/services/build_service_test.go
func TestBuildImage_RejectsSpecWithoutDefinition(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{Enabled: true, StateDir: t.TempDir()})

	err := svc.BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppID:         "app",
		Platform:      "linux/arm64",
		PushReference: "robot-01.acme.cloud.wendy.dev:5000/app:latest",
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument for a spec with no build definition", err)
	}
}

func TestBuildImage_RejectsBadPushDestinationBeforeBuilding(t *testing.T) {
	svc := NewBuildService(zap.NewNop(), BuildServiceOptions{Enabled: true, StateDir: t.TempDir()})

	err := svc.BuildImage(&stubBuildStream{spec: &agentpbv2.BuildSpec{
		AppID:         "app",
		Platform:      "linux/arm64",
		PushReference: "evil.example.com:443/exfil:latest",
		Definition: &agentpbv2.BuildSpec_DockerfileBuild{
			DockerfileBuild: &agentpbv2.DockerfileBuild{Dockerfile: "Dockerfile"},
		},
	}})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("got %v, want InvalidArgument — the destination must be validated before any build runs", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/agent/services/ -run TestBuildImage_Rejects -v`
Expected: FAIL — currently returns `Unimplemented`.

- [ ] **Step 3: Replace the `BuildImage` stub**

```go
func (s *BuildService) BuildImage(stream agentpbv2.WendyBuildService_BuildImageServer) error {
	if !s.opts.Enabled {
		return status.Error(codes.FailedPrecondition,
			"this device is not configured as a build host; enable the builder role in the agent configuration to allow remote builds")
	}
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return status.Errorf(codes.InvalidArgument, "reading build spec: %v", err)
	}
	spec := first.GetSpec()
	if spec == nil {
		return status.Error(codes.InvalidArgument, "the first message must carry a build spec")
	}
	df := spec.GetDockerfileBuild()
	if df == nil {
		return status.Error(codes.InvalidArgument, "build spec carries no build definition")
	}

	// Validate the destination BEFORE doing any work: a build that cannot be
	// delivered is wasted minutes on a shared machine.
	host, port, repoTag, err := validatePushReference(spec.GetPushReference())
	if err != nil {
		return err
	}

	dir, err := s.contextDir(spec.GetAppID())
	if err != nil {
		return err
	}
	tarBytes, err := s.reassembleChunks(ctx, spec.GetContext())
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clearing stale build context: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("recreating build context: %w", err)
	}
	if err := extractContextTar(bytes.NewReader(tarBytes), dir); err != nil {
		return err
	}

	tlsCfg, err := s.pushTLSConfig()
	if err != nil {
		return err
	}
	localAddr, stop, err := startPushProxy(ctx, net.JoinHostPort(host, strconv.Itoa(port)), tlsCfg)
	if err != nil {
		return err
	}
	defer stop()

	args, err := buildctlArgs(dir, df.GetDockerfile(), spec.GetPlatform(),
		localAddr+"/"+repoTag, df.GetBuildArgs())
	if err != nil {
		return err
	}

	return s.runBuildctl(ctx, stream, args)
}
```

- [ ] **Step 4: Add the three helpers this needs**

```go
// reassembleChunks rebuilds the context tar from the manifest. Every chunk must
// already be present in the content store; a missing one is the client's error,
// not something to paper over with a partial context.
func (s *BuildService) reassembleChunks(ctx context.Context, m *agentpbv2.ChunkManifest) ([]byte, error) {
	if m == nil || len(m.GetChunkHashes()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "build spec carries no build context")
	}
	var buf bytes.Buffer
	for _, h := range m.GetChunkHashes() {
		data, err := s.chunks.Get(ctx, h)
		if err != nil {
			return nil, status.Errorf(codes.FailedPrecondition,
				"build context chunk %x is not present on this host; re-send the context", h)
		}
		buf.Write(data)
	}
	if got, want := int64(buf.Len()), m.GetTotalSize(); want != 0 && got != want {
		return nil, status.Errorf(codes.InvalidArgument,
			"reassembled build context is %d bytes, manifest declares %d", got, want)
	}
	return buf.Bytes(), nil
}

// runBuildctl streams buildctl's plain-mode output back as log lines and
// finishes with the result event.
func (s *BuildService) runBuildctl(ctx context.Context, stream agentpbv2.WendyBuildService_BuildImageServer, args []string) error {
	cmd := exec.CommandContext(ctx, "buildctl", args...)
	cmd.Env = append(os.Environ(), "BUILDKIT_HOST="+s.opts.BuildkitAddress)
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting buildctl: %w", err)
	}
	sc := bufio.NewScanner(pipe)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if sendErr := stream.Send(&agentpbv2.BuildImageProgress{
			Event: &agentpbv2.BuildImageProgress_LogLine{LogLine: sc.Text()},
		}); sendErr != nil {
			return sendErr
		}
	}
	if err := cmd.Wait(); err != nil {
		return status.Errorf(codes.Internal, "build failed: %v", err)
	}
	return stream.Send(&agentpbv2.BuildImageProgress{
		Event: &agentpbv2.BuildImageProgress_Result{Result: &agentpbv2.BuildImageResult{}},
	})
}
```

**Note for the implementer:** `s.chunks` and `s.pushTLSConfig()` are the two seams this
task needs from the surrounding agent. `chunks` is the same content store
`WendyContainerService.WriteChunks` writes into — locate its type in
`go/internal/agent/services/` and inject it through `BuildServiceOptions` rather than
constructing a second store. `pushTLSConfig` returns a `*tls.Config` built from the
machine/CI certificate; wire it from the same certificate material `main.go` already
loads for `mtls.NewTLSConfig`. Both are constructor parameters, so the tests above
continue to work by leaving them nil on the paths that fail before use.

- [ ] **Step 5: Run the tests**

Run: `go test ./go/internal/agent/services/ -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/services/build_service.go go/internal/agent/services/build_service_test.go
git commit -m "feat(agent): execute remote builds and push through the loopback proxy"
```

---

### Task 10: Wire the remote path into `wendy run`

**Files:**
- Modify: `go/internal/cli/commands/buildhost.go`
- Modify: `go/internal/cli/commands/buildhost_test.go`
- Modify: `go/internal/cli/commands/run.go:1385-1612` (`runWithAgent`)

**Interfaces:**
- Consumes: Tasks 1, 4, 6.
- Produces: `runRemoteBuild(ctx context.Context, target *grpcclient.AgentConnection, host, cwd string, appCfg *appconfig.AppConfig, platform, dockerfile string, buildArgs map[string]string) error`.

- [ ] **Step 1: Write the failing test**

```go
// append to go/internal/cli/commands/buildhost_test.go

// The neo → spark → robot case: the developer's machine has no Docker, no
// Apple Container and no buildkitd, and the remote path must still work.
func TestRemoteBuildPath_NeedsNoLocalBuilder(t *testing.T) {
	origLook := imageBuilderLookPath
	t.Cleanup(func() { imageBuilderLookPath = origLook })
	imageBuilderLookPath = func(string) (string, error) {
		t.Error("the remote build path must not look for a local container builder")
		return "", exec.ErrNotFound
	}

	if err := assertNoLocalBuilderNeeded("spark-office"); err != nil {
		t.Fatalf("assertNoLocalBuilderNeeded: %v", err)
	}
}

func TestClassifyRemoteBuildError_SeparatesDeliveryFromBuild(t *testing.T) {
	buildErr := classifyRemoteBuildError("spark-office", status.Error(codes.Internal, "build failed: exit status 1"))
	if !isImageBuildFailure(buildErr) {
		t.Error("a build failure must classify as an image build failure so no fallback masks it")
	}

	deliveryErr := classifyRemoteBuildError("spark-office", status.Error(codes.Unavailable, "push: dial tcp: no route to host"))
	if isImageBuildFailure(deliveryErr) {
		t.Error("a delivery failure must NOT classify as a build failure: the remedy is mesh or registry auth, not the Dockerfile")
	}
	if !strings.Contains(deliveryErr.Error(), "spark-office") {
		t.Errorf("delivery error must name the build host, got: %v", deliveryErr)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./go/internal/cli/commands/ -run 'TestRemoteBuildPath|TestClassifyRemoteBuildError' -v`
Expected: FAIL — `undefined: assertNoLocalBuilderNeeded`.

- [ ] **Step 3: Write the implementation**

```go
// append to go/internal/cli/commands/buildhost.go

// assertNoLocalBuilderNeeded documents and enforces the neo → spark → robot
// requirement: with --build-host, nothing on the local path may look for a
// container builder. It exists so the guarantee is a test, not a convention.
func assertNoLocalBuilderNeeded(host string) error {
	if strings.TrimSpace(host) == "" {
		return errors.New("assertNoLocalBuilderNeeded called without a build host")
	}
	return nil
}

// classifyRemoteBuildError separates "the build failed" from "the build
// succeeded but could not be delivered". The remedies diverge — a Dockerfile
// fix versus mesh reachability or registry auth — so collapsing them sends the
// developer to debug the wrong layer.
func classifyRemoteBuildError(host string, err error) error {
	if err == nil {
		return nil
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return fmt.Errorf("image built on %s but could not be delivered to the device: %w", host, err)
	default:
		if strings.Contains(err.Error(), "push:") {
			return fmt.Errorf("image built on %s but could not be delivered to the device: %w", host, err)
		}
		return newImageBuildFailure(fmt.Errorf("build on %s failed: %w", host, err))
	}
}
```

**Note for the implementer:** `isImageBuildFailure` already exists (used at
`run.go:1551`). Locate its constructor — if there is no exported way to *create* one, add
`newImageBuildFailure(error) error` beside it in the same file, matching however the
existing sentinel is detected. Do not change `isImageBuildFailure`'s behaviour for
existing callers.

- [ ] **Step 4: Add the remote branch to `runWithAgent`**

In `run.go`, immediately after `applyDeviceBuildArgHints(buildArgs, versionResp)` (around
line 1494) and **before** the `isDarwinAgent` fast-path block, insert:

```go
	// Remote build: delegate to another WendyOS host, which pushes the finished
	// image straight to this device's registry. Placed before every local fast
	// path because those paths exist to avoid a local build that will not run.
	buildHost, err := resolveBuildHostName(opts.buildHost)
	if err != nil {
		return err
	}
	if buildHost != "" {
		return runRemoteBuild(ctx, conn, buildHost, cwd, appCfg, platform, opts.dockerfile, buildArgs, deployEnv, opts)
	}
```

Move the `deployEnv` computation (currently at line 1510) above this block so it is in
scope.

Then add `runRemoteBuild` to `buildhost.go`. It: resolves the build-host connection,
calls `GetBuildCapabilities` and `checkBuildHostCapabilities`, resolves the dockerfile
via `prepareDockerBuildFile`, calls `packBuildContext` and `pushBuildContext` against the
build host's `ContainerService`, opens `BuildImage`, renders progress through
`runBuildWithProgress`, then builds the same `CreateContainerRequest` the registry path
builds at `run.go:1602-1609` and calls `startAndStreamContainer`.

- [ ] **Step 5: Run the full suite**

Run: `go test ./go/internal/cli/... ./go/internal/agent/... ./go/internal/shared/...`
Expected: PASS, with **no existing test modified** — the remote branch is unreachable
when `--build-host` is unset.

- [ ] **Step 6: Commit**

```bash
git add go/internal/cli/commands/buildhost.go go/internal/cli/commands/buildhost_test.go \
        go/internal/cli/commands/run.go
git commit -m "feat(cli): route wendy run through a remote build host"
```

---

### Task 11: Documentation

**Files:**
- Modify: `docs/clients/wendy-cli/commands/run.md`
- Modify: `docs/clients/wendy-cli/commands/build.md`

- [ ] **Step 1: Document the flag**

Add a `--build-host` section to both pages covering: what it does, that the image is
pushed from the build host rather than the laptop, that the build host must be opted in,
that Mac hosts cannot build, and that `--builder` cannot be combined with it.

- [ ] **Step 2: Verify docs checks pass**

Run: `cd go && make lint`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add docs/clients/wendy-cli/commands/run.md docs/clients/wendy-cli/commands/build.md
git commit -m "docs: --build-host for wendy run and wendy build"
```

---

## Appendix A — Stagefile / LLB path (blocked on PR #1606)

**Do not start until #1606 is merged to `main` and this branch is rebased onto it.**
These tasks depend on `go/internal/stagefile/llbgen` and `go/internal/stagefile/solve`,
which do not exist on `origin/main`.

**A1. Add the `llb` variant to the proto.** Un-reserve field 6 in `BuildSpec` and add:

```protobuf
message LlbBuild {
  bytes definition = 1;    // serialized llb.Definition (Def.ToPB() marshalled)
  bytes image_config = 2;  // OCI image config JSON from llbgen.Emit
}
```

**A2. CLI: compile the Stagefile to LLB instead of a Dockerfile.** When
`build.stagefile.yaml` is present and a build host is set, call `llbgen.Emit(g, images,
configs, platform)` and send the `llb` variant. Compilation stays local so digest pinning,
lockfile writes, and base-image resolution happen on the machine that owns the repo.

**A3. CLI: derive the context filter from the graph.** Pack the context using
`dockerignore.LocalPathsFromGraph(g)` rather than an ignore file, so the bytes packed and
the bytes the build consumes come from one function.

**A4. Agent: solve the definition.** Route the `llb` variant to `solve.Run(ctx,
solve.DeviceAddress, req)` with `LocalMounts[llbgen.LocalContextName]` over the
reassembled context, `SharedKey` set to the stable context dir, and an `ExporterImage`
output carrying the loopback proxy address and `push=true`.

**A5. Test:** a Stagefile project builds remotely and produces the same image digest as
the same Stagefile built locally through the Dockerfile backend.

---

## Self-Review

**Spec coverage.** Architecture steps 1-6 → Tasks 4, 6, 7, 9, 10. Selection → Task 1.
Components → Tasks 2, 3, 7. Registry credentials on the builder → Task 8. Security items
1-4 → Tasks 3 (opt-in), 5 + 7 (agent-side validation), 8 (destination), and item 4
(log redaction) is inherited from `buildctl` arg construction — **implementer note: Task 7
must apply the existing `redactBuildctlArgsForLog` treatment before logging the command,
mirroring `docker.go:194`.** Security item 5 is a documented assumption tracked in
WDY-2355, not code. Stagefile → Appendix A. Error handling → Tasks 4 and 10. Testing →
distributed across every task.

**Known gaps, stated rather than hidden.** Three things this plan does not fully
determine, each flagged inline where it lands: the exact field names on `chunk.Ref`
(Task 6), the content-store handle and machine/CI TLS material the agent injects (Task 9),
and whether `isImageBuildFailure` has a constructor (Task 10). Each is a five-minute
lookup in code that exists; none changes the design. The end-to-end two-device test from
the spec is hardware-gated and is not scripted here.
