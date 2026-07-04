# Jetson A/B Rootfs Redundancy Auto-Arm — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When `wendy os update` / `wendy device update` hits a Jetson whose firmware isn't armed for A/B rootfs switching, the agent arms redundancy and reboots, and the CLI auto-resumes the update once the device returns — instead of failing with a raw, noisy rejection.

**Architecture:** The agent gains a redundancy preflight (delegate to the on-device arm script, else write the efivar itself) that runs before `wendyos-update install`. It signals a new `ArmingRedundancy` variant on the `UpdateOS` stream, then reboots. The CLI treats that variant as "reconnect and retry once." Separately, the agent strips syslog `<PRI>` prefixes from updater output so every failure renders cleanly.

**Tech Stack:** Go, gRPC (server-streaming), protobuf (protoc via `scripts/generate-proto.sh`), Bubble Tea TUI, efivarfs, systemd/`nvbootctrl` (Jetson).

## Global Constraints

- Module root for Go work: `go/` (repo root is `/Users/joannisorlandos/git/wendy/wendyos`; the Go module lives under `go/`). Run all Go commands from `go/`.
- Proto sources live at repo-root `Proto/`; generated Go lands in `go/proto/gen/agentpb` (v1) and `go/proto/gen/agentpb/v2` (v2). Regenerate with `make proto` (from `go/`), which runs `scripts/generate-proto.sh`.
- **Regenerate Go protos only. Do NOT regenerate or hand-edit Swift protos** (standing protoc-gen-swift churn convention).
- Both the v1 (`agentpb`) and v2 (`agentpbv2`) `UpdateOS` transports must be updated in lockstep.
- Armed efivar payload is exactly `07 00 00 00 01 00 00 00` for `RootfsRedundancyLevel` and `07 00 00 00 03 00 00 00` for `RootfsRetryCountMax` (4 attribute bytes `NV|BS|RT` + UINT32), copied verbatim from the builder boot service.
- The reboot-loop guard marker path is `/data/wendyos-update/rootfs-redundancy-arm-attempted`, shared byte-for-byte with the builder's boot service.
- All code is package `services` (agent) or `commands` / `mcp` (CLI); follow existing file conventions.
- `gofmt -w -s .` before every commit; commits end with the repo's Co-Authored-By + Claude-Session trailers.

## File Structure

- `go/internal/agent/services/wendyos_backend.go` — MODIFY: add `stripSyslogPriority`, apply it in the stderr goroutine (Task 1).
- `go/internal/agent/services/wendyos_backend_test.go` — CREATE or MODIFY: `stripSyslogPriority` table test (Task 1).
- `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto` — MODIFY: add `ArmingRedundancy` to `UpdateOSResponse` (Task 2).
- `Proto/wendy/agent/services/v2/os_update_service.proto` — MODIFY: same (Task 2).
- `go/internal/agent/services/tegra_redundancy.go` — CREATE: detection + arm logic with injectable seams (Task 3).
- `go/internal/agent/services/tegra_redundancy_test.go` — CREATE: decision matrix + arm delegate/fallback tests (Task 3).
- `go/internal/agent/services/agent_service.go` — MODIFY: v1 `UpdateOS` preflight (Task 4).
- `go/internal/agent/services/os_update_service.go` — MODIFY: v2 `UpdateOS` preflight (Task 4).
- `go/internal/agent/services/os_update_service_test.go` — CREATE or MODIFY: handler preflight test (Task 4).
- `go/internal/cli/commands/os_cmd.go` — MODIFY: `errArmingRebooted` sentinel, detect variant in `streamOSUpdate`/`drainOSUpdateStream`, add `streamOSUpdateWithArmRetry`, wire caller (Task 5).
- `go/internal/cli/commands/device.go` — MODIFY: wire the second caller (Task 5).
- `go/internal/cli/mcp/tools_os.go` — MODIFY: handle the variant in the MCP path (Task 5).
- `go/internal/cli/commands/os_cmd_test.go` — CREATE or MODIFY: arm-retry reconnect test (Task 5).

---

### Task 1: Strip syslog `<PRI>` prefixes from updater output

**Files:**
- Modify: `go/internal/agent/services/wendyos_backend.go` (add helper; apply at the stderr goroutine, currently line 147-149)
- Test: `go/internal/agent/services/wendyos_backend_test.go`

**Interfaces:**
- Produces: `func stripSyslogPriority(line string) string`

- [ ] **Step 1: Write the failing test**

Create/append to `go/internal/agent/services/wendyos_backend_test.go`:

```go
package services

import "testing"

func TestStripSyslogPriority(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"info prefix + tag", "<6>wendyos-update: install: downloading url=x", "install: downloading url=x"},
		{"error prefix + tag", "<3>wendyos-update: artifact rejected: rootfs A/B redundancy is not armed", "artifact rejected: rootfs A/B redundancy is not armed"},
		{"prefix only, no tag", "<6>some raw line", "some raw line"},
		{"tag only, no prefix", "wendyos-update: hello", "hello"},
		{"no prefix, no tag", "plain message", "plain message"},
		{"malformed prefix passes through", "<abc>wendyos-update: x", "<abc>wendyos-update: x"},
		{"too many digits passes through", "<9999>x", "<9999>x"},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripSyslogPriority(c.in); got != c.want {
				t.Fatalf("stripSyslogPriority(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestStripSyslogPriority -v`
