# MCP Agent-Support Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `wendy mcp serve` server materially better for AI/coding agents: safety annotations, machine-readable errors, structured (parseable) results, output bounding, progress on long ops, visible proxy diagnostics, workflow prompts, richer descriptions, and resolving the dead `filesync_sync` tool.

**Architecture:** The MCP server lives in `go/internal/cli/mcp/` (~40 hand-written tools over `github.com/mark3labs/mcp-go v0.54.0`, stdio transport). We do **not** rewrite it into a DSL — we follow the existing functional-options idiom (`mcpgo.NewTool(name, opts...)`) and introduce a small set of shared helpers (`results.go`, `errors.go`, `annotations.go`, `progress.go`, `diagnostics.go`) that make the cross-cutting concerns consistent and hard to omit. Handlers keep their current signatures.

**Tech Stack:** Go, `mark3labs/mcp-go` v0.54.0, gRPC (`google.golang.org/grpc`), `google.golang.org/grpc/status`, Go standard `testing`.

## Global Constraints

- Module path: `github.com/wendylabsinc/wendy/go`; MCP package import prefix `mcpgo "github.com/mark3labs/mcp-go/mcp"`, `"github.com/mark3labs/mcp-go/server"`.
- Transport stays **stdio only** (`server.ServeStdio`). HTTP/SSE is explicitly out of scope (separate future design).
- No new third-party dependencies. `mcp-go` stays pinned at v0.54.0.
- Backward compatibility: existing tool **names and parameters must keep working**. Any renamed parameter keeps its old name as an accepted alias.
- Every result an agent can receive carries a **text fallback** (`Content[0]` `TextContent`) in addition to any structured content, because not all MCP hosts render `structuredContent`. Existing tests read `result.Content[0].(mcpgo.TextContent).Text` — that must keep working.
- Run gofmt on every changed file before committing. Baseline (`go test ./internal/cli/mcp/...`) is green today; keep it green after every task.
- Test invocation from `go/`: `go test ./internal/cli/mcp/... -run <Name> -v`.

---

## Delivery Staging

- **PR1 — Foundation (this plan, full detail):** shared helpers + apply across all tools. Items #1 (annotations), #2 (structured content), #6 (error codes), #9 (shared helpers / de-boilerplate). Tasks 1–9 below.
- **PR2 — Reliability (roadmap locked, §Appendix A):** #3 (output bounding), #4 (progress notifications), #5 (proxy diagnostics).
- **PR3 — Discoverability (roadmap locked, §Appendix B):** #7 (prompts), #8 (descriptions), #10 (filesync resolution).

Each PR is independently mergeable and leaves tests green. Write the PR2 and PR3 step-level plans (same format) when starting them; their design is fixed in the appendices.

---

## File Structure (PR1)

- Create: `go/internal/cli/mcp/errors.go` — `errorCode` type, constants, `errResult`, `errResultf`, `codeFromGRPC`.
- Create: `go/internal/cli/mcp/errors_test.go`
- Create: `go/internal/cli/mcp/results.go` — `okResult` (structured + JSON text fallback), `okText`.
- Create: `go/internal/cli/mcp/results_test.go`
- Create: `go/internal/cli/mcp/annotations.go` — annotation option bundles (`readOnly`, `destructive`, `idempotent`, `openWorld` helpers returning `[]mcpgo.ToolOption`).
- Create: `go/internal/cli/mcp/annotations_test.go`
- Modify: `server.go` — keep `errNotConnected`/`grpcErrString` (now delegating to new helpers); no signature changes.
- Modify: every `tools_*.go` — add annotation options to each `NewTool`; replace `json.MarshalIndent(...)+NewToolResultText` with `okResult`; replace `NewToolResultError(...)` with `errResult(code, ...)`.

## Design decisions locked (PR1)

### Error-code taxonomy (`errors.go`)

```go
type errorCode string

const (
	errCodeNotConnected     errorCode = "NOT_CONNECTED"
	errCodeInvalidArgument  errorCode = "INVALID_ARGUMENT"
	errCodeDeviceUnreachable errorCode = "DEVICE_UNREACHABLE"
	errCodeEntitlementDenied errorCode = "ENTITLEMENT_DENIED"
	errCodeMultipleSessions errorCode = "MULTIPLE_SESSIONS"
	errCodeNotFound         errorCode = "NOT_FOUND"
	errCodeTimeout          errorCode = "TIMEOUT"
	errCodeUnsupported      errorCode = "UNSUPPORTED"
	errCodeInternal         errorCode = "INTERNAL"
)
```

An error result carries structured content `{"error_code": "<CODE>", "message": "<msg>"}` **and** a text fallback `"[CODE] msg"`, with `IsError = true`. This preserves the current human-readable behavior (existing tests match substrings of the message) while adding a machine-readable code.

