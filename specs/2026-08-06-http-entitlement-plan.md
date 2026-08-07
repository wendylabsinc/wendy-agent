# `http` Entitlement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a new `http` wendy.json entitlement (`{ "type": "http", "port": N }`) that surfaces an app's declared HTTP port over gRPC as `AppContainer.http_port`, and makes `wendy run` automatically wait for that port and open it in the developer's browser after launch — with no extra config required.

**Architecture:** Mirrors the existing `mcp` entitlement's static, declared-only pattern end to end: appconfig validation → containerd label (Go/Linux agent) or in-memory `WendyAppConfig` (Swift/Mac agent) → `AppContainer.http_port` on `ListContainers` → CLI consumption in `wendy run` (readiness + auto-open) and `wendy device apps` (display).

**Tech Stack:** Go (CLI + Linux agent, containerd), Swift (Mac agent, WendyAgentCore), Protocol Buffers (shared gRPC contract).

## Global Constraints

- Mirror the `mcp` entitlement's shape and behavior everywhere: a single `port` field, static/declared-only (no liveness check), at most one per app.
- Do not modify `network.ports`, `GetContainerPorts`, or any other existing port mechanism.
- Add `http` to the embedded public `go/internal/shared/appconfig/wendy.schema.json`. Do not change `wendy-fleet.schema.json`, which has no app entitlements.
- Do not touch the interactive `apps list` dashboard (`runAppsDashboard`) — only the static/JSON output path.
- Never hand-edit generated protobuf files (`*.pb.go`, `*.pb.swift`, `*.grpc.swift`) — always regenerate via the existing scripts.

## Final-review lifecycle corrections

- Keep full inherited entitlements in every multi-service container create payload, but build separate CLI-private lifecycle configs. Service lifecycle configs include only service-declared HTTP; top-level HTTP/readiness/hooks (including `timeoutSeconds`) run once after all services start.
- Preserve `wendy run --service` behavior by omitting app-level lifecycle actions on subset runs. If both app and service scopes declare HTTP, execute both at their respective scopes.
- Gate automatic success side effects on readiness. A timeout remains non-fatal and explicitly configured multi-service hooks still execute, but `App reachable` and a synthesized HTTP browser open are suppressed. Cancellation suppresses the warning, announcement, and hook.
- Keep explicit `readiness.tcpSocket.port` as the probe. Choose presentation in this order: hostname-templated explicit `openURL`, HTTP entitlement port, readiness TCP port.
- Browser reachability currently requires host networking. Bridge mode supplies outbound NAT only; mesh mappings serve mesh-peer traffic, not host-browser ingress.

---

### Task 1: Proto — add `AppContainer.http_port`

**Files:**
- Modify: `Proto/wendy/agent/services/v1/shared.proto:36-49` (`message AppContainer`)
- Generated (regenerate, do not hand-edit): Go bindings under `go/proto/gen/agentpb/`, Swift bindings under `swift/WendyAgentCore/Sources/WendyAgentGRPC/Proto/wendy/agent/services/v1/`

**Interfaces:**
- Produces: `agentpb.AppContainer.HttpPort uint32` / `GetHttpPort() uint32` (Go), `Wendy_Agent_Services_V1_AppContainer.httpPort: UInt32` (Swift) — the field every later task reads/writes.

- [ ] **Step 1: Add the field to the proto**

In `Proto/wendy/agent/services/v1/shared.proto`, inside `message AppContainer`, after the existing `termination_reason` field (currently the last field, `= 12`):

```proto
    int32 exit_code = 11;            // process exit code of the last run; -1 = task never started.
    string termination_reason = 12;  // exited | crashed | oom_killed | start_failed | entitlement_denied; empty if unknown.
    // wendy.json `http` entitlement's declared port, if any. Static/declared —
    // mirrors mcp_port; 0 when the app declares no `http` entitlement.
    uint32 http_port = 13;
```

- [ ] **Step 2: Regenerate Go bindings**

Run from repo root: `bash scripts/generate-proto.sh`

- [ ] **Step 3: Verify Go generation**

Confirm the generated file now defines the field:

Run: `grep -rn "HttpPort" go/proto/gen/agentpb/`
Expected: at least one match in the generated `shared.pb.go` (or equivalent), e.g. `HttpPort uint32` and a `func (x *AppContainer) GetHttpPort() uint32`.

- [ ] **Step 4: Regenerate Swift bindings**

Run: `bash swift/Scripts/GenerateProto.sh`

- [ ] **Step 5: Verify Swift generation**

Run: `grep -rn "httpPort" swift/WendyAgentCore/Sources/WendyAgentGRPC/`
Expected: at least one match, e.g. `var httpPort: UInt32`.

- [ ] **Step 6: Build both to confirm the regenerated code compiles**

Run: `cd go && go build ./... && cd ../swift && swift build --package-path WendyAgentCore`
Expected: both succeed with no errors.

- [ ] **Step 7: Commit**

```bash
git add Proto/wendy/agent/services/v1/shared.proto go/proto/gen/agentpb swift/WendyAgentCore/Sources/WendyAgentGRPC
git commit -m "proto: add AppContainer.http_port field"
```

---

### Task 2: Go appconfig — `http` entitlement type + validation

**Files:**
- Modify: `go/internal/shared/appconfig/appconfig.go:33-343, 379-453`
- Modify: `go/internal/shared/appconfig/appconfig_test.go` (append near the existing MCP tests, ~line 420, ~503-525, ~1330-1385, ~1595-1690)
- Modify: `go/internal/shared/appconfig/wendy.schema.json`
- Modify: `go/internal/shared/appconfig/schema_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `appconfig.EntitlementHTTP = "http"` (const), reuses existing `Entitlement.Port int` field — both read by Task 3 (containerd), Task 5 (run.go), Task 6 (apps.go).

- [ ] **Step 1: Add the entitlement type constant**

In `go/internal/shared/appconfig/appconfig.go`, in the `EntitlementType enumerates...` const block (currently ending with `EntitlementBuild = "build"` around line 62), add:

```go
	// EntitlementHTTP grants no additional container privileges by itself; it
	// declares the app's primary HTTP port so clients (wendy run, remote
	// management apps) can discover and open it. See entitlements.md.
	EntitlementHTTP = "http"
```

And add `EntitlementHTTP,` to the `ValidEntitlementTypes` slice literal (after `EntitlementBuild,`).

- [ ] **Step 2: Add to `allowedKeys`**

In the `allowedKeys` map literal (~line 92-111), add:

```go
	EntitlementHTTP:          {"type", "port"},
