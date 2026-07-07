# `admin` Entitlement + Agent Local Unix-Socket gRPC — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let entitled on-device containers reach the wendy-agent's full gRPC over a local unix socket with no mTLS, gated solely by a new `admin` entitlement.

**Architecture:** A new `admin` appconfig entitlement; an `applyAdmin` OCI step that bind-mounts `/run/wendy/agent.sock` into entitled containers + sets `WENDY_AGENT_SOCKET`; and a third agent gRPC listener (plain, no mTLS) on that unix socket reusing the existing `registerAllServices` closure.

**Tech Stack:** Go; gRPC (`google.golang.org/grpc`); the agent OCI/appconfig packages; `go test`.

## Global Constraints

- Language Go; run `gofmt -w` on every changed `.go` file before committing.
- Device mount uses `Access`-free bind mount (it's a socket, not a device node); options `["rbind", "nosuid", "noexec"]`.
- **Invariant:** an app without `admin` produces a byte-for-byte unchanged OCI spec (no socket mount, no `WENDY_AGENT_SOCKET`). Tested.
- At most one `admin` entitlement per app (mirrors the `mcp` rule).
- The local socket exposes the **full** service set with **no** auth; the entitlement-gated mount is the entire trust boundary. Document the blast radius.
- Socket path: `/run/wendy/agent.sock`, mode `0660`.
- Test packages (run from `go/`): `go test ./internal/shared/appconfig/ ./internal/agent/oci/ ./internal/agent/localsocket/`.

---

### Task 1: `admin` entitlement type & validation (appconfig)

**Files:**
- Modify: `go/internal/shared/appconfig/appconfig.go` (constants, `ValidEntitlementTypes`, `allowedKeys`, `validateEntitlements`)
- Test: `go/internal/shared/appconfig/appconfig_test.go`

**Interfaces:**
- Produces: `appconfig.EntitlementAdmin = "admin"`; accepted by `Validate()`/`ValidateJSON`; a second `admin` entitlement rejected.

- [ ] **Step 1: Write the failing tests**

Append to `go/internal/shared/appconfig/appconfig_test.go`:

```go
func TestAdminEntitlementValid(t *testing.T) {
	cfg := &AppConfig{AppID: "test", Entitlements: []Entitlement{{Type: EntitlementAdmin}}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestAdminEntitlementDuplicateRejected(t *testing.T) {
	cfg := &AppConfig{AppID: "test", Entitlements: []Entitlement{
		{Type: EntitlementAdmin}, {Type: EntitlementAdmin},
	}}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected error for duplicate admin entitlement")
	}
}

func TestAdminEntitlementJSONNoWarnings(t *testing.T) {
	warnings := ValidateJSON([]byte(`{"appId":"test","entitlements":[{"type":"admin"}]}`))
	if len(warnings) != 0 {
		t.Fatalf("got %d warnings, want 0: %v", len(warnings), warnings)
	}
}

func TestAdminEntitlementJSONUnknownKeyWarns(t *testing.T) {
	warnings := ValidateJSON([]byte(`{"appId":"test","entitlements":[{"type":"admin","bogus":1}]}`))
	if len(warnings) == 0 {
		t.Fatal("expected warning for unknown key on admin entitlement")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/shared/appconfig/ -run TestAdminEntitlement -v`
Expected: FAIL — `undefined: EntitlementAdmin`.

- [ ] **Step 3: Add the constant, allowed keys, and duplicate check**

In `appconfig.go`, add to the entitlement constants (after `EntitlementMCP`):

```go
	EntitlementMCP       = "mcp"
	EntitlementAdmin     = "admin"
```

Add to `ValidEntitlementTypes` (after `EntitlementMCP`):

```go
	EntitlementMCP,
	EntitlementAdmin,
```

Add to `allowedKeys`:

```go
	EntitlementMCP:       {"type", "port"},
	EntitlementAdmin:     {"type"},
```

In `validateEntitlements`, after the existing `mcpCount` block, add:

```go
	adminCount := 0
	for _, e := range entitlements {
		if e.Type == EntitlementAdmin {
			adminCount++
		}
	}
	if adminCount > 1 {
		return fmt.Errorf("at most one admin entitlement is allowed in %s, found %d", prefix, adminCount)
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/shared/appconfig/ -run TestAdminEntitlement -v`
Expected: PASS (4 tests).

- [ ] **Step 5: gofmt + full package + commit**

```bash
cd go && gofmt -w internal/shared/appconfig/appconfig.go internal/shared/appconfig/appconfig_test.go && go test ./internal/shared/appconfig/
git add go/internal/shared/appconfig/appconfig.go go/internal/shared/appconfig/appconfig_test.go
git commit -m "feat(appconfig): add admin entitlement type and validation"
```

---

### Task 2: `applyAdmin` socket passthrough (OCI)

**Files:**
- Modify: `go/internal/agent/oci/entitlements.go` (the `ApplyEntitlements` switch + a new `applyAdmin`)
- Test: `go/internal/agent/oci/entitlements_test.go`

**Interfaces:**
- Consumes: `appconfig.EntitlementAdmin` (Task 1); existing `Mount`, `DefaultSpec`, `ApplyEntitlements`, `hasGID` (test helper).
- Produces: `applyAdmin(spec *Spec)`; stubbable package var `adminAgentSocketPath string`.

- [ ] **Step 1: Write the failing tests**

Append to `go/internal/agent/oci/entitlements_test.go`:

```go
func hasMount(spec *Spec, dest string) bool {
	for _, m := range spec.Mounts {
		if m.Destination == dest {
			return true
		}
	}
	return false
}

func hasEnv(spec *Spec, kv string) bool {
	for _, e := range spec.Process.Env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestApplyAdmin_MountsSocketWhenPresent(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "agent.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()
	origPath := adminAgentSocketPath
	t.Cleanup(func() { adminAgentSocketPath = origPath })
	adminAgentSocketPath = sock

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementAdmin}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if !hasMount(spec, "/run/wendy/agent.sock") {
		t.Error("expected /run/wendy/agent.sock bind mount")
	}
	if !hasEnv(spec, "WENDY_AGENT_SOCKET=/run/wendy/agent.sock") {
		t.Error("expected WENDY_AGENT_SOCKET env")
	}
}

func TestApplyAdmin_NoSocketWhenAbsent(t *testing.T) {
	origPath := adminAgentSocketPath
	t.Cleanup(func() { adminAgentSocketPath = origPath })
	adminAgentSocketPath = filepath.Join(t.TempDir(), "missing.sock")

	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementAdmin}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if hasMount(spec, "/run/wendy/agent.sock") {
		t.Error("must not mount a missing socket")
	}
}

func TestApplyAdmin_NonAdminAppUnchanged(t *testing.T) {
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{AppID: "test", Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementNetwork}}}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements: %v", err)
	}
	if hasMount(spec, "/run/wendy/agent.sock") || hasEnv(spec, "WENDY_AGENT_SOCKET=/run/wendy/agent.sock") {
		t.Error("non-admin app must not get the agent socket")
	}
}
```

(If `hasMount`/`hasEnv` already exist in the test file, drop the duplicate definitions and keep the test funcs.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd go && go test ./internal/agent/oci/ -run TestApplyAdmin -v`
Expected: FAIL — `undefined: adminAgentSocketPath`.

- [ ] **Step 3: Add the var, `applyAdmin`, and switch wiring**

In `entitlements.go`, near the other stubbable path vars (e.g. after `udevRuntimeDir`):

```go
// adminAgentSocketPath is the host wendy-agent local control socket bind-mounted
// into containers granted the admin entitlement. Behind a var so tests can point
// it at a temp socket.
var adminAgentSocketPath = "/run/wendy/agent.sock"
```

Add `applyAdmin` (after `applyGPU`):

```go
// applyAdmin grants a container access to the wendy-agent's local control socket
// (full gRPC, no mTLS). It is the entire trust boundary: only containers that
// declare the admin entitlement get the socket, so anything with this can fully
// control the device's apps. The mount is conditional on the host socket
// existing so an app still starts if the agent socket is down (no-op-safe).
func applyAdmin(spec *Spec) {
	fi, err := os.Lstat(adminAgentSocketPath)
	if err != nil || fi.Mode()&os.ModeSocket == 0 {
		return
	}
	spec.Mounts = append(spec.Mounts, Mount{
		Destination: "/run/wendy/agent.sock",
		Source:      adminAgentSocketPath,
		Type:        "bind",
		Options:     []string{"rbind", "nosuid", "noexec"},
	})
	spec.Process.Env = append(spec.Process.Env, "WENDY_AGENT_SOCKET=/run/wendy/agent.sock")
}
```

Wire into the `ApplyEntitlements` switch (after the `EntitlementSerial` case):

```go
		case appconfig.EntitlementAdmin:
			applyAdmin(spec)
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd go && go test ./internal/agent/oci/ -run TestApplyAdmin -v`
Expected: PASS (3 tests).

- [ ] **Step 5: gofmt + full package + commit**

```bash
cd go && gofmt -w internal/agent/oci/entitlements.go internal/agent/oci/entitlements_test.go && go test ./internal/agent/oci/
git add go/internal/agent/oci/entitlements.go go/internal/agent/oci/entitlements_test.go
git commit -m "feat(oci): applyAdmin bind-mounts the local agent socket"
```

---

### Task 3: Agent local unix-socket listener

**Files:**
- Create: `go/internal/agent/localsocket/localsocket.go`
- Test: `go/internal/agent/localsocket/localsocket_test.go`
- Modify: `go/cmd/wendy-agent/main.go` (add the listener using `registerAllServices`)

**Interfaces:**
- Produces: `localsocket.Listen(path string) (net.Listener, error)` — creates the parent dir, removes a stale socket, listens on the unix socket, chmods it `0660`.
- Consumes (in main.go): existing `registerAllServices(srv *grpc.Server)` (main.go:392), `interceptor.UnaryErrorInterceptor`/`StreamErrorInterceptor`.

- [ ] **Step 1: Write the failing test**

Create `go/internal/agent/localsocket/localsocket_test.go`:

```go
package localsocket

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func TestListen_CreatesSocketAndServesPlainGRPC(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "nested", "agent.sock")

	lis, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer lis.Close()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("socket not created: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o660 {
		t.Errorf("socket mode = %o, want 660", perm)
	}

	srv := grpc.NewServer()
	healthpb.RegisterHealthServer(srv, health.NewServer())
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient("unix://"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	resp, err := healthpb.NewHealthClient(conn).Check(context.Background(), &healthpb.HealthCheckRequest{})
	if err != nil {
		t.Fatalf("health check over UDS failed (plain gRPC, no mTLS): %v", err)
	}
	if resp.Status != healthpb.HealthCheckResponse_SERVING {
		t.Errorf("status = %v, want SERVING", resp.Status)
	}
}

func TestListen_RemovesStaleSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "agent.sock")
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("seed stale: %v", err)
	}
	lis, err := Listen(sock)
	if err != nil {
		t.Fatalf("Listen over stale socket: %v", err)
	}
	lis.Close()
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd go && go test ./internal/agent/localsocket/ -v`
Expected: FAIL — package/`Listen` undefined (build error).

- [ ] **Step 3: Implement `localsocket.Listen`**

Create `go/internal/agent/localsocket/localsocket.go`:

```go
// Package localsocket provides the wendy-agent's local unix-domain-socket
// listener. The socket carries the agent's full gRPC with no mTLS; access is
// gated entirely by the admin entitlement, which bind-mounts the socket into
// entitled containers (see oci.applyAdmin).
package localsocket

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
)