```go
func errResult(code errorCode, msg string) *mcpgo.CallToolResult {
	r := mcpgo.NewToolResultStructured(
		map[string]any{"error_code": string(code), "message": msg},
		fmt.Sprintf("[%s] %s", code, msg),
	)
	r.IsError = true
	return r
}
func errResultf(code errorCode, format string, a ...any) *mcpgo.CallToolResult {
	return errResult(code, fmt.Sprintf(format, a...))
}
```

gRPC status → error code mapping (`codeFromGRPC`): `Unavailable/DeadlineExceeded→DEVICE_UNREACHABLE` (DeadlineExceeded also acceptable as TIMEOUT; we map transport deadline to DEVICE_UNREACHABLE and use TIMEOUT only for our own context timeouts), `PermissionDenied→ENTITLEMENT_DENIED`, `NotFound→NOT_FOUND`, `InvalidArgument→INVALID_ARGUMENT`, `Unimplemented→UNSUPPORTED`, default→`INTERNAL`. The message stays the unwrapped gRPC message (current `grpcErrString` behavior).

### Structured results (`results.go`)

```go
// okResult returns a result carrying v as structuredContent plus an indented-JSON text fallback.
func okResult(v any) *mcpgo.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResultf(errCodeInternal, "marshaling result: %s", err.Error())
	}
	return mcpgo.NewToolResultStructured(v, string(b))
}
// okText returns a plain text success result (for simple string confirmations).
func okText(msg string) *mcpgo.CallToolResult { return mcpgo.NewToolResultText(msg) }
```

Formal per-tool `WithOutputSchema[T]()` is **deferred** (needs concrete Go result types; handlers currently build `map[string]any`). PR1 delivers `structuredContent` on every data-returning tool, which is the high-value part for agents. Note this deferral in the PR description.

### Annotation matrix (`annotations.go`)

**CRITICAL:** `mcp-go`'s `NewTool` applies pessimistic MCP defaults to every tool —
`ReadOnlyHint=false`, `DestructiveHint=true`, `IdempotentHint=false`, `OpenWorldHint=true`.
"Absence" therefore advertises a tool as destructive + open-world. Helpers must set
each axis **explicitly**. Every tool applies exactly one behavior helper
(`readOnly`/`mutating`/`destructive`) and exactly one world helper
(`localOnly`/`openWorld`); `idempotent()` is an optional modifier for mutating tools
(`readOnly` already implies idempotent — do not stack them).

```go
func readOnly() []mcpgo.ToolOption { // reads only: not destructive, idempotent
	return []mcpgo.ToolOption{mcpgo.WithReadOnlyHintAnnotation(true), mcpgo.WithDestructiveHintAnnotation(false), mcpgo.WithIdempotentHintAnnotation(true)}
}
func mutating() []mcpgo.ToolOption { // changes state, not destructively
	return []mcpgo.ToolOption{mcpgo.WithReadOnlyHintAnnotation(false), mcpgo.WithDestructiveHintAnnotation(false)}
}
func destructive() []mcpgo.ToolOption { // irreversible/disruptive updates
	return []mcpgo.ToolOption{mcpgo.WithReadOnlyHintAnnotation(false), mcpgo.WithDestructiveHintAnnotation(true)}
}
func idempotent() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithIdempotentHintAnnotation(true)}
}
func openWorld() []mcpgo.ToolOption { // external network/radios/cloud broker
	return []mcpgo.ToolOption{mcpgo.WithOpenWorldHintAnnotation(true)}
}
func localOnly() []mcpgo.ToolOption { // closed world: connected device/local host
	return []mcpgo.ToolOption{mcpgo.WithOpenWorldHintAnnotation(false)}
}
```

Usage pattern (options spread):

```go
opts := []mcpgo.ToolOption{mcpgo.WithDescription("...")}
opts = append(opts, readOnly()...)
opts = append(opts, openWorld()...)
srv.AddTool(mcpgo.NewTool("device_list", opts...), s.handleDeviceList)
```

Per-tool classification — apply the named behavior helper + world helper (+ `idempotent()` where shown). `readOnly` already implies idempotent, so those rows show "—":