```

- [ ] **Step 3: Update the `Port` field doc comment**

Change (line 338):
```go
	Port      int           `json:"port,omitempty"`      // MCP
```
to:
```go
	Port      int           `json:"port,omitempty"`      // MCP, HTTP
```

- [ ] **Step 4: Add validation in `validateEntitlements`**

In the `switch e.Type` block (~line 392-442), add a case alongside `EntitlementMCP` (order doesn't matter; place it directly after the `EntitlementMCP` case for readability):

```go
		case EntitlementHTTP:
			if e.Port < 1 || e.Port > 65535 {
				return fmt.Errorf("%s[%d]: http port must be between 1 and 65535, got %d", prefix, i, e.Port)
			}
```

- [ ] **Step 5: Add the at-most-one-`http` check**

Directly after the existing `mcpCount` block (~line 445-453), add the same pattern for `http`:

```go
	httpCount := 0
	for _, e := range entitlements {
		if e.Type == EntitlementHTTP {
			httpCount++
		}
	}
	if httpCount > 1 {
		return fmt.Errorf("at most one http entitlement is allowed in %s, found %d", prefix, httpCount)
	}
```

- [ ] **Step 6: Write the failing tests**

Before the Go validation tests, add the public schema's strict `http` entitlement branch: require `type` and `port`, make `port` an integer from 1 through 65535, and set `additionalProperties: false`. Add a structural test that locates that branch and verifies its required fields, bounds, and strictness. Do not modify the fleet schema.

Append to `go/internal/shared/appconfig/appconfig_test.go`, near the existing MCP test functions:

```go
func TestValidateJSON_HTTPNoWarnings(t *testing.T) {
	data := []byte(`{
		"appId": "com.example.app",
		"entitlements": [
			{"type": "http", "port": 8080}
		]
	}`)

	warnings := ValidateJSON(data)
	if len(warnings) != 0 {
		t.Errorf("ValidateJSON() got %d warnings for valid http entitlement, want 0", len(warnings))
	}
}

func TestValidateJSON_HTTPUnknownKeys(t *testing.T) {
	data := []byte(`{
		"appId": "com.example.app",
		"entitlements": [
			{"type": "http", "port": 8080, "typo": 1}
		]
	}`)

	warnings := ValidateJSON(data)
	if len(warnings) == 0 {
		t.Fatal("ValidateJSON() expected warning for unknown key on http entitlement, got none")
	}
}