// Listen creates the socket's parent directory, removes any stale socket at
// path, listens on it, and restricts it to mode 0660.
func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create socket dir: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove stale socket: %w", err)
	}
	lis, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on unix socket: %w", err)
	}
	if err := os.Chmod(path, 0o660); err != nil {
		lis.Close()
		return nil, fmt.Errorf("chmod socket: %w", err)
	}
	return lis, nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd go && go test ./internal/agent/localsocket/ -v`
Expected: PASS (2 tests).

- [ ] **Step 5: Wire the listener into the agent**

In `go/cmd/wendy-agent/main.go`, after the `agentServer` block (the existing second listener, ~main.go:565), add a third listener that reuses `registerAllServices`:

```go
	// Local control socket: the agent's full gRPC over a unix socket with NO
	// mTLS. Access is gated solely by the admin entitlement (oci.applyAdmin
	// bind-mounts this socket into entitled containers). Disabled with
	// WENDY_LOCAL_SOCKET=off.
	var localSocketServer *grpc.Server
	if os.Getenv("WENDY_LOCAL_SOCKET") != "off" {
		localSocketServer = grpc.NewServer(
			grpc.UnaryInterceptor(interceptor.UnaryErrorInterceptor(logger)),
			grpc.StreamInterceptor(interceptor.StreamErrorInterceptor(logger)),
		)
		registerAllServices(localSocketServer)

		const localSocketPath = "/run/wendy/agent.sock"
		localLis, err := localsocket.Listen(localSocketPath)
		if err != nil {
			logger.Error("Failed to listen on local control socket", zap.Error(err))
		} else {
			wg.Add(1)
			go func() {
				defer wg.Done()
				logger.Info("Agent local control socket listening", zap.String("path", localSocketPath))
				if err := localSocketServer.Serve(localLis); err != nil {
					logger.Error("Local control socket server error", zap.Error(err))
				}
			}()
		}
	}