| Tool | behavior | world | +idempotent() |
|------|----------|-------|:--:|
| wendy_status | readOnly | localOnly | — |
| device_list | readOnly | openWorld | — |
| device_connect | mutating | openWorld | ✓ |
| device_disconnect | mutating | localOnly | ✓ |
| device_info | readOnly | localOnly | — |
| device_set_default | mutating | localOnly | ✓ |
| container_list | readOnly | localOnly | — |
| container_start | mutating | localOnly | |
| container_stop | destructive | localOnly | ✓ |
| container_delete | destructive | localOnly | ✓ |
| container_stats | readOnly | localOnly | — |
| container_attach | readOnly | localOnly | — |
| telemetry_logs / _metrics / _traces | readOnly | localOnly | — |
| wifi_list | readOnly | openWorld | — |
| wifi_connect | mutating | openWorld | ✓ |
| wifi_status | readOnly | localOnly | — |
| wifi_disconnect | destructive | openWorld | ✓ |
| wifi_known_networks | readOnly | localOnly | — |
| bluetooth_scan | readOnly | openWorld | — |
| bluetooth_connect | mutating | openWorld | ✓ |
| bluetooth_disconnect | destructive | openWorld | ✓ |
| hardware_capabilities | readOnly | localOnly | — |
| filesync_sync | mutating | localOnly | ✓ |
| provisioning_status | readOnly | localOnly | — |
| provisioning_start | mutating | openWorld | ✓ |
| os_update | destructive | openWorld | |
| os_update_status | readOnly | localOnly | — |
| cloud_discover | readOnly | openWorld | — |
| cloud_connect / cloud_device_connect | mutating | openWorld | ✓ |
| cloud_enroll_device | mutating | openWorld | ✓ |
| cloud_tunnel | mutating | openWorld | ✓ |
| run / cloud_run | mutating | openWorld | |

---

## Task 1: Error-code helpers

**Files:**
- Create: `go/internal/cli/mcp/errors.go`
- Test: `go/internal/cli/mcp/errors_test.go`

**Interfaces:**
- Produces: `type errorCode string`; constants above; `errResult(code errorCode, msg string) *mcpgo.CallToolResult`; `errResultf(code errorCode, format string, a ...any) *mcpgo.CallToolResult`; `codeFromGRPC(err error) errorCode`.

- [ ] **Step 1: Write failing test** in `errors_test.go`

```go
package mcp

import (
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestErrResult_StructuredAndText(t *testing.T) {
	r := errResult(errCodeNotConnected, "no device connected")
	if !r.IsError {
		t.Fatal("expected IsError=true")
	}
	sc, ok := r.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected map structured content, got %T", r.StructuredContent)
	}
	if sc["error_code"] != "NOT_CONNECTED" {
		t.Errorf("error_code = %v, want NOT_CONNECTED", sc["error_code"])
	}
	text := toolResultText(t, r)
	if text != "[NOT_CONNECTED] no device connected" {
		t.Errorf("text = %q", text)
	}
}

func TestCodeFromGRPC(t *testing.T) {
	cases := map[codes.Code]errorCode{
		codes.Unavailable:      errCodeDeviceUnreachable,
		codes.PermissionDenied: errCodeEntitlementDenied,
		codes.NotFound:         errCodeNotFound,
		codes.InvalidArgument:  errCodeInvalidArgument,
		codes.Unimplemented:    errCodeUnsupported,
		codes.Internal:         errCodeInternal,
	}
	for c, want := range cases {
		if got := codeFromGRPC(status.Error(c, "x")); got != want {
			t.Errorf("codeFromGRPC(%v) = %v, want %v", c, got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/mcp/... -run 'TestErrResult_StructuredAndText|TestCodeFromGRPC' -v`
Expected: FAIL — `undefined: errResult` / `errCodeNotConnected` / `codeFromGRPC`.

- [ ] **Step 3: Write `errors.go`**

```go
package mcp

import (
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type errorCode string

const (
	errCodeNotConnected      errorCode = "NOT_CONNECTED"
	errCodeInvalidArgument   errorCode = "INVALID_ARGUMENT"
	errCodeDeviceUnreachable errorCode = "DEVICE_UNREACHABLE"
	errCodeEntitlementDenied errorCode = "ENTITLEMENT_DENIED"
	errCodeMultipleSessions  errorCode = "MULTIPLE_SESSIONS"
	errCodeNotFound          errorCode = "NOT_FOUND"
	errCodeTimeout           errorCode = "TIMEOUT"
	errCodeUnsupported       errorCode = "UNSUPPORTED"
	errCodeInternal          errorCode = "INTERNAL"
)

// errResult builds an error tool result with a machine-readable code and a
// human-readable "[CODE] message" text fallback.
func errResult(code errorCode, msg string) *mcpgo.CallToolResult {
	r := mcpgo.NewToolResultStructured(
		map[string]any{"error_code": string(code), "message": msg},
		fmt.Sprintf("[%s] %s", code, msg),
	)
	r.IsError = true
	return r
}

func errResultf(code errorCode, format string, a ...any) *mcpgo.CallToolResult {
	return errResult(code, fmt.Sprintf(format, a...))
}

// codeFromGRPC maps a gRPC status error to an errorCode.
func codeFromGRPC(err error) errorCode {
	st, ok := status.FromError(err)
	if !ok {
		return errCodeInternal
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded:
		return errCodeDeviceUnreachable
	case codes.PermissionDenied:
		return errCodeEntitlementDenied
	case codes.NotFound:
		return errCodeNotFound
	case codes.InvalidArgument:
		return errCodeInvalidArgument
	case codes.Unimplemented:
		return errCodeUnsupported
	default:
		return errCodeInternal
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/mcp/... -run 'TestErrResult_StructuredAndText|TestCodeFromGRPC' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/cli/mcp/errors.go internal/cli/mcp/errors_test.go
git add internal/cli/mcp/errors.go internal/cli/mcp/errors_test.go
git commit -m "feat(mcp): add machine-readable error codes for tool results"
```