Expected: FAIL — `undefined: stripSyslogPriority`.

- [ ] **Step 3: Write minimal implementation**

Add to `go/internal/agent/services/wendyos_backend.go` (near `wendyOSInstallErrorMessage`). Add `regexp` to the import block:

```go
// syslogPriorityPrefix matches a leading syslog <PRI> token: '<' + 1-3 digits + '>'.
var syslogPriorityPrefix = regexp.MustCompile(`^<[0-9]{1,3}>`)

// stripSyslogPriority cleans one line of wendyos-update stderr for display: it
// removes a well-formed leading syslog <PRI> prefix (e.g. "<6>", "<3>") and the
// redundant repeated "wendyos-update: " program tag. Lines that don't match are
// returned unchanged, so this never corrupts unexpected output.
func stripSyslogPriority(line string) string {
	line = syslogPriorityPrefix.ReplaceAllString(line, "")
	return strings.TrimPrefix(line, "wendyos-update: ")
}
```

- [ ] **Step 4: Apply it in the stderr goroutine**

In `runInstall`, change the stderr scan (currently lines 147-149) from:

```go
			line := scanner.Text()
			outputTail.push(line)
			w.logger.Debug("wendyos-update output", zap.String("line", line))
```

to:

```go
			line := stripSyslogPriority(scanner.Text())
			outputTail.push(line)
			w.logger.Debug("wendyos-update output", zap.String("line", line))
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run TestStripSyslogPriority -v && gofmt -l internal/agent/services/wendyos_backend.go`
Expected: PASS; `gofmt -l` prints nothing.