```

Add the import `"github.com/wendylabsinc/wendy/go/internal/agent/localsocket"`, and in the shutdown section (next to `agentServer.GracefulStop()`, ~main.go:675) add:

```go
	if localSocketServer != nil {
		localSocketServer.GracefulStop()
	}
```

- [ ] **Step 6: Build the agent + commit**

```bash
cd go && gofmt -w cmd/wendy-agent/main.go internal/agent/localsocket/localsocket.go internal/agent/localsocket/localsocket_test.go
go build ./cmd/wendy-agent/ && go test ./internal/agent/localsocket/
git add go/internal/agent/localsocket/ go/cmd/wendy-agent/main.go
git commit -m "feat(agent): serve full gRPC on a local unix socket (no mTLS)"
```

---

### Task 4: Document the `admin` entitlement

**Files:**
- Modify: `plugins/wendy-agentic-coding/skills/wendy-entitlements/SKILL.md`
- Modify (if present): `go/internal/cli/assets/docs/entitlements.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Find the entitlement docs**

Run: `grep -rln "entitlement" plugins/wendy-agentic-coding/skills/wendy-entitlements/ go/internal/cli/assets/docs/ | head`

- [ ] **Step 2: Add the `admin` entry (with the blast-radius warning)**

In each file that enumerates entitlements, add, matching the existing `gpu` entry's style:

```markdown
- **`admin`** — grants the container the wendy-agent's local control socket
  (`/run/wendy/agent.sock`, exposed as `WENDY_AGENT_SOCKET`). This is the agent's
  **full gRPC with no authentication** — an app with `admin` can start, stop, and
  delete apps and read all device data locally. Grant it only to fully-trusted
  first-party apps (e.g. the WendyOS shell). Shape: `{ "type": "admin" }`, at most
  one per app. Requires an agent build that serves the local socket.
```

- [ ] **Step 3: Commit**

```bash
git add plugins/wendy-agentic-coding/skills/wendy-entitlements/SKILL.md
git commit -m "docs(entitlements): document the admin entitlement"
```

---

## Self-Review

**Spec coverage:** `admin` type+validation → Task 1; `applyAdmin` socket mount + env + non-admin invariant → Task 2; agent local unix-socket server reusing `registerAllServices` → Task 3; security/blast-radius docs → Task 4. Local-server integration test (plain gRPC over UDS, 0660 perms, stale-socket removal) → Task 3. ✓

**Placeholder scan:** none — every step has complete code/commands.

**Type consistency:** `EntitlementAdmin`, `adminAgentSocketPath`, `applyAdmin(spec *Spec)`, `localsocket.Listen(path) (net.Listener, error)` are used identically where defined and consumed. `hasMount`/`hasEnv` are guarded against duplication. Socket path `/run/wendy/agent.sock` and mode `0660` are consistent across tasks.

**Out of scope (separate specs):** the shell's grpc-swift `AgentDataSource` (sub-project B) and deploying the new agent to the device (sub-project C).