---

## Task 2: Structured-result helpers

**Files:**
- Create: `go/internal/cli/mcp/results.go`
- Test: `go/internal/cli/mcp/results_test.go`

**Interfaces:**
- Consumes: `errResultf` (Task 1).
- Produces: `okResult(v any) *mcpgo.CallToolResult`; `okText(msg string) *mcpgo.CallToolResult`.

- [ ] **Step 1: Write failing test** in `results_test.go`

```go
package mcp

import (
	"encoding/json"
	"testing"
)

func TestOkResult_HasStructuredAndJSONText(t *testing.T) {
	r := okResult(map[string]any{"version": "1.2.3"})
	if r.IsError {
		t.Fatal("expected success result")
	}
	if r.StructuredContent == nil {
		t.Fatal("expected structured content")
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(toolResultText(t, r)), &m); err != nil {
		t.Fatalf("text fallback is not valid JSON: %v", err)
	}
	if m["version"] != "1.2.3" {
		t.Errorf("version = %v", m["version"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/mcp/... -run TestOkResult -v`
Expected: FAIL — `undefined: okResult`.

- [ ] **Step 3: Write `results.go`**

```go
package mcp

import (
	"encoding/json"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

// okResult returns a success result carrying v as structuredContent plus an
// indented-JSON text fallback for hosts that do not render structured content.
func okResult(v any) *mcpgo.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResultf(errCodeInternal, "marshaling result: %s", err.Error())
	}
	return mcpgo.NewToolResultStructured(v, string(b))
}

// okText returns a plain-text success result (for simple confirmations).
func okText(msg string) *mcpgo.CallToolResult {
	return mcpgo.NewToolResultText(msg)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/mcp/... -run TestOkResult -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/cli/mcp/results.go internal/cli/mcp/results_test.go
git add internal/cli/mcp/results.go internal/cli/mcp/results_test.go
git commit -m "feat(mcp): add structured-content result helper"
```

---

## Task 3: Annotation option bundles

**Files:**
- Create: `go/internal/cli/mcp/annotations.go`
- Test: `go/internal/cli/mcp/annotations_test.go`

**Interfaces:**
- Produces: `readOnly()`, `destructive()`, `idempotent()`, `openWorld()` each returning `[]mcpgo.ToolOption`.

- [ ] **Step 1: Write failing test** in `annotations_test.go`

```go
package mcp

import (
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestAnnotations_ReadOnlyAndDestructive(t *testing.T) {
	ro := mcpgo.NewTool("t_ro", readOnly()...)
	if ro.Annotations.ReadOnlyHint == nil || !*ro.Annotations.ReadOnlyHint {
		t.Error("readOnly() should set ReadOnlyHint=true")
	}
	de := mcpgo.NewTool("t_de", destructive()...)
	if de.Annotations.DestructiveHint == nil || !*de.Annotations.DestructiveHint {
		t.Error("destructive() should set DestructiveHint=true")
	}
	if de.Annotations.ReadOnlyHint == nil || *de.Annotations.ReadOnlyHint {
		t.Error("destructive() should set ReadOnlyHint=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/mcp/... -run TestAnnotations -v`
Expected: FAIL — `undefined: readOnly`.

- [ ] **Step 3: Write `annotations.go`**

```go
package mcp

import mcpgo "github.com/mark3labs/mcp-go/mcp"

// readOnly marks a tool that does not modify device or host state.
func readOnly() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithReadOnlyHintAnnotation(true)}
}

// destructive marks a tool that may perform irreversible or disruptive updates.
func destructive() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{
		mcpgo.WithDestructiveHintAnnotation(true),
		mcpgo.WithReadOnlyHintAnnotation(false),
	}
}

// idempotent marks a tool where repeated identical calls have no extra effect.
func idempotent() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithIdempotentHintAnnotation(true)}
}

// openWorld marks a tool that interacts with external entities (network,
// nearby radios, cloud broker) whose state the server does not own.
func openWorld() []mcpgo.ToolOption {
	return []mcpgo.ToolOption{mcpgo.WithOpenWorldHintAnnotation(true)}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/mcp/... -run TestAnnotations -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/cli/mcp/annotations.go internal/cli/mcp/annotations_test.go
git add internal/cli/mcp/annotations.go internal/cli/mcp/annotations_test.go
git commit -m "feat(mcp): add tool annotation option bundles"
```