func TestHTTPEntitlementValid(t *testing.T) {
	cfg := &AppConfig{
		AppID: "test",
		Entitlements: []Entitlement{
			{Type: EntitlementHTTP, Port: 8080},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestHTTPEntitlementPortRequired(t *testing.T) {
	cfg := &AppConfig{
		AppID: "test",
		Entitlements: []Entitlement{
			{Type: EntitlementHTTP, Port: 0},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing port")
	}
	if !strings.Contains(err.Error(), "port") {
		t.Fatalf("expected error to mention port, got: %v", err)
	}
}

func TestHTTPEntitlementDuplicateRejected(t *testing.T) {
	cfg := &AppConfig{
		AppID: "test",
		Entitlements: []Entitlement{
			{Type: EntitlementHTTP, Port: 8080},
			{Type: EntitlementHTTP, Port: 9090},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate http entitlement")
	}
}

func TestHTTPEntitlementPortOutOfRange(t *testing.T) {
	cfg := &AppConfig{
		AppID: "test",
		Entitlements: []Entitlement{
			{Type: EntitlementHTTP, Port: 99999},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for out-of-range port")
	}
}
```

- [ ] **Step 7: Run tests to verify they fail before the implementation, then pass after**

Run: `cd go && go test ./internal/shared/appconfig/... -run 'HTTP' -v`

Since steps 1-5 (implementation) are written before this test step in this plan, run the tests now — expected: PASS. (If you are following strict red-green TDD, comment out steps 1-5 temporarily, confirm the same tests fail with "unknown type" / "no error" messages, then restore steps 1-5.)

- [ ] **Step 8: Run the full appconfig test suite to check for regressions**

Run: `cd go && go test ./internal/shared/appconfig/... -v`
Expected: all tests PASS, including the existing MCP-related and entitlement-count tests.

- [ ] **Step 9: Commit**

```bash
git add go/internal/shared/appconfig/appconfig.go go/internal/shared/appconfig/appconfig_test.go
git commit -m "appconfig: add http entitlement type"
```

---

### Task 3: Go/Linux agent — containerd label for `http_port` + confirm no-op in `ApplyEntitlements`

**Files:**
- Modify: `go/internal/agent/containerd/helpers.go:65-66, ~284-292` (const block, `wendyLabels`)
- Modify: `go/internal/agent/containerd/helpers_test.go` (append near `TestWendyLabels_*`, ~line 266-355)
- Modify: `go/internal/agent/oci/entitlements_test.go` (append; add `"reflect"` to its import block)

**Interfaces:**
- Consumes: `appconfig.EntitlementHTTP`, `appconfig.EntitlementMCP` (Task 2), `wendyLabels(appName, serviceName, version string, restartPolicy *agentpb.RestartPolicy, entitlements []appconfig.Entitlement, isolation string, dependsOn []string) map[string]string` (existing signature, unchanged), `ApplyEntitlements(spec *Spec, cfg *appconfig.AppConfig, opts ApplyOptions) error` / `DefaultSpec(rootfs string, args []string) *Spec` (existing, unchanged).
- Produces: `labelKeyHTTPPort = "sh.wendy/http.port"` constant, consumed by Task 4.

- [ ] **Step 1: Add the label constant**

In `go/internal/agent/containerd/helpers.go`, directly after the existing:
```go
// labelKeyMCPPort stores the MCP server port for containers with an mcp entitlement.
const labelKeyMCPPort = "sh.wendy/mcp.port"
```
add:
```go
// labelKeyHTTPPort stores the HTTP port for containers with an http entitlement.
const labelKeyHTTPPort = "sh.wendy/http.port"
```

- [ ] **Step 2: Wire it into `wendyLabels`**

In `wendyLabels`, directly after the existing MCP loop:
```go
	for _, e := range entitlements {
		if e.Type == appconfig.EntitlementMCP && e.Port > 0 {
			labels[labelKeyMCPPort] = strconv.FormatUint(uint64(e.Port), 10)
			break
		}
	}
```
add:
```go
	for _, e := range entitlements {
		if e.Type == appconfig.EntitlementHTTP && e.Port > 0 {
			labels[labelKeyHTTPPort] = strconv.FormatUint(uint64(e.Port), 10)
			break
		}
	}
```

- [ ] **Step 3: Write the failing tests**

Append to `go/internal/agent/containerd/helpers_test.go`:

```go
func TestWendyLabels_WithHTTPPort(t *testing.T) {
	ents := []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: 8080}}
	labels := wendyLabels("app", "", "1.0", nil, ents, "", nil)
	if v, ok := labels[labelKeyHTTPPort]; !ok {
		t.Error("missing http port label")
	} else if v != "8080" {
		t.Errorf("http port label = %q; want %q", v, "8080")
	}
}

func TestWendyLabels_WithoutHTTPEntitlementOmitsLabel(t *testing.T) {
	labels := wendyLabels("app", "", "1.0", nil, nil, "", nil)
	if v, ok := labels[labelKeyHTTPPort]; ok {
		t.Errorf("should not have http port label when no http entitlement declared, got %q", v)
	}
}

// TestWendyLabels_WithMCPPort closes a pre-existing test gap: wendyLabels has
// written labelKeyMCPPort since the mcp entitlement shipped, but nothing
// asserted it. Added here because this task touches the same loop shape.
func TestWendyLabels_WithMCPPort(t *testing.T) {
	ents := []appconfig.Entitlement{{Type: appconfig.EntitlementMCP, Port: 3000}}
	labels := wendyLabels("app", "", "1.0", nil, ents, "", nil)
	if v, ok := labels[labelKeyMCPPort]; !ok {
		t.Error("missing mcp port label")
	} else if v != "3000" {
		t.Errorf("mcp port label = %q; want %q", v, "3000")
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd go && go test ./internal/agent/containerd/... -run 'WendyLabels' -v`
Expected: all PASS.

- [ ] **Step 5: Confirm `ApplyEntitlements` treats `http` as a no-op**

`ApplyEntitlements`'s switch in `go/internal/agent/oci/entitlements.go:69-120` has no `case appconfig.EntitlementHTTP` (mirroring the pre-existing absence of `case appconfig.EntitlementMCP`), so declaring `http` must not mutate the OCI spec at all. Add `"reflect"` to the import block of `go/internal/agent/oci/entitlements_test.go`, then append:

```go
func TestApplyEntitlements_HTTPIsNoOp(t *testing.T) {
	base := DefaultSpec("/rootfs", []string{"/bin/sh"})
	spec := DefaultSpec("/rootfs", []string{"/bin/sh"})
	cfg := &appconfig.AppConfig{
		AppID: "test-app",
		Entitlements: []appconfig.Entitlement{
			{Type: appconfig.EntitlementHTTP, Port: 8080},
		},
	}
	if err := ApplyEntitlements(spec, cfg, ApplyOptions{}); err != nil {
		t.Fatalf("ApplyEntitlements() error = %v", err)
	}
	if !reflect.DeepEqual(base, spec) {
		t.Errorf("http entitlement mutated the OCI spec; want no-op.\nbase: %+v\nspec: %+v", base, spec)
	}
}
```

Run: `cd go && go test ./internal/agent/oci/... -run 'TestApplyEntitlements_HTTPIsNoOp' -v`
Expected: PASS (both specs come from independent `DefaultSpec` calls with identical inputs, so they must already be deep-equal before `ApplyEntitlements` runs on `spec` — if this fails on the base case, `DefaultSpec` is non-deterministic and the test needs a different comparison; otherwise a failure means `http` incorrectly changed the spec).

- [ ] **Step 6: Commit**

```bash
git add go/internal/agent/containerd/helpers.go go/internal/agent/containerd/helpers_test.go go/internal/agent/oci/entitlements_test.go
git commit -m "containerd: label containers with their http entitlement port"
```

---

### Task 4: Go/Linux agent — populate `AppContainer.HttpPort` in `ListContainers`

**Files:**
- Modify: `go/internal/agent/containerd/client.go:3487-3620` (`Client.ListContainers`)

**Interfaces:**
- Consumes: `labelKeyHTTPPort` (Task 3).
- Produces: `agentpb.AppContainer.HttpPort` populated for real Linux-agent deployments — consumed downstream by the CLI (Task 6) and any gRPC client.

**Note:** `ListContainers` has no existing unit test (it depends on a real/mocked containerd client interface with no test scaffolding in this package today — `mcpPort`'s merge logic, which this mirrors exactly, is likewise untested at the unit level). This task follows that existing precedent; coverage remains E2E-only, consistent with the rest of this function.

- [ ] **Step 1: Add `httpPort` to the internal `entry` struct**

In `ListContainers`, the internal `entry` struct (~line 3499-3506) currently reads:

```go
	type entry struct {
		version      string
		runningState agentpb.AppRunningState
		mcpPort      uint32
		services     []serviceEntry
		exitCode     int32
		exitReason   string // "" until an exit label is seen for this app
	}
```

Change to:

```go
	type entry struct {
		version      string
		runningState agentpb.AppRunningState
		mcpPort      uint32
		httpPort     uint32
		services     []serviceEntry
		exitCode     int32
		exitReason   string // "" until an exit label is seen for this app
	}
```

- [ ] **Step 2: Parse the label per container**

Directly after the existing:
```go
		var mcpPort uint32
		if portStr, ok := info.Labels[labelKeyMCPPort]; ok && portStr != "" {
			if p, err := strconv.ParseUint(portStr, 10, 32); err == nil {
				mcpPort = uint32(p)
			}
		}
```
add:
```go
		var httpPort uint32
		if portStr, ok := info.Labels[labelKeyHTTPPort]; ok && portStr != "" {
			if p, err := strconv.ParseUint(portStr, 10, 32); err == nil {
				httpPort = uint32(p)
			}
		}
```

- [ ] **Step 3: Set it when creating a new grouped entry**

In the `if e, ok := grouped[appID]; !ok { ... }` branch, the `ne := &entry{...}` literal currently reads:
```go
			ne := &entry{
				version:      appVersion,
				runningState: runningState,
				mcpPort:      mcpPort,
				services:     []serviceEntry{svc},
			}
```
Change to:
```go
			ne := &entry{
				version:      appVersion,
				runningState: runningState,
				mcpPort:      mcpPort,
				httpPort:     httpPort,
				services:     []serviceEntry{svc},
			}
```

- [ ] **Step 4: Merge across multi-service containers**

Directly after the existing:
```go
			if mcpPort != 0 && e.mcpPort == 0 {
				e.mcpPort = mcpPort
			}
```
add:
```go
			if httpPort != 0 && e.httpPort == 0 {
				e.httpPort = httpPort
			}
```

- [ ] **Step 5: Set it on the returned `AppContainer`**

The final `ac := &agentpb.AppContainer{...}` literal currently reads:
```go
		ac := &agentpb.AppContainer{
			AppName:      appID,
			AppVersion:   e.version,
			RunningState: e.runningState,
			McpPort:      e.mcpPort,
			Services:     services,
		}
```
Change to:
```go
		ac := &agentpb.AppContainer{
			AppName:      appID,
			AppVersion:   e.version,
			RunningState: e.runningState,
			McpPort:      e.mcpPort,
			HttpPort:     e.httpPort,
			Services:     services,
		}
```

- [ ] **Step 6: Build to confirm correctness**

Run: `cd go && go build ./...`
Expected: succeeds with no errors (this task has no dedicated test per the note above; the build is the only verification available in this package for this function).

- [ ] **Step 7: Commit**

```bash
git add go/internal/agent/containerd/client.go
git commit -m "containerd: populate AppContainer.HttpPort in ListContainers"
```

---

### Task 5: `wendy run` — readiness default + auto-open from `http` entitlement

Final behavior note: an explicit readiness TCP port remains the probe even when HTTP is declared. Announcement/opening prefers a hostname-templated explicit `openURL`, then the HTTP port, then the readiness port. The multi-service paths use separately scoped lifecycle configs and suppress automatic success side effects after a failed readiness probe.

**Files:**
- Modify: `go/internal/cli/commands/network_format.go` (add helpers near `bestReachableIP`/`reachableAppURL`)
- Modify: `go/internal/cli/commands/run.go:1942-1978` (`runPostStartIfReady`, `announceReachableURL`)
- Modify: `go/internal/cli/commands/run_hooks_test.go` (append new tests)

**Interfaces:**
- Consumes: `appconfig.EntitlementHTTP`, `Entitlement.Port` (Task 2); `appconfig.AppConfig.Entitlements`, `.Readiness`, `.Hooks` (existing).
- Produces: `httpEntitlementPort(entitlements []appconfig.Entitlement) (int, bool)`, `effectiveReadiness(appCfg *appconfig.AppConfig) *appconfig.ReadinessConfig` — both used only within this task's own call sites.

- [ ] **Step 1: Add `httpEntitlementPort` and `effectiveReadiness` to `network_format.go`**

Add near the end of `go/internal/cli/commands/network_format.go` (after `reachableAppURL`):

```go
// httpEntitlementPort returns the port declared by an `http` entitlement, if
// the app has one.
func httpEntitlementPort(entitlements []appconfig.Entitlement) (int, bool) {
	for _, e := range entitlements {
		if e.Type == appconfig.EntitlementHTTP && e.Port > 0 {
			return e.Port, true
		}
	}
	return 0, false
}

// effectiveReadiness returns appCfg.Readiness unchanged when it already
// declares a TCP probe. Otherwise, when the app declares an `http`
// entitlement, it synthesizes a readiness probe on that port (preserving any
// explicit timeoutSeconds) so `wendy run` waits for the declared HTTP port to
// come up before announcing or opening it — an `http` entitlement gets this
// gating without a separate readiness.tcpSocket config.
func effectiveReadiness(appCfg *appconfig.AppConfig) *appconfig.ReadinessConfig {
	if appCfg.Readiness != nil && appCfg.Readiness.TCPSocket != nil {
		return appCfg.Readiness
	}
	port, ok := httpEntitlementPort(appCfg.Entitlements)
	if !ok {
		return appCfg.Readiness
	}
	timeout := 0
	if appCfg.Readiness != nil {
		timeout = appCfg.Readiness.TimeoutSeconds
	}
	return &appconfig.ReadinessConfig{
		TCPSocket:      &appconfig.TCPSocketProbe{Port: port},
		TimeoutSeconds: timeout,
	}
}
```

- [ ] **Step 2: Use `effectiveReadiness` in `runPostStartIfReady`**

In `go/internal/cli/commands/run.go`, `runPostStartIfReady` currently opens with:
```go
func runPostStartIfReady(ctx, hookCtx context.Context, conn *grpcclient.AgentConnection, appCfg *appconfig.AppConfig) *exec.Cmd {
	if err := waitForReadiness(ctx, appCfg.Readiness, conn.Host); err != nil {
```
Change the `waitForReadiness` call to:
```go
	readiness := effectiveReadiness(appCfg)
	if err := waitForReadiness(ctx, readiness, conn.Host); err != nil {
```

- [ ] **Step 3: Use `effectiveReadiness` in `announceReachableURL`**

`announceReachableURL` currently reads:
```go
	hasPort := appCfg.Readiness != nil && appCfg.Readiness.TCPSocket != nil && appCfg.Readiness.TCPSocket.Port != 0
	if hookURL == "" && !hasPort {
		return ""
	}

	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return ""
	}
	ip := bestReachableIP(resp.GetNetworkInterfaces())
	url := reachableAppURL(hookURL, appCfg.AppID, appCfg.ServiceName, ip, appCfg.Readiness)
```
Change to:
```go
	readiness := effectiveReadiness(appCfg)
	hasPort := readiness != nil && readiness.TCPSocket != nil && readiness.TCPSocket.Port != 0
	if hookURL == "" && !hasPort {
		return ""
	}

	resp, err := conn.AgentService.GetAgentVersion(ctx, &agentpb.GetAgentVersionRequest{})
	if err != nil {
		return ""
	}
	ip := bestReachableIP(resp.GetNetworkInterfaces())
	url := reachableAppURL(hookURL, appCfg.AppID, appCfg.ServiceName, ip, readiness)
```

- [ ] **Step 4: Auto-open without requiring an explicit `postStart.openURL`**

Add a new helper directly above `runPostStartIfReady` in `run.go`:

```go
// synthesizedOpenURLHook returns appCfg.Hooks unchanged when the app already
// configures an explicit postStart action (openURL, cli, or agent). Otherwise,
// when the app declares an `http` entitlement, it returns a synthetic
// HooksConfig whose postStart opens that port automatically in the browser —
// an `http` entitlement needs no separate hooks.postStart config to get
// `wendy run`'s auto-open behavior.
func synthesizedOpenURLHook(appCfg *appconfig.AppConfig) *appconfig.HooksConfig {
	if appCfg.Hooks != nil && appCfg.Hooks.PostStart != nil {
		p := appCfg.Hooks.PostStart
		if p.OpenURL != "" || p.CLI != "" || p.Agent != "" {
			return appCfg.Hooks
		}
	}
	port, ok := httpEntitlementPort(appCfg.Entitlements)
	if !ok {
		return appCfg.Hooks
	}
	return &appconfig.HooksConfig{
		PostStart: &appconfig.HookCommand{
			OpenURL: fmt.Sprintf("http://${WENDY_HOSTNAME}:%d", port),
		},
	}
}
```

Then change `runPostStartIfReady`'s final line from:
```go
	return startPostStartHook(hookCtx, appCfg, hookHost, appCfg.ServiceName)
```
to:
```go
	effectiveCfg := appCfg
	if hooks := synthesizedOpenURLHook(appCfg); hooks != appCfg.Hooks {
		clone := *appCfg
		clone.Hooks = hooks
		effectiveCfg = &clone
	}
	return startPostStartHook(hookCtx, effectiveCfg, hookHost, appCfg.ServiceName)
```

(`AppConfig` is a plain value struct — `clone := *appCfg` is a safe shallow copy since only the top-level `Hooks` pointer field is being swapped, not mutated in place.)

- [ ] **Step 5: Write the failing tests**

Append to `go/internal/cli/commands/run_hooks_test.go`:

```go
func TestEffectiveReadiness_ExplicitReadinessWins(t *testing.T) {
	appCfg := &appconfig.AppConfig{
		Readiness:    &appconfig.ReadinessConfig{TCPSocket: &appconfig.TCPSocketProbe{Port: 1234}},
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: 8080}},
	}
	got := effectiveReadiness(appCfg)
	if got.TCPSocket.Port != 1234 {
		t.Errorf("effectiveReadiness().TCPSocket.Port = %d, want explicit 1234", got.TCPSocket.Port)
	}
}

func TestEffectiveReadiness_SynthesizedFromHTTPEntitlement(t *testing.T) {
	appCfg := &appconfig.AppConfig{
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: 8080}},
	}
	got := effectiveReadiness(appCfg)
	if got == nil || got.TCPSocket == nil || got.TCPSocket.Port != 8080 {
		t.Fatalf("effectiveReadiness() = %+v, want synthesized TCPSocket on port 8080", got)
	}
}

func TestEffectiveReadiness_NoHTTPEntitlementNoReadiness(t *testing.T) {
	appCfg := &appconfig.AppConfig{}
	got := effectiveReadiness(appCfg)
	if got != nil {
		t.Errorf("effectiveReadiness() = %+v, want nil", got)
	}
}

// TestRunPostStartIfReady_AutoOpensFromHTTPEntitlement verifies the "wendy run
// also opens this page after launch" behavior: an app declaring only an http
// entitlement (no readiness config, no postStart hook) gets its port polled
// for readiness and then opened automatically.
func TestRunPostStartIfReady_AutoOpensFromHTTPEntitlement(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:        "http-app",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: port}},
	}
	conn := &grpcclient.AgentConnection{
		Host: "127.0.0.1",
		AgentService: &fakeAgentVersionClient{resp: &agentpb.GetAgentVersionResponse{
			NetworkInterfaces: []*agentpb.NetworkInterface{{Name: "eth0", IpAddresses: []string{"127.0.0.1"}}},
		}},
	}

	runPostStartIfReady(context.Background(), context.Background(), conn, appCfg)
	want := fmt.Sprintf("http://127.0.0.1:%d", port)
	if opened != want {
		t.Errorf("opened = %q, want %q", opened, want)
	}
}

// TestRunPostStartIfReady_ExplicitHookNotOverriddenByHTTPEntitlement verifies
// that an app declaring both an http entitlement AND an explicit
// hooks.postStart.openURL keeps the explicit URL — the entitlement only fills
// in when nothing is configured.
func TestRunPostStartIfReady_ExplicitHookNotOverriddenByHTTPEntitlement(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to start listener: %v", err)
	}
	defer ln.Close()
	port := testPort(t, ln)

	original := browserOpen
	t.Cleanup(func() { browserOpen = original })
	var opened string
	browserOpen = func(url string) error {
		opened = url
		return nil
	}

	appCfg := &appconfig.AppConfig{
		AppID:        "http-app-explicit",
		Entitlements: []appconfig.Entitlement{{Type: appconfig.EntitlementHTTP, Port: port}},
		Hooks: &appconfig.HooksConfig{
			PostStart: &appconfig.HookCommand{OpenURL: "http://${WENDY_HOSTNAME}:9999/custom"},
		},
	}
	conn := &grpcclient.AgentConnection{Host: "127.0.0.1"}

	runPostStartIfReady(context.Background(), context.Background(), conn, appCfg)
	if opened != "http://127.0.0.1:9999/custom" {
		t.Errorf("opened = %q, want the explicit hook URL unchanged", opened)
	}
}
```

- [ ] **Step 6: Run the new and existing tests**

Run: `cd go && go test ./internal/cli/commands/... -run 'ReadinessIfReady|EffectiveReadiness|RunPostStartIfReady' -v`
Expected: all PASS, including the pre-existing `TestRunPostStartIfReady_*` tests (they must still pass unmodified — `effectiveReadiness` is a no-op passthrough when an explicit `readiness.tcpSocket` or no `http` entitlement is present).

- [ ] **Step 7: Run the full commands package test suite to check for regressions**

Run: `cd go && go test ./internal/cli/commands/... -v 2>&1 | tail -100`
Expected: no new failures.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/network_format.go go/internal/cli/commands/run.go go/internal/cli/commands/run_hooks_test.go
git commit -m "cli: wendy run auto-opens the declared http entitlement port"
```