- [ ] **Step 6: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add go/internal/agent/services/wendyos_backend.go go/internal/agent/services/wendyos_backend_test.go
git commit -m "fix(agent): strip syslog <PRI> prefixes from wendyos-update output"
```

---

### Task 2: Add `ArmingRedundancy` to the `UpdateOSResponse` proto (v1 + v2) and regenerate

**Files:**
- Modify: `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto` (message `UpdateOSResponse`, currently line 478)
- Modify: `Proto/wendy/agent/services/v2/os_update_service.proto` (message `UpdateOSResponse`, currently line 20)
- Regenerate: `go/proto/gen/agentpb/**` and `go/proto/gen/agentpb/v2/**`

**Interfaces:**
- Produces (generated, v1): `agentpb.UpdateOSResponse_ArmingRedundancy_` (oneof wrapper), `agentpb.UpdateOSResponse_ArmingRedundancy` (message with `Message string`, `WillReboot bool`), and `resp.GetArmingRedundancy() *UpdateOSResponse_ArmingRedundancy`.
- Produces (generated, v2): same names under `agentpbv2`.

- [ ] **Step 1: Edit the v1 proto**

In `Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto`, change the `UpdateOSResponse` oneof + add the nested message:

```proto
message UpdateOSResponse {
    oneof response_type {
        Progress progress = 1;
        Completed completed = 2;
        Failed failed = 3;
        // The device is arming Jetson A/B rootfs redundancy and rebooting; the
        // client should wait for the device to return and re-issue UpdateOS.
        ArmingRedundancy arming_redundancy = 4;
    }

    message Progress {
        // Current phase of the update (downloading, installing, etc.)
        string phase = 1;
        // Progress percentage (0-100)
        int32 percent = 2;
    }

    message Completed {
        // Whether a reboot is required to complete the update
        bool reboot_required = 1;
    }

    message Failed {
        // Error message describing the failure
        string error_message = 1;
    }

    // Emitted instead of installing when a Jetson's firmware is not armed for
    // A/B rootfs switching: the agent arms RootfsRedundancyLevel and reboots.
    message ArmingRedundancy {
        // Human-readable status shown while the device arms and reboots.
        string message = 1;
        // Always true today: arming only takes effect after a reboot.
        bool will_reboot = 2;
    }
}
```

- [ ] **Step 2: Edit the v2 proto**

In `Proto/wendy/agent/services/v2/os_update_service.proto`, apply the identical change to its `UpdateOSResponse` (add `ArmingRedundancy arming_redundancy = 4;` to the oneof and the nested `ArmingRedundancy` message with the same two fields + comments).

- [ ] **Step 3: Regenerate Go protos**

Run: `cd go && make proto`
Expected: completes with no error; `git status` shows modified files only under `go/proto/gen/agentpb/`.

- [ ] **Step 4: Verify the generated types compile and exist**

Run:
```bash
cd go && go build ./proto/... && \
  grep -rl "UpdateOSResponse_ArmingRedundancy" proto/gen/agentpb/ | head
```
Expected: build succeeds; grep lists at least the v1 and v2 generated files.

- [ ] **Step 5: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add Proto/wendy/agent/services/v1/wendy_agent_v1_service.proto \
        Proto/wendy/agent/services/v2/os_update_service.proto \
        go/proto/gen/agentpb
git commit -m "proto(agent): add ArmingRedundancy variant to UpdateOSResponse (v1+v2)"
```

---

### Task 3: Redundancy detection + arm operation (agent)

**Files:**
- Create: `go/internal/agent/services/tegra_redundancy.go`
- Test: `go/internal/agent/services/tegra_redundancy_test.go`

**Interfaces:**
- Consumes: `rebootSystem() error` (existing, `agent_service.go`), `envWithPath(string) []string` (existing, `agent_service.go`).
- Produces:
  - `type armDecision int` with `armNotNeeded`, `armPossible`, `armImpossibleNoSlot`, `armFailedPreviously`.
  - `type redundancyArmer struct { ... }` with methods `decide() armDecision` and `arm() error`.
  - `func newRedundancyArmer(logger *zap.Logger) *redundancyArmer`.
  - `var makeRedundancyArmer = newRedundancyArmer` (indirection so handler tests can inject a stub).
  - Message consts: `redundancyArmingMessage`, `redundancyNoSlotMessage`, `redundancyArmFailedMessage`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/services/tegra_redundancy_test.go`:

```go
package services

import (
	"errors"
	"testing"

	"go.uber.org/zap"
)

// newTestArmer returns an armer with all seams stubbed to safe no-ops; each test
// overrides only what it needs.
func newTestArmer() *redundancyArmer {
	return &redundancyArmer{
		logger:      zap.NewNop(),
		isJetson:    func() bool { return true },
		readEfivar:  func(string) ([]byte, error) { return nil, errors.New("missing") },
		statPath:    func(string) error { return nil },
		lookPath:    func(string) (string, bool) { return "", false },
		runScript:   func(string) error { return nil },
		writeEfivar: func(string, []byte) error { return nil },
		writeMarker: func(string) error { return nil },
		reboot:      func() error { return nil },
	}
}

func TestDecideNotJetson(t *testing.T) {
	a := newTestArmer()
	a.isJetson = func() bool { return false }
	if got := a.decide(); got != armNotNeeded {
		t.Fatalf("decide() = %v, want armNotNeeded", got)
	}
}

func TestDecideAlreadyArmed(t *testing.T) {
	a := newTestArmer()
	a.readEfivar = func(string) ([]byte, error) {
		return []byte{0x07, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}, nil
	}
	if got := a.decide(); got != armNotNeeded {
		t.Fatalf("decide() = %v, want armNotNeeded", got)
	}
}

func TestDecideNoAppBSlot(t *testing.T) {
	a := newTestArmer()
	a.statPath = func(p string) error {
		if p == appBPartition {
			return errors.New("no such device")
		}
		return nil
	}
	if got := a.decide(); got != armImpossibleNoSlot {
		t.Fatalf("decide() = %v, want armImpossibleNoSlot", got)
	}
}

func TestDecideMarkerAlreadySet(t *testing.T) {
	a := newTestArmer()
	// APP_b exists, marker exists, still unarmed -> arming failed before.
	if got := a.decide(); got != armFailedPreviously {
		t.Fatalf("decide() = %v, want armFailedPreviously", got)
	}
}

func TestDecideArmable(t *testing.T) {
	a := newTestArmer()
	a.statPath = func(p string) error {
		if p == armAttemptMarker {
			return errors.New("no marker yet")
		}
		return nil // APP_b present
	}
	if got := a.decide(); got != armPossible {
		t.Fatalf("decide() = %v, want armPossible", got)
	}
}

func TestArmDelegatesToScriptWhenPresent(t *testing.T) {
	a := newTestArmer()
	ran := ""
	a.lookPath = func(f string) (string, bool) { return f, true }
	a.runScript = func(p string) error { ran = p; return nil }
	wroteEfivar := false
	a.writeEfivar = func(string, []byte) error { wroteEfivar = true; return nil }
	if err := a.arm(); err != nil {
		t.Fatalf("arm() error = %v", err)
	}
	if ran != armScript {
		t.Fatalf("ran = %q, want %q", ran, armScript)
	}
	if wroteEfivar {
		t.Fatal("arm() wrote efivar directly despite script being present")
	}
}

func TestArmFallbackWritesEfivarAndReboots(t *testing.T) {
	a := newTestArmer()
	writes := map[string][]byte{}
	markerWritten := false
	rebooted := false
	a.lookPath = func(string) (string, bool) { return "", false } // no script
	a.writeMarker = func(string) error { markerWritten = true; return nil }
	a.writeEfivar = func(p string, d []byte) error { writes[p] = d; return nil }
	a.reboot = func() error { rebooted = true; return nil }

	if err := a.arm(); err != nil {
		t.Fatalf("arm() error = %v", err)
	}
	if !markerWritten {
		t.Fatal("fallback did not write the attempt marker")
	}
	if got := writes[rootfsRedundancyEfivar]; string(got) != string([]byte{0x07, 0, 0, 0, 0x01, 0, 0, 0}) {
		t.Fatalf("RootfsRedundancyLevel bytes = % x, want 07 00 00 00 01 00 00 00", got)
	}
	if got := writes[rootfsRetryCountMaxEfivar]; string(got) != string([]byte{0x07, 0, 0, 0, 0x03, 0, 0, 0}) {
		t.Fatalf("RootfsRetryCountMax bytes = % x, want 07 00 00 00 03 00 00 00", got)
	}
	if !rebooted {
		t.Fatal("fallback did not reboot")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run 'TestDecide|TestArm' -v`
Expected: FAIL — undefined `redundancyArmer`, `armDecision`, consts, etc.

- [ ] **Step 3: Write the implementation**

Create `go/internal/agent/services/tegra_redundancy.go`:

```go
package services

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"go.uber.org/zap"
)

// Jetson A/B rootfs redundancy is gated by a UEFI variable the firmware reads
// at boot. When it is missing or zero the device runs single-slot: an OTA
// writes the inactive slot and requests a slot switch the firmware ignores, so
// the update silently rolls back. These values mirror wendyos-update's
// tegrauefi connector and the builder's wendyos-tegra-arm-rootfs-redundancy
// boot service byte-for-byte.
const (
	rootfsRedundancyEfivar    = "/sys/firmware/efi/efivars/RootfsRedundancyLevel-781e084c-a330-417c-b678-38e696380cb9"
	rootfsRetryCountMaxEfivar = "/sys/firmware/efi/efivars/RootfsRetryCountMax-781e084c-a330-417c-b678-38e696380cb9"

	// appBPartition exists only on an A/B-flashed device. Its absence means the
	// device is genuinely single-slot and cannot be armed in software.
	appBPartition = "/dev/disk/by-partlabel/APP_b"

	// armAttemptMarker is the reboot-loop guard, shared with the boot service.
	armAttemptMarker = "/data/wendyos-update/rootfs-redundancy-arm-attempted"

	// armScript is the image-native arm-and-reboot helper (present on current
	// images, absent on older ones — the case the agent must handle itself).
	armScript = "/usr/sbin/wendyos-tegra-arm-rootfs-redundancy"
)

// efivar payload = 4 attribute bytes (0x07 = NV|BS|RT) + a UINT32 value.
var (
	rootfsRedundancyArmedBytes = []byte{0x07, 0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00}
	rootfsRetryCountMaxBytes   = []byte{0x07, 0x00, 0x00, 0x00, 0x03, 0x00, 0x00, 0x00}
)

const (
	redundancyArmingMessage    = "Arming A/B rootfs redundancy and rebooting device; the update will resume automatically once it is back online."
	redundancyNoSlotMessage    = "cannot update: this Jetson has no B rootfs slot (no APP_b partition), so A/B redundancy cannot be armed in software. Reflash the device with an A/B image, then retry."
	redundancyArmFailedMessage = "cannot update: rootfs A/B redundancy was armed on a previous attempt but the firmware still reports it inactive. This device needs a reflash with an A/B image."
)

type armDecision int

const (
	// armNotNeeded: not a Jetson, or already armed — proceed with the install.
	armNotNeeded armDecision = iota
	// armPossible: unarmed, APP_b present, no prior attempt — arm and reboot.
	armPossible
	// armImpossibleNoSlot: unarmed with no APP_b — needs a reflash.
	armImpossibleNoSlot
	// armFailedPreviously: attempt marker set but still unarmed — needs a reflash.
	armFailedPreviously
)

// redundancyArmer decides whether Jetson A/B rootfs redundancy must be armed
// before an OTA and performs the arm+reboot. All OS interactions are seams so
// the decision logic is unit-testable.
type redundancyArmer struct {
	logger *zap.Logger

	isJetson    func() bool
	readEfivar  func(path string) ([]byte, error)
	statPath    func(path string) error
	lookPath    func(file string) (string, bool)
	runScript   func(path string) error
	writeEfivar func(path string, data []byte) error
	writeMarker func(path string) error
	reboot      func() error
}

func newRedundancyArmer(logger *zap.Logger) *redundancyArmer {
	return &redundancyArmer{
		logger:      logger,
		isJetson:    jetsonDetected,
		readEfivar:  os.ReadFile,
		statPath:    func(p string) error { _, err := os.Stat(p); return err },
		lookPath:    func(f string) (string, bool) { p, err := exec.LookPath(f); return p, err == nil },
		runScript:   runArmScript,
		writeEfivar: writeEfivarFile,
		writeMarker: writeMarkerFile,
		reboot:      rebootSystem,
	}
}

// makeRedundancyArmer is an indirection so UpdateOS handler tests can inject a
// stubbed armer without real OS access.
var makeRedundancyArmer = newRedundancyArmer

func (a *redundancyArmer) armed() bool {
	raw, err := a.readEfivar(rootfsRedundancyEfivar)
	if err != nil {
		return false
	}
	return len(raw) >= 8 && (raw[4]|raw[5]|raw[6]|raw[7]) != 0
}

func (a *redundancyArmer) decide() armDecision {
	if !a.isJetson() || a.armed() {
		return armNotNeeded
	}
	if a.statPath(appBPartition) != nil {
		return armImpossibleNoSlot
	}
	if a.statPath(armAttemptMarker) == nil {
		return armFailedPreviously
	}
	return armPossible
}

// arm arms A/B rootfs redundancy and reboots. Call only when decide() returned
// armPossible. On the delegate path the on-device script writes the marker,
// arms the efivar, and reboots itself; on the fallback path the agent does all
// three. A non-nil return means arming failed before any reboot was triggered.
func (a *redundancyArmer) arm() error {
	if path, ok := a.lookPath(armScript); ok {
		a.logger.Info("arming rootfs A/B redundancy via on-device script", zap.String("script", path))
		return a.runScript(path)
	}
	a.logger.Info("on-device arm script absent; arming rootfs A/B redundancy directly")
	if err := a.writeMarker(armAttemptMarker); err != nil {
		return fmt.Errorf("writing arm attempt marker: %w", err)
	}
	if err := a.writeEfivar(rootfsRedundancyEfivar, rootfsRedundancyArmedBytes); err != nil {
		return fmt.Errorf("arming RootfsRedundancyLevel: %w", err)
	}
	if err := a.writeEfivar(rootfsRetryCountMaxEfivar, rootfsRetryCountMaxBytes); err != nil {
		return fmt.Errorf("writing RootfsRetryCountMax: %w", err)
	}
	return a.reboot()
}

func jetsonDetected() bool {
	_, err := exec.LookPath("nvbootctrl")
	return err == nil
}

func runArmScript(path string) error {
	cmd := exec.Command(path)
	cmd.Env = envWithPath("/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	return cmd.Run()
}

// writeEfivarFile writes attribute-header+value bytes to an efivarfs file.
// efivarfs marks existing variables immutable, so clear the flag first (best
// effort); a not-yet-existing variable is created by the write. The single
// os.WriteFile call performs one write() of the whole payload, as efivarfs
// requires — mirroring the boot service's `cp` into efivarfs.
func writeEfivarFile(path string, data []byte) error {
	if _, err := os.Stat(path); err == nil {
		_ = exec.Command("chattr", "-i", path).Run()
	}
	return os.WriteFile(path, data, 0o644)
}

func writeMarkerFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte("arm attempted by wendy-agent\n"), 0o644)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run 'TestDecide|TestArm' -v && gofmt -l internal/agent/services/tegra_redundancy.go`
Expected: all PASS; `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add go/internal/agent/services/tegra_redundancy.go go/internal/agent/services/tegra_redundancy_test.go
git commit -m "feat(agent): detect + arm Jetson A/B rootfs redundancy"
```

---

### Task 4: Wire the redundancy preflight into v1 + v2 UpdateOS handlers

**Files:**
- Modify: `go/internal/agent/services/agent_service.go` (v1 `UpdateOS`, after `selectUpdater` succeeds ~line 764, before `install` ~line 777)
- Modify: `go/internal/agent/services/os_update_service.go` (v2 `UpdateOS`, after `selectUpdater` ~line 45, before `install` ~line 55)
- Test: `go/internal/agent/services/os_update_service_test.go`

**Interfaces:**
- Consumes: `makeRedundancyArmer`, `armDecision` values, message consts (Task 3); generated `UpdateOSResponse_ArmingRedundancy_` / `...ArmingRedundancy` (Task 2); existing `sendOSUpdateFailure` (v1) / `sendOSUpdateFailureV2` (v2).

- [ ] **Step 1: Write the failing test (v2 handler)**

Create/append `go/internal/agent/services/os_update_service_test.go`. If a fake `UpdateOS` v2 stream already exists in the package's tests, reuse it and delete the local `fakeUpdateOSStreamV2` below.

```go
package services

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc"

	agentpbv2 "github.com/wendylabsinc/wendy/go/proto/gen/agentpb/v2"
)

type fakeUpdateOSStreamV2 struct {
	grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse]
	ctx  context.Context
	sent []*agentpbv2.UpdateOSResponse
}

func (f *fakeUpdateOSStreamV2) Context() context.Context { return f.ctx }
func (f *fakeUpdateOSStreamV2) Send(r *agentpbv2.UpdateOSResponse) error {
	f.sent = append(f.sent, r)
	return nil
}

func TestUpdateOSV2ArmsRedundancyAndReboots(t *testing.T) {
	rebooted := false
	prev := makeRedundancyArmer
	makeRedundancyArmer = func(*zap.Logger) *redundancyArmer {
		a := &redundancyArmer{
			logger:   zap.NewNop(),
			isJetson: func() bool { return true },
			// unarmed
			readEfivar: func(string) ([]byte, error) { return nil, context.Canceled },
			statPath: func(p string) error {
				if p == armAttemptMarker {
					return context.Canceled // no marker yet
				}
				return nil // APP_b present
			},
			lookPath:    func(string) (string, bool) { return "", false },
			writeMarker: func(string) error { return nil },
			writeEfivar: func(string, []byte) error { return nil },
			reboot:      func() error { rebooted = true; return nil },
		}
		return a
	}
	defer func() { makeRedundancyArmer = prev }()

	s := NewOSUpdateService(zap.NewNop())
	s.isWendyOSHost = func() bool { return true }
	stream := &fakeUpdateOSStreamV2{ctx: context.Background()}

	err := s.UpdateOS(&agentpbv2.UpdateOSRequest{ArtifactUrl: "http://x/y.wendy"}, stream)
	if err != nil {
		t.Fatalf("UpdateOS returned %v, want nil (rebooting)", err)
	}
	if !rebooted {
		t.Fatal("expected arm() to reboot")
	}
	if len(stream.sent) != 1 || stream.sent[0].GetArmingRedundancy() == nil {
		t.Fatalf("expected a single ArmingRedundancy response, got %+v", stream.sent)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/agent/services/ -run TestUpdateOSV2ArmsRedundancy -v`
Expected: FAIL — the handler still calls `install` (which errors without a real binary) and never sends `ArmingRedundancy`.

- [ ] **Step 3: Implement the v2 preflight**

In `os_update_service.go`, insert between the `selectUpdater` success log (line 45) and `sendProgress :=` (line 47):

```go
	if handled, err := armRedundancyPreflightV2(s.logger, stream); handled {
		return err
	}
```

And add this helper at the bottom of `os_update_service.go`:

```go
// armRedundancyPreflightV2 arms Jetson A/B rootfs redundancy before an OTA when
// required. It returns handled=true when it has produced a terminal outcome on
// the stream (a Failed message, or an ArmingRedundancy+reboot); the caller then
// returns err. handled=false means the install should proceed normally.
func armRedundancyPreflightV2(logger *zap.Logger, stream grpc.ServerStreamingServer[agentpbv2.UpdateOSResponse]) (bool, error) {
	armer := makeRedundancyArmer(logger)
	switch armer.decide() {
	case armImpossibleNoSlot:
		return true, sendOSUpdateFailureV2(stream, redundancyNoSlotMessage)
	case armFailedPreviously:
		return true, sendOSUpdateFailureV2(stream, redundancyArmFailedMessage)
	case armPossible:
		if err := stream.Send(&agentpbv2.UpdateOSResponse{
			ResponseType: &agentpbv2.UpdateOSResponse_ArmingRedundancy_{
				ArmingRedundancy: &agentpbv2.UpdateOSResponse_ArmingRedundancy{
					Message: redundancyArmingMessage, WillReboot: true,
				},
			},
		}); err != nil {
			return true, err
		}
		if err := armer.arm(); err != nil {
			return true, sendOSUpdateFailureV2(stream, "arming rootfs A/B redundancy failed: "+err.Error())
		}
		return true, nil // device is rebooting; the CLI reconnects and retries
	default: // armNotNeeded
		return false, nil
	}
}
```

- [ ] **Step 4: Implement the v1 preflight**

In `agent_service.go`, insert between the backend log (line 764) and `sendProgress :=` (line 766):

```go
	if handled, err := armRedundancyPreflightV1(s.logger, stream); handled {
		return err
	}
```

Add this helper near `sendOSUpdateFailure` (line 802):

```go
// armRedundancyPreflightV1 is the v1 twin of armRedundancyPreflightV2. See that
// function for semantics.
func armRedundancyPreflightV1(logger *zap.Logger, stream grpc.ServerStreamingServer[agentpb.UpdateOSResponse]) (bool, error) {
	armer := makeRedundancyArmer(logger)
	switch armer.decide() {
	case armImpossibleNoSlot:
		return true, sendOSUpdateFailure(stream, redundancyNoSlotMessage)
	case armFailedPreviously:
		return true, sendOSUpdateFailure(stream, redundancyArmFailedMessage)
	case armPossible:
		if err := stream.Send(&agentpb.UpdateOSResponse{
			ResponseType: &agentpb.UpdateOSResponse_ArmingRedundancy_{
				ArmingRedundancy: &agentpb.UpdateOSResponse_ArmingRedundancy{
					Message: redundancyArmingMessage, WillReboot: true,
				},
			},
		}); err != nil {
			return true, err
		}
		if err := armer.arm(); err != nil {
			return true, sendOSUpdateFailure(stream, "arming rootfs A/B redundancy failed: "+err.Error())
		}
		return true, nil
	default:
		return false, nil
	}
}
```

- [ ] **Step 5: Run tests + build to verify they pass**

Run: `cd go && go test ./internal/agent/services/ -run TestUpdateOSV2ArmsRedundancy -v && go build ./... && gofmt -l internal/agent/services/`
Expected: PASS; build clean; `gofmt -l` prints nothing.

- [ ] **Step 6: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add go/internal/agent/services/agent_service.go go/internal/agent/services/os_update_service.go go/internal/agent/services/os_update_service_test.go
git commit -m "feat(agent): arm rootfs redundancy in UpdateOS preflight (v1+v2)"
```

---

### Task 5: CLI auto-resume across the arming reboot + MCP message

**Files:**
- Modify: `go/internal/cli/commands/os_cmd.go` (sentinel + `streamOSUpdate` interactive path ~line 383-406 + `drainOSUpdateStream` + new `streamOSUpdateWithArmRetry` + caller ~line 344)
- Modify: `go/internal/cli/commands/device.go` (caller ~line 2040)
- Modify: `go/internal/cli/mcp/tools_os.go` (switch ~line 85-98)
- Test: `go/internal/cli/commands/os_cmd_test.go`

**Interfaces:**
- Consumes: generated `resp.GetArmingRedundancy()` (Task 2); existing `waitForDeviceOnline(ctx, host)`, `connectWithAutoTLS(ctx, addr)`, `hostPort(host, defaultAgentPort)`, `grpcclient.AgentConnection` (with `Close()`).
- Produces: `var errArmingRebooted error`; `func streamOSUpdateWithArmRetry(ctx context.Context, host string, conn *grpcclient.AgentConnection, redial func(context.Context) (*grpcclient.AgentConnection, error), artifactURL, updaterBackend string) error`.

- [ ] **Step 1: Write the failing test**

Append to `go/internal/cli/commands/os_cmd_test.go` (create if absent). This tests the retry wrapper's control flow with stubbed `streamOSUpdate`-equivalent behavior via a small indirection. Introduce a package var `streamOSUpdateFn = streamOSUpdate` used by the wrapper (added in Step 4) so the test can stub it:

```go
package commands

import (
	"context"
	"errors"
	"testing"

	"github.com/wendylabsinc/wendy/go/internal/grpcclient"
)

func TestStreamOSUpdateWithArmRetryResumesAfterArming(t *testing.T) {
	prevStream := streamOSUpdateFn
	prevWait := waitForDeviceOnlineFn
	defer func() { streamOSUpdateFn = prevStream; waitForDeviceOnlineFn = prevWait }()

	waitForDeviceOnlineFn = func(context.Context, string) error { return nil }

	calls := 0
	streamOSUpdateFn = func(context.Context, *grpcclient.AgentConnection, string, string) error {
		calls++
		if calls == 1 {
			return errArmingRebooted // first attempt: agent armed + rebooted
		}
		return nil // second attempt: succeeds
	}

	redialed := 0
	redial := func(context.Context) (*grpcclient.AgentConnection, error) {
		redialed++
		return &grpcclient.AgentConnection{}, nil
	}

	err := streamOSUpdateWithArmRetry(context.Background(), "dev.local", &grpcclient.AgentConnection{}, redial, "http://x/y.wendy", "")
	if err != nil {
		t.Fatalf("want nil after resume, got %v", err)
	}
	if calls != 2 || redialed != 1 {
		t.Fatalf("calls=%d redialed=%d, want calls=2 redialed=1", calls, redialed)
	}
}

func TestStreamOSUpdateWithArmRetryStopsOnSecondArming(t *testing.T) {
	prevStream := streamOSUpdateFn
	prevWait := waitForDeviceOnlineFn
	defer func() { streamOSUpdateFn = prevStream; waitForDeviceOnlineFn = prevWait }()

	waitForDeviceOnlineFn = func(context.Context, string) error { return nil }
	streamOSUpdateFn = func(context.Context, *grpcclient.AgentConnection, string, string) error {
		return errArmingRebooted // never converges
	}
	redial := func(context.Context) (*grpcclient.AgentConnection, error) {
		return &grpcclient.AgentConnection{}, nil
	}

	err := streamOSUpdateWithArmRetry(context.Background(), "dev.local", &grpcclient.AgentConnection{}, redial, "http://x/y.wendy", "")
	if err == nil || errors.Is(err, errArmingRebooted) {
		t.Fatalf("want a terminal reflash-style error, got %v", err)
	}
}
```

> Note: if `grpcclient.AgentConnection` cannot be zero-constructed or its `Close()` panics on a nil conn, adjust the test to use a tiny local fake that satisfies the same method set, or guard `Close()` in the wrapper. Verify `AgentConnection` has a `Close()` method first with `grep -n "func (.*AgentConnection) Close" internal/grpcclient/*.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd go && go test ./internal/cli/commands/ -run TestStreamOSUpdateWithArmRetry -v`
Expected: FAIL — `undefined: errArmingRebooted`, `streamOSUpdateFn`, `waitForDeviceOnlineFn`, `streamOSUpdateWithArmRetry`.

- [ ] **Step 3: Add the sentinel + test indirections**

In `os_cmd.go`, add near the top-level declarations (ensure `errors` is imported):

```go
// errArmingRebooted is returned by streamOSUpdate when the agent armed Jetson
// A/B rootfs redundancy and rebooted the device instead of installing. The
// caller waits for the device to return, re-dials, and retries the update once.
var errArmingRebooted = errors.New("device rebooting to arm rootfs redundancy")

// Indirections so tests can stub the reconnect loop's dependencies.
var (
	streamOSUpdateFn      = streamOSUpdate
	waitForDeviceOnlineFn = waitForDeviceOnline
)
```

- [ ] **Step 4: Detect the variant in `streamOSUpdate` and add the retry wrapper**

In `streamOSUpdate`, interactive goroutine, add a case before the `failed` case (after line 400):

```go
				if arming := resp.GetArmingRedundancy(); arming != nil {
					fmt.Println(arming.GetMessage())
					p.Send(tui.SpinnerDoneMsg{Err: errArmingRebooted})
					return
				}
```

In `drainOSUpdateStream` (non-interactive path), add the equivalent in its `Recv` loop — on `resp.GetArmingRedundancy() != nil`, print the message and `return errArmingRebooted`. (Open `drainOSUpdateStream` and mirror how it handles `GetFailed`; return `errArmingRebooted` in the same shape.)

Add the wrapper (near `streamOSUpdate`):

```go
// streamOSUpdateWithArmRetry runs the OS update and transparently handles the
// Jetson "arm A/B rootfs redundancy + reboot" preflight: if the agent reboots
// to arm redundancy, it waits for the device to return, re-dials via redial,
// and retries the update exactly once. A second arming request means the device
// could not be armed and needs a reflash.
func streamOSUpdateWithArmRetry(ctx context.Context, host string, conn *grpcclient.AgentConnection, redial func(context.Context) (*grpcclient.AgentConnection, error), artifactURL, updaterBackend string) error {
	err := streamOSUpdateFn(ctx, conn, artifactURL, updaterBackend)
	if !errors.Is(err, errArmingRebooted) {
		return err
	}
	fmt.Println("Waiting for device to come back online after arming redundancy...")
	if err := waitForDeviceOnlineFn(ctx, host); err != nil {
		return err
	}
	newConn, err := redial(ctx)
	if err != nil {
		return fmt.Errorf("reconnecting after arming redundancy: %w", err)
	}
	defer newConn.Close()
	if err := streamOSUpdateFn(ctx, newConn, artifactURL, updaterBackend); errors.Is(err, errArmingRebooted) {
		return fmt.Errorf("device requested arming rootfs redundancy again after a reboot; it likely needs a reflash with an A/B image")
	} else {
		return err
	}
}
```

- [ ] **Step 5: Wire the two callers**

In `os_cmd.go` at the update command (currently line 344), replace:

```go
			if err := streamOSUpdate(ctx, conn, artifactURL, ""); err != nil {
				return err
			}
```

with:

```go
			redial := func(c context.Context) (*grpcclient.AgentConnection, error) {
				return connectWithAutoTLS(c, hostPort(conn.Host, defaultAgentPort))
			}
			if err := streamOSUpdateWithArmRetry(ctx, conn.Host, conn, redial, artifactURL, ""); err != nil {
				return err
			}
```

In `device.go` (currently line 2040), apply the same replacement, using that call site's connection variable (`conn`) and host (`priorConn.Host`) for `redial`/`host`.

- [ ] **Step 6: Handle the variant in the MCP path**

In `tools_os.go`, add a case to the `switch` (after the `Completed_` case, before/alongside `Failed_`):

```go
		case *agentpb.UpdateOSResponse_ArmingRedundancy_:
			return mcpgo.NewToolResultText(resp.GetArmingRedundancy().GetMessage() +
				"\nThe device is rebooting; re-run the OS update once it is back online."), nil
```

- [ ] **Step 7: Run tests + build to verify they pass**

Run: `cd go && go test ./internal/cli/... -run 'TestStreamOSUpdateWithArmRetry' -v && go build ./... && gofmt -l internal/cli/`
Expected: PASS; build clean; `gofmt -l` prints nothing.

- [ ] **Step 8: Commit**

```bash
cd "$(git rev-parse --show-toplevel)"
git add go/internal/cli/commands/os_cmd.go go/internal/cli/commands/device.go go/internal/cli/mcp/tools_os.go go/internal/cli/commands/os_cmd_test.go
git commit -m "feat(cli): auto-resume OS update across the rootfs-redundancy arming reboot"
```

---

### Task 6: Full build, vet, and package test sweep

- [ ] **Step 1: Build everything**

Run: `cd go && go build ./...`
Expected: no errors.

- [ ] **Step 2: Vet + format**

Run: `cd go && go vet ./... && gofmt -l .`
Expected: vet clean; `gofmt -l` prints nothing.

- [ ] **Step 3: Run the touched packages' tests**

Run: `cd go && go test ./internal/agent/services/... ./internal/cli/...`
Expected: PASS.

- [ ] **Step 4: Commit any formatting fixups (if needed)**

```bash
cd "$(git rev-parse --show-toplevel)"
git add -A && git commit -m "chore: gofmt/vet fixups" || echo "nothing to commit"
```

---

## Notes for the implementer

- **Hardware-unverified paths:** the efivarfs write (`writeEfivarFile`) and the reboot cannot be exercised off-device. The unit tests cover the decision logic and the byte payloads; the on-device write/reboot mirror the builder's boot service and must be smoke-tested on a real single-slot-but-A/B-capable Orin before merge. Flag this in the PR body.
- **v1 vs v2 reboot:** v1 `UpdateOS` reboots itself after a successful install; v2 does not. The arming preflight `return`s before reaching either path, so neither completion/reboot runs on the arming turn — the reboot comes solely from `arm()`.
- **Do not** thread the arming reboot through the existing post-update `waitForDeviceOnline` at line 350; that one is for the *successful install's* reboot. The arming reconnect is internal to `streamOSUpdateWithArmRetry`.
- If `connectWithAutoTLS`/`AgentConnection.Close` signatures differ from what's assumed, adapt the `redial` closure and wrapper accordingly — the shapes above match `reportOSUpdateOutcome` (os_cmd.go:609).