---

## Task 4: Adopt helpers in device + status tools; add annotations

**Files:**
- Modify: `go/internal/cli/mcp/tools_device.go`
- Modify: `go/internal/cli/mcp/tools_status.go`
- Modify: `go/internal/cli/mcp/server.go` (`errNotConnected` now returns `errResult(errCodeNotConnected, ...)`)
- Test: `go/internal/cli/mcp/tools_device_test.go` (add structured-content assertion)

**Interfaces:**
- Consumes: `okResult`, `errResult`, `codeFromGRPC`, annotation bundles.

- [ ] **Step 1: Add a failing test** to `tools_device_test.go`

```go
func TestDeviceInfo_HasStructuredContent(t *testing.T) {
	fake := &fakeAgentServer{versionResp: &agentpb.GetAgentVersionResponse{Version: "9.9.9", Os: "linux"}}
	conn, _ := startFakeAgentServer(t, fake)
	srv := New(&config.Config{}, nil)
	srv.SetConn(conn)
	result, err := srv.callTool(context.Background(), "device_info", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.StructuredContent == nil {
		t.Fatal("device_info should return structuredContent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/mcp/... -run TestDeviceInfo_HasStructuredContent -v`
Expected: FAIL — `StructuredContent` is nil (handler still uses `NewToolResultText`).

- [ ] **Step 3: Update `server.go` `errNotConnected`**

```go
func errNotConnected() *mcpgo.CallToolResult {
	return errResult(errCodeNotConnected, "no device connected — use device_connect first")
}
```

- [ ] **Step 4: Update `tools_device.go`** — registration adds annotations; handlers use helpers. Replace the `registerDeviceTools` body with option-spread form and swap result constructors. Examples:

```go
func (s *mcpServer) registerDeviceTools(srv *server.MCPServer) {
	listOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("List wendy devices from config and known addresses. Pass scan=true to also run a live 3-second mDNS scan for devices on the local network."),
		mcpgo.WithBoolean("scan", mcpgo.Description("If true, run a live mDNS scan (3 s) in addition to returning configured devices")),
	}
	listOpts = append(listOpts, readOnly()...)
	listOpts = append(listOpts, openWorld()...)
	srv.AddTool(mcpgo.NewTool("device_list", listOpts...), s.handleDeviceList)

	connectOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("Connect to a wendy device by address (host:port)"),
		mcpgo.WithString("address", mcpgo.Required(), mcpgo.Description("Device address, e.g. mydevice.local:50051 or 192.168.1.10:50051")),
	}
	connectOpts = append(connectOpts, idempotent()...)
	connectOpts = append(connectOpts, openWorld()...)
	srv.AddTool(mcpgo.NewTool("device_connect", connectOpts...), s.handleDeviceConnect)

	disconnectOpts := append([]mcpgo.ToolOption{mcpgo.WithDescription("Disconnect from the currently connected device")}, idempotent()...)
	srv.AddTool(mcpgo.NewTool("device_disconnect", disconnectOpts...), s.handleDeviceDisconnect)

	infoOpts := append([]mcpgo.ToolOption{mcpgo.WithDescription("Get agent version, OS, CPU architecture, GPU info, and feature set of connected device")}, readOnly()...)
	srv.AddTool(mcpgo.NewTool("device_info", infoOpts...), s.handleDeviceInfo)

	setDefaultOpts := []mcpgo.ToolOption{
		mcpgo.WithDescription("Save an address as the default device in ~/.wendy/config.json"),
		mcpgo.WithString("address", mcpgo.Required(), mcpgo.Description("Device address to save as default")),
	}
	setDefaultOpts = append(setDefaultOpts, idempotent()...)
	srv.AddTool(mcpgo.NewTool("device_set_default", setDefaultOpts...), s.handleDeviceSetDefault)
}
```

Handler result swaps in `tools_device.go`:
- `handleDeviceList`: final `return mcpgo.NewToolResultText(string(b)), nil` → `return okResult(devices), nil` (drop the manual `json.MarshalIndent`; keep the `if len(devices)==0 { devices = []map[string]any{} }` guard).
- `handleDeviceConnect`: `address == ""` branch → `return errResult(errCodeInvalidArgument, "address is required"), nil`; connect-failure branch → `return errResultf(errCodeDeviceUnreachable, "connecting to %s: %s", address, err.Error()), nil`; success → `return okText(fmt.Sprintf("connected to %s", address)), nil`.
- `handleDeviceInfo`: gRPC error branch → `return errResult(codeFromGRPC(err), grpcErrString(err)), nil`; final → `return okResult(info), nil`.
- `handleDeviceSetDefault`: empty → `errResult(errCodeInvalidArgument, "address is required")`; save error → `errResultf(errCodeInternal, "saving config: %s", err.Error())`; success → `okText(...)`.