---

### Task 6: `wendy device apps` — display `httpPort`

**Files:**
- Modify: `go/internal/cli/commands/apps.go:113-227` (`appsListAgent`)
- Test: `go/internal/cli/commands/apps_test.go` (create if it doesn't already exist, or append to it if it does — check first)

**Interfaces:**
- Consumes: `agentpb.AppContainer.GetHttpPort() uint32` (Task 1/4).

- [ ] **Step 1: Check whether `apps_test.go` exists**

Run: `ls go/internal/cli/commands/apps_test.go 2>/dev/null || echo "does not exist"`

If it doesn't exist, Step 5 below creates it fresh; if it does, read it first to match its existing helper/mock conventions before appending.

- [ ] **Step 2: Add `HTTPPort` to the JSON struct and populate it**

In `appsListAgent`, the `jsonApp` struct (~line 147-155) currently reads:
```go
		type jsonApp struct {
			Name              string        `json:"name"`
			Version           string        `json:"version,omitempty"`
			RunningState      string        `json:"runningState,omitempty"`
			FailureCount      uint32        `json:"failureCount,omitempty"`
			ExitCode          *int32        `json:"exitCode,omitempty"` // pointer so a clean exit 0 is still emitted alongside terminationReason
			TerminationReason string        `json:"terminationReason,omitempty"`
			Services          []jsonService `json:"services,omitempty"`
		}
```
Add an `HTTPPort` field:
```go
		type jsonApp struct {
			Name              string        `json:"name"`
			Version           string        `json:"version,omitempty"`
			RunningState      string        `json:"runningState,omitempty"`
			FailureCount      uint32        `json:"failureCount,omitempty"`
			ExitCode          *int32        `json:"exitCode,omitempty"` // pointer so a clean exit 0 is still emitted alongside terminationReason
			TerminationReason string        `json:"terminationReason,omitempty"`
			HTTPPort          uint32        `json:"httpPort,omitempty"`
			Services          []jsonService `json:"services,omitempty"`
		}
```
And in the `apps[i] = jsonApp{...}` literal, add `HTTPPort: c.GetHttpPort(),`.

- [ ] **Step 3: Add a "Port" column to the static table**

Change the `headers` line (~line 192) from:
```go
	headers := []string{"", "Name", "Version", "Failures", "Reason"}
```
to:
```go
	headers := []string{"", "Name", "Version", "Port", "Failures", "Reason"}
```

Update each of the three `rows = append(rows, []string{...})` literals in the loop to insert the port value in the same column position (right after `c.GetAppVersion()` / the blank version slot):

Group header row:
```go
			rows = append(rows, []string{
				stateIcon(c.GetRunningState().String()),
				c.GetAppName() + " " + lipgloss.NewStyle().Foreground(tui.ColorDim).Render("[group]"),
				c.GetAppVersion(),
				httpPortColumn(c.GetHttpPort()),
				fmt.Sprintf("%d", c.GetFailureCount()),
				terminationSummary(c.GetTerminationReason(), c.GetExitCode()),
			})
```
Per-service sub-row (add one more empty string):
```go
				rows = append(rows, []string{
					"",
					"  ↳ " + s.GetName() + "  " + stateIcon(s.GetRunningState().String()),
					"",
					"",
					"",
					"",
				})
```
Single-container row:
```go
			rows = append(rows, []string{
				stateIcon(c.GetRunningState().String()),
				c.GetAppName(),
				c.GetAppVersion(),
				httpPortColumn(c.GetHttpPort()),
				fmt.Sprintf("%d", c.GetFailureCount()),
				terminationSummary(c.GetTerminationReason(), c.GetExitCode()),
			})
```

- [ ] **Step 4: Add the `httpPortColumn` helper**

Add directly above `terminationSummary` (~line 229):
```go
// httpPortColumn renders an app's declared http entitlement port for the
// apps-list table, or "" when the app declares none.
func httpPortColumn(port uint32) string {
	if port == 0 {
		return ""
	}
	return fmt.Sprintf(":%d", port)
}
```

- [ ] **Step 5: Write a failing test**

If `go/internal/cli/commands/apps_test.go` does not exist, create it:

```go
package commands

import (
	"testing"
)

func TestHTTPPortColumn_Zero(t *testing.T) {
	if got := httpPortColumn(0); got != "" {
		t.Errorf("httpPortColumn(0) = %q, want empty string", got)
	}
}

func TestHTTPPortColumn_NonZero(t *testing.T) {
	if got := httpPortColumn(8080); got != ":8080" {
		t.Errorf("httpPortColumn(8080) = %q, want %q", got, ":8080")
	}
}
```

If it already exists, append these two test functions to it instead (do not create a duplicate file).

- [ ] **Step 6: Run the tests**

Run: `cd go && go test ./internal/cli/commands/... -run 'HTTPPortColumn' -v`
Expected: both PASS.

- [ ] **Step 7: Build to confirm the table/JSON changes compile end to end**

Run: `cd go && go build ./...`
Expected: succeeds.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/commands/apps.go go/internal/cli/commands/apps_test.go
git commit -m "cli: show declared http port in wendy device apps"
```

---

### Task 7: Docs — document the `http` entitlement

**Files:**
- Modify: `go/internal/cli/assets/docs/apps/wendy.json.md` (near the existing `### mcp` section)
- Modify: `go/internal/cli/assets/docs/apps/wendy-services.md`
- Modify: `go/internal/cli/assets/docs/apps/compose.md`

**Interfaces:** none (documentation only).

- [ ] **Step 1: Add the `http` section**

Directly after the existing `mcp` section (which ends around the line containing `> **Note:** The \`mcp\` entitlement is typically combined with...`), add:

```markdown
### `http`

Declares the app's primary HTTP port. The agent reports it over gRPC (`AppContainer.http_port`) for any client — including `wendy device apps` and remote management apps — to discover and open. `wendy run` uses it automatically: it waits for the port to accept connections before printing "App reachable at ..." and opens it in your default browser, with no extra `hooks.postStart` configuration required.

```json
{ "type": "http", "port": 8080 }
```

> **Networking:** Browser reachability currently requires `{ "type": "network", "mode": "host" }`. Bridge mode provides outbound NAT only and does not publish host/LAN ports. Mesh port mappings serve mesh-peer traffic rather than developer-browser ingress.
```

Also document app-level versus service-level HTTP lifecycle scope, subset-run behavior, readiness-failure suppression, and the case where readiness probes `9000` while HTTP presents `8080`.

- [ ] **Step 2: Spot-check rendering**

Run: `grep -n "^### \`http\`" go/internal/cli/assets/docs/apps/wendy.json.md`
Expected: one match, immediately after the `mcp` section.

- [ ] **Step 3: Commit**

```bash
git add go/internal/cli/assets/docs/apps/wendy.json.md go/internal/cli/assets/docs/apps/wendy-services.md go/internal/cli/assets/docs/apps/compose.md
git commit -m "docs: document the http entitlement"
```

---

### Task 8: Swift — add `port` to `WendyEntitlement`

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Services/OCITypes.swift:60-66`
- Modify: `swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift:8, 16, 30, 44`
- Modify: `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLIBackendTests.swift:11, 18`
- Modify: `swift/WendyAgentCore/Tests/WendyAgentTests/DockerContainerBackendTests.swift:11, 18`

**Interfaces:**
- Produces: `WendyEntitlement.port: Int?` — consumed by Task 9 and Task 10.

**Note:** `WendyEntitlement` has no custom initializer (Swift's synthesized memberwise init), and every existing call site passes all current parameters explicitly by label in declaration order — appending `port` as the last property means every call site needs `, port: nil` (or a real value) appended, in the same style as the other explicitly-passed `nil`s.

- [ ] **Step 1: Add the field**

In `swift/WendyAgentCore/Sources/WendyAgent/Services/OCITypes.swift`, change:
```swift
struct WendyEntitlement: Codable, Equatable {
    let type: String
    let mode: String?
    let name: String?
    let path: String?
    let ports: [WendyPortMapping]?
}
```
to:
```swift
struct WendyEntitlement: Codable, Equatable {
    let type: String
    let mode: String?
    let name: String?
    let path: String?
    let ports: [WendyPortMapping]?
    let port: Int?
}
```

- [ ] **Step 2: Update all 8 existing construction call sites**

In `swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift`, update all four `WendyEntitlement(...)` literals (lines 8, 16, 30, 44) to append `, port: nil` as the last argument. For example, line 8:
```swift
            WendyEntitlement(type: "network", mode: "none", name: nil, path: nil, ports: nil)
```
becomes:
```swift
            WendyEntitlement(type: "network", mode: "none", name: nil, path: nil, ports: nil, port: nil)
```
And the multi-line ones (lines 16, 30, 44) get a trailing `,\n                port: nil` added after their `ports:` argument, e.g. line 16-23:
```swift
            WendyEntitlement(
                type: "network",
                mode: nil,
                name: nil,
                path: nil,
                ports: [WendyPortMapping(host: 8080, container: 80)]
            )
```
becomes:
```swift
            WendyEntitlement(
                type: "network",
                mode: nil,
                name: nil,
                path: nil,
                ports: [WendyPortMapping(host: 8080, container: 80)],
                port: nil
            )
```

Apply the same pattern to lines 30 and 44.

In `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLIBackendTests.swift`, both `WendyEntitlement(...)` literals (lines 11 and 18) get `,\n                    port: nil` appended after their `ports:` line, matching the multi-line style shown above.

In `swift/WendyAgentCore/Tests/WendyAgentTests/DockerContainerBackendTests.swift`, both `WendyEntitlement(...)` literals (lines 11 and 18) get the same treatment.

- [ ] **Step 3: Build the test target to confirm every call site compiles**

Run: `cd swift && swift build --package-path WendyAgentCore --build-tests`
Expected: succeeds with no errors (a missed call site fails here with "missing argument for parameter 'port' in call").

- [ ] **Step 4: Run the existing test suites to confirm no behavior changed**

Run: `cd swift && swift test --package-path WendyAgentCore --filter LinuxRunSpecTests`
Run: `cd swift && swift test --package-path WendyAgentCore --filter ContainerCLIBackendTests`
Run: `cd swift && swift test --package-path WendyAgentCore --filter DockerContainerBackendTests`
Expected: all PASS, unchanged from before this task (adding an unused `nil` field changes no behavior).

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Services/OCITypes.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/ContainerCLIBackendTests.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/DockerContainerBackendTests.swift
git commit -m "swift: add port field to WendyEntitlement"
```

---

### Task 9: Swift — no-op `http`/`mcp` entitlement types in `LinuxRunSpecBuilder`

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Containers/LinuxContainerBackend.swift:50-69` (`LinuxRunSpecBuilder.specs`)
- Modify: `swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift` (append)

**Interfaces:**
- Consumes: `WendyEntitlement.type` (existing), `.port` (Task 8, unused here but present on the struct).
- Produces: no spec, no warning, for `"http"`/`"mcp"` entitlement types — matching the Go OCI `ApplyEntitlements` behavior (Go: `entitlements.go`'s switch has no `case EntitlementMCP`/`EntitlementHTTP` at all, i.e. no-op).

- [ ] **Step 1: Write the failing test**

Append to `swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift`:

```swift
    @Test func httpEntitlementProducesNoSpecAndNoWarning() {
        var warnings: [String] = []
        let ents = [
            WendyEntitlement(type: "http", mode: nil, name: nil, path: nil, ports: nil, port: 8080)
        ]
        let specs = LinuxRunSpecBuilder.specs(
            from: ents,
            appName: "app",
            warn: { warnings.append($0) }
        )
        #expect(specs.isEmpty)
        #expect(warnings.isEmpty)
    }

    @Test func mcpEntitlementProducesNoSpecAndNoWarning() {
        var warnings: [String] = []
        let ents = [
            WendyEntitlement(type: "mcp", mode: nil, name: nil, path: nil, ports: nil, port: 3000)
        ]
        let specs = LinuxRunSpecBuilder.specs(
            from: ents,
            appName: "app",
            warn: { warnings.append($0) }
        )
        #expect(specs.isEmpty)
        #expect(warnings.isEmpty)
    }
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd swift && swift test --package-path WendyAgentCore --filter LinuxRunSpecTests`
Expected: FAIL — both new tests report a non-empty `warnings` array (`"Unknown entitlement type 'http'"` / `'mcp'`), since neither case exists yet in the switch.

- [ ] **Step 3: Add the no-op cases**

In `swift/WendyAgentCore/Sources/WendyAgent/Containers/LinuxContainerBackend.swift`, in `LinuxRunSpecBuilder.specs`'s switch, add directly before the `case let type where unsupportedHardwareTypes.contains(type):` branch:

```swift
            case "http", "mcp":
                // Metadata-only entitlements: the port is read directly from the
                // app's retained WendyAppConfig at listContainers time (see
                // ContainerService.listContainers), not applied to the container's
                // run spec. Mirrors the Go agent, where ApplyEntitlements has no
                // case for either type either.
                break
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd swift && swift test --package-path WendyAgentCore --filter LinuxRunSpecTests`
Expected: all PASS, including the two new tests and all pre-existing ones.

- [ ] **Step 5: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Containers/LinuxContainerBackend.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/LinuxRunSpecTests.swift
git commit -m "swift: http and mcp entitlements are run-spec no-ops"
```

---

### Task 10: Swift — populate `httpPort`/`mcpPort` in `ContainerService.listContainers`

**Files:**
- Modify: `swift/WendyAgentCore/Sources/WendyAgent/Services/ContainerService.swift:1131-1147` (`listContainers`)
- Modify: `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerServiceLinuxTests.swift` (append)

**Interfaces:**
- Consumes: `appsByID: [String: WendyApp]` (existing actor-private property, accessible from within `ContainerService`'s own methods), `WendyApp.container?.appConfig?.entitlements: [WendyEntitlement]?` (existing), `WendyEntitlement.type`/`.port` (Task 8).
- Produces: `AppContainer.httpPort` / `.mcpPort` populated for Mac-agent-managed containers — closes the pre-existing Mac-side gap where neither was ever set.

- [ ] **Step 1: Write the failing test**

Append to `swift/WendyAgentCore/Tests/WendyAgentTests/ContainerServiceLinuxTests.swift`, inside `@Suite struct ContainerServiceLinuxTests`:

```swift
    @Test func listContainersReportsHTTPAndMCPPortsFromEntitlements() async throws {
        let backend = FakeLinuxBackend()
        let service = ContainerService(
            broadcaster: TelemetryBroadcaster(),
            executablePath: "/usr/bin/true",
            stateDirectory: FileManager.default.temporaryDirectory
                .appendingPathComponent("cs-\(UUID().uuidString)"),
            linuxBackend: backend
        )
        let config = WendyAppConfig(
            appId: "svc-ports",
            platform: "linux/arm64",
            entitlements: [
                WendyEntitlement(type: "http", mode: nil, name: nil, path: nil, ports: nil, port: 8080),
                WendyEntitlement(type: "mcp", mode: nil, name: nil, path: nil, ports: nil, port: 3000),
            ],
            brewfile: nil
        )
        let configData = try JSONEncoder().encode(config)

        var createReq = Wendy_Agent_Services_V1_CreateContainerRequest()
        createReq.appName = "svc-ports"
        createReq.imageName = "localhost:5555/svc-ports:latest"
        createReq.appConfig = configData
        _ = try await service.createContainer(
            request: ServerRequest(metadata: [:], message: createReq),
            context: makeServerContext(method: "CreateContainer")
        )

        let listResponse = try await service.listContainers(
            request: ServerRequest(metadata: [:], message: Wendy_Agent_Services_V1_ListContainersRequest()),
            context: makeServerContext(method: "ListContainers")
        )
        let contents = try listResponse.accepted.get()
        let writer = CollectingWriter<Wendy_Agent_Services_V1_ListContainersResponse>()
        _ = try await contents.producer(RPCWriter(wrapping: writer))
        let containers = writer.snapshot().compactMap(\.container)

        let svc = try #require(containers.first { $0.appName == "svc-ports" })
        #expect(svc.httpPort == 8080)
        #expect(svc.mcpPort == 3000)
    }
```

(This is the exact same `.accepted.get()` → `contents.producer(RPCWriter(wrapping: writer))` → `writer.snapshot()` pattern `createThenStartPullsAndRunsViaBackend` already uses in this file for `startContainer`'s streaming response, and that `ContainerServiceTests.swift`'s own private `listContainers(service:)` helper uses for this exact RPC — confirmed via `CollectingWriter<Wendy_Agent_Services_V1_ListContainersResponse>()` already appearing there.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd swift && swift test --package-path WendyAgentCore --filter ContainerServiceLinuxTests`
Expected: FAIL — `svc.httpPort == 0` and `svc.mcpPort == 0` (neither is populated yet).

- [ ] **Step 3: Implement the population logic**

In `swift/WendyAgentCore/Sources/WendyAgent/Services/ContainerService.swift`, `listContainers` currently reads:

```swift
    func listContainers(
        request: ServerRequest<Wendy_Agent_Services_V1_ListContainersRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Wendy_Agent_Services_V1_ListContainersResponse> {
        let apps = self.currentAppInfos()
        return StreamingServerResponse { writer in
            for app in apps {
                var container = AppContainer()
                container.appName = app.id
                container.runningState = app.status == .running ? .running : .stopped

                var response = Wendy_Agent_Services_V1_ListContainersResponse()
                response.container = container
                try await writer.write(response)
            }

            return Metadata()
        }
    }
```

Change to:

```swift
    func listContainers(
        request: ServerRequest<Wendy_Agent_Services_V1_ListContainersRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Wendy_Agent_Services_V1_ListContainersResponse> {
        let apps = self.currentAppInfos()
        // Capture entitlement-derived ports while still on the actor: appsByID
        // is actor-isolated state, but the StreamingServerResponse closure below
        // is not guaranteed to run on the actor's executor.
        let ports: [String: (http: UInt32, mcp: UInt32)] = Dictionary(
            uniqueKeysWithValues: apps.map { app in
                (app.id, self.entitlementPorts(forAppID: app.id))
            }
        )
        return StreamingServerResponse { writer in
            for app in apps {
                var container = AppContainer()
                container.appName = app.id
                container.runningState = app.status == .running ? .running : .stopped
                if let (http, mcp) = ports[app.id] {
                    container.httpPort = http
                    container.mcpPort = mcp
                }

                var response = Wendy_Agent_Services_V1_ListContainersResponse()
                response.container = container
                try await writer.write(response)
            }

            return Metadata()
        }
    }

    /// Reads the http/mcp entitlement ports declared in the app's retained
    /// wendy.json config, if it has one (native/file-sync apps have no
    /// `.container` metadata and no entitlements, so both are 0 for them).
    private func entitlementPorts(forAppID appID: String) -> (http: UInt32, mcp: UInt32) {
        guard let entitlements = appsByID[appID]?.container?.appConfig?.entitlements else {
            return (0, 0)
        }
        var http: UInt32 = 0
        var mcp: UInt32 = 0
        for entitlement in entitlements {
            guard let port = entitlement.port, port > 0 else { continue }
            switch entitlement.type {
            case "http": http = UInt32(port)
            case "mcp": mcp = UInt32(port)
            default: break
            }
        }
        return (http, mcp)
    }
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd swift && swift test --package-path WendyAgentCore --filter ContainerServiceLinuxTests`
Expected: all PASS, including the pre-existing `createThenStartPullsAndRunsViaBackend` test.

- [ ] **Step 5: Run the full WendyAgentCore test suite to check for regressions**

Run: `cd swift && swift test --package-path WendyAgentCore`
Expected: no new failures.

- [ ] **Step 6: Commit**

```bash
git add swift/WendyAgentCore/Sources/WendyAgent/Services/ContainerService.swift \
        swift/WendyAgentCore/Tests/WendyAgentTests/ContainerServiceLinuxTests.swift
git commit -m "swift: populate httpPort/mcpPort in ContainerService.listContainers"
```

---

## Post-plan verification

- [ ] Run the full Go suite: `cd go && go test ./...`
- [ ] Run the full Swift suite: `cd swift && swift test --package-path WendyAgentCore`
- [ ] Manually exercise, on a real device if available: create a small app with `wendy.json` declaring `{ "type": "http", "port": <N> }` and `{ "type": "network", "mode": "host" }`, run `wendy run`, and confirm the browser opens automatically once the app's server is listening. This is hardware/E2E verification, out of scope for the automated tests above (consistent with how most entitlement additions in this repo ship CI-green with hardware verification following separately).