Remove now-unused `encoding/json` import from `tools_device.go` **only if** no other use remains (handleDeviceList/Info no longer marshal). Verify with `go build`.

- [ ] **Step 5: Update `tools_status.go`** analogously — add `readOnly()` to `wendy_status`, swap its data result to `okResult` and any error to `errResult`. (Read the file first; apply the same mechanical swaps.)

- [ ] **Step 6: Run tests**

Run: `go test ./internal/cli/mcp/... -run 'TestDevice|TestWendyStatus' -v`
Expected: PASS (including the new structured-content test and all pre-existing device/status tests, which read `Content[0].Text`).

- [ ] **Step 7: Commit**

```bash
gofmt -w internal/cli/mcp/tools_device.go internal/cli/mcp/tools_status.go internal/cli/mcp/server.go internal/cli/mcp/tools_device_test.go
git add -A internal/cli/mcp/
git commit -m "feat(mcp): annotations + structured results for device and status tools"
```

---

## Task 5: Adopt helpers in container tools

**Files:**
- Modify: `go/internal/cli/mcp/tools_container.go`
- Test: `go/internal/cli/mcp/tools_container_test.go`

**Interfaces:** Consumes helpers from Tasks 1–3.

- [ ] **Step 1: Add failing test** asserting `container_delete` is annotated destructive at registration. Because tests call handlers directly (not via a registered server), assert the annotation through a tiny registration probe:

```go
func TestContainerDelete_AnnotatedDestructive(t *testing.T) {
	srv := server.NewMCPServer("t", "0")
	s := New(&config.Config{}, nil)
	s.registerContainerTools(srv)
	tools := srv.ListTools() // returns map[string]ServerTool
	tool, ok := tools["container_delete"]
	if !ok {
		t.Fatal("container_delete not registered")
	}
	if tool.Tool.Annotations.DestructiveHint == nil || !*tool.Tool.Annotations.DestructiveHint {
		t.Error("container_delete should be annotated destructive")
	}
}
```

Note: confirm the accessor name for registered tools on `*server.MCPServer` in v0.54.0 before finalizing (grep `func (s \*MCPServer) ListTools` in the module cache; if the accessor differs, adjust — do not invent one). If no public accessor exists, drop this probe test and instead assert annotations via a unit test on the option bundle already covered in Task 3, and rely on manual verification for wiring.

- [ ] **Step 2: Run — verify fail.** `go test ./internal/cli/mcp/... -run TestContainerDelete_AnnotatedDestructive -v`

- [ ] **Step 3: Update `tools_container.go`** — add annotations per matrix (`container_list`/`container_stats`/`container_attach` readOnly; `container_stop`/`container_delete` destructive+idempotent; `container_start` none). Swap every `NewToolResultError(...)` → `errResult(code, ...)` using `codeFromGRPC` for gRPC failures and `errCodeInvalidArgument` for bad params; swap data results (`container_list`, `container_stats`) → `okResult(...)`. Preserve the entitlement-denied text in `container_list` termination handling but wrap as `errResult(errCodeEntitlementDenied, msg)` where the code returns an error for that case.

- [ ] **Step 4: Run tests.** `go test ./internal/cli/mcp/... -run TestContainer -v` — Expected PASS.

- [ ] **Step 5: Commit.** `feat(mcp): annotations + structured results for container tools`

---

## Task 6: Adopt helpers in telemetry, wifi, bluetooth, hardware tools

**Files:** Modify `tools_telemetry.go`, `tools_wifi.go`, `tools_bluetooth.go`, `tools_hardware.go`; touch matching `_test.go` where an assertion strengthens (structured content on a list tool each).

- [ ] **Step 1:** Add one structured-content test per domain (e.g. `TestWiFiList_HasStructuredContent`, `TestHardwareCapabilities_HasStructuredContent`) following the Task 4 pattern, using the existing fakes in each `_test.go`.
- [ ] **Step 2:** Run — verify fail.
- [ ] **Step 3:** Apply annotations per matrix and swap result constructors (`okResult` for data; `errResult`+`codeFromGRPC` for gRPC errors; `errCodeInvalidArgument` for missing required params like `ssid`, `mac`).
- [ ] **Step 4:** Run `go test ./internal/cli/mcp/... -run 'TestTelemetry|TestWiFi|TestBluetooth|TestHardware' -v` — Expected PASS.
- [ ] **Step 5:** Commit `feat(mcp): annotations + structured results for telemetry/wifi/bluetooth/hardware tools`.

---

## Task 7: Adopt helpers in provisioning, os, filesync tools

**Files:** Modify `tools_provisioning.go`, `tools_os.go`, `tools_filesync.go`; matching `_test.go`.

- [ ] **Step 1:** Add structured-content test for `provisioning_status`; keep the `filesync_sync` error test but assert its code is `errCodeUnsupported` now:

```go
func TestFileSyncSync_UnsupportedCode(t *testing.T) {
	srv := New(&config.Config{}, nil)
	r, _ := srv.callTool(context.Background(), "filesync_sync", nil)
	if !r.IsError {
		t.Fatal("filesync_sync should error")
	}
	sc, _ := r.StructuredContent.(map[string]any)
	if sc == nil || sc["error_code"] != "UNSUPPORTED" {
		t.Errorf("want error_code UNSUPPORTED, got %v", sc)
	}
}
```

- [ ] **Step 2:** Run — verify fail.
- [ ] **Step 3:** Apply annotations (`provisioning_status`/`os_update_status` readOnly; `provisioning_start` idempotent+openWorld; `os_update` destructive+openWorld; `filesync_sync` idempotent). Swap result constructors; `filesync_sync` returns `errResult(errCodeUnsupported, "filesync over MCP is not supported; run `wendy device sync` from the CLI")`. (Full resolution of filesync is PR3 Task; here we only migrate to the code.)
- [ ] **Step 4:** Run `go test ./internal/cli/mcp/... -run 'TestProvisioning|TestOS|TestFileSync|TestHardware' -v` — Expected PASS.
- [ ] **Step 5:** Commit `feat(mcp): annotations + structured results for provisioning/os/filesync tools`.

---

## Task 8: Adopt helpers in cloud tools

**Files:** Modify `tools_cloud.go`; `tools_cloud_test.go`.

- [ ] **Step 1:** Add a structured-content test for `cloud_discover` (using its existing fake); keep the multi-session error test but assert code `errCodeMultipleSessions`.
- [ ] **Step 2:** Run — verify fail.
- [ ] **Step 3:** Apply annotations per matrix. Swap constructors. Map the multi-session error (`ErrMultipleSessions`) to `errResult(errCodeMultipleSessions, <existing actionable message>)`; `pickCloudAsset` "run cloud_discover" → `errResult(errCodeNotFound, ...)`; `run`/`cloud_run` shell-out failures → `errResultf(errCodeInternal, ...)` preserving combined output in the message; `cloud_discover` over-cap → `errResultf(errCodeInvalidArgument, ...)`.
- [ ] **Step 4:** Run `go test ./internal/cli/mcp/... -run TestCloud -v` — Expected PASS.
- [ ] **Step 5:** Commit `feat(mcp): annotations + structured results for cloud tools`.

---

## Task 9: Full-package verification + guide-resource note

**Files:** Modify `go/internal/cli/assets/docs/` guide source if the guide text lives there, or `tools_guide.go` inline text — add a short "Result shape & error codes" paragraph documenting that data tools return `structuredContent` and errors carry `error_code`.

- [ ] **Step 1:** Run the whole package: `go test ./internal/cli/mcp/... -v` — Expected: all PASS, zero skips of previously-passing tests.
- [ ] **Step 2:** `go vet ./internal/cli/mcp/...` — Expected: clean.
- [ ] **Step 3:** `gofmt -l internal/cli/mcp` — Expected: no files listed.
- [ ] **Step 4:** Add the guide paragraph (read `tools_guide.go` first; append to the guide text constant). Text: *"Tools that return data include a machine-readable `structuredContent` object alongside human-readable text. Errors include an `error_code` (e.g. NOT_CONNECTED, ENTITLEMENT_DENIED, DEVICE_UNREACHABLE) you can branch on. Tool annotations mark read-only vs destructive operations."*
- [ ] **Step 5:** Commit `docs(mcp): document structured results and error codes in guide`.

---

## Self-Review (PR1)

- **Coverage:** #1 annotations → Task 3 bundles + Tasks 4–8 apply the matrix (every tool row assigned). #2 structured content → `okResult` (Task 2) applied Tasks 4–8; formal outputSchema explicitly deferred and noted. #6 error codes → Task 1 + applied Tasks 4–8; taxonomy maps every current error site. #9 de-boilerplate → `errors.go`/`results.go`/`annotations.go` replace the repeated `json.MarshalIndent`+`NewToolResultText`+`NewToolResultError` triples; no DSL rewrite (follows codebase idiom).
- **Placeholders:** none — every code step shows the code; the one uncertain accessor (`ListTools`) has an explicit "verify or drop" instruction rather than an invented API.
- **Type consistency:** `errorCode` constants, `errResult`/`errResultf`/`codeFromGRPC`, `okResult`/`okText`, `readOnly/destructive/idempotent/openWorld` names are used identically in every task.
- **Backward-compat:** text fallback preserved on every result (existing `Content[0].Text` tests unaffected); no tool/param renames in PR1.

---

# Appendix A — PR2 (Reliability) design lock

Write the step-level plan (same TDD format) at PR2 start. Design is fixed:

**#3 Output bounding — `results.go` additions.**
- `clampJSON(v any, maxBytes int) (any, bool)`: marshal, and if over `maxBytes`, return a truncation envelope `{"truncated": true, "max_bytes": N, "note": "output exceeded N bytes; narrow the query (reduce max_batches / max_chunks or filter)"}` with as much head data as fits, plus `truncated=true`. Default `maxBytes = 100_000`, overridable per call via a new optional `max_bytes` number param on `telemetry_logs`, `container_attach`, `container_start`, `run`.
- Rename `max_lines` → `max_chunks` on `container_attach`/`container_start`, **accepting `max_lines` as an alias** (`intParam` with fallback to the other key). Update descriptions to say "chunks".
- Every bounded result sets a top-level `truncated` bool in structured content so an agent knows data was cut.
- Honest limitation to log in PR body: gRPC streams are **not resumable**, so there is no true cursor; bounding = byte cap + explicit `truncated` signal + narrower-query guidance.

**#4 Progress notifications — new `progress.go`.**
- `reportProgress(ctx, progressToken, progress, total float64, message string)` uses `server.ServerFromContext(ctx).SendNotificationToClient(ctx, "notifications/progress", params)`; no-op when `progressToken == nil`.
- Extract `progressToken` from `req.Params.Meta` (guard nil Meta). Wire into `handleOSUpdate` (emit per `[phase] N%`), `handleProvisioningStart` (emit each 3s poll), `handleRun` (emit on start/end). Final return unchanged (still the summary blob).
- Handlers get the server via `server.ServerFromContext(ctx)` — no struct change needed.

**#5 Proxy diagnostics — new `diagnostics.go` + `mcpServer` field.**
- Add `proxyDiag []proxyDiagEntry` (guarded by existing `mu`) with `{app_name, stage, error, time}`; `time` supplied by caller (avoid `time.Now()` inside pure helpers is unnecessary here — this is runtime, `time.Now()` is fine in non-workflow code).
- Replace the six `fmt.Fprintf(os.Stderr, "Warning: ...")` sites in `server.go` with `s.recordProxyDiag(appName, stage, err)` (keep a single stderr line too for humans).
- Surface via (a) a new `wendy://diagnostics` resource listing entries as JSON, and (b) a `proxy_diagnostics` array in `wendy_status` structured content.
- Add `WithResourceCapabilities(true, true)` only in PR that adds listChanged; leave as-is here.

---

# Appendix B — PR3 (Discoverability) design lock

**#7 Prompts — new `prompts.go`, registered in `Start`.**
- `AddPrompt` three workflow prompts: `deploy_app` (args: `app_path`, `device?`), `diagnose_container` (args: `app_name?`), `provision_device` (args: `address?`). Each handler returns `GetPromptResult` with a user-role message templating the known connect→act→verify tool sequence in prose. No device calls in the handler — pure templating.

**#8 Descriptions — sweep across `tools_*.go`.**
- Add a worked example line to `container_start`/`run` descriptions referencing `wendy.json` entitlements (e.g. gpu/network/persistence) the app may need, and note that entitlement failures surface as `error_code=ENTITLEMENT_DENIED`.
- Ensure the `max_lines`→`max_chunks` rename (from PR2) descriptions read correctly.
- Add example addresses/args where missing (audit each `WithString`/`WithNumber` `Description`).

**#10 filesync resolution — decision: implement minimal or remove.**
- `WendyFileSyncService.SyncFiles` is a bidi stream (FileSyncStart → chunks/commit → manifest/ack → complete). Implementing full host↔device dir sync over MCP is high-effort and awkward (agent-driven binary transfer). **Default: remove `filesync_sync` from the tool list** and add a guide note pointing at the CLII (`wendy device sync`), because a tool that only ever errors wastes an agent call. Alternative (only if product wants it): implement a **single-file push** MCP tool (`filesync_push` with `local_path`,`remote_path`) using the existing stream for one file. Pick at PR3 start; default is removal.

---

# Execution notes

- Tasks 4–8 are mechanical and independent per file, but they share the three helper files (Tasks 1–3), so **Tasks 1–3 must land first**. 4–8 can then be done in any order / parallelized across subagents in this worktree (distinct files, no shared edits except each touches its own `tools_*` + `_test`).
- Run the full `go test ./internal/cli/mcp/...` after each of Tasks 4–8, not just the filtered subset, to catch cross-file breakage from the shared helper adoption.
