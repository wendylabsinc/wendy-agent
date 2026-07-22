# MCP Agent-Support PR3 — Discoverability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the `wendy mcp serve` tools easier for agents to discover and use correctly: workflow prompts (#7), richer tool descriptions incl. entitlement guidance (#8), and removing the dead `filesync_sync` tool (#10).

**Architecture:** Adds `prompts.go` (MCP prompts registered in `Start`), edits tool-description strings in place, and removes the `filesync_sync` tool end-to-end. Base branch: `jo/mcp-agent-support-2-reliability` (stacked on PR2).

**Tech Stack:** Go, `mark3labs/mcp-go` v0.54.0.

## Global Constraints

- Import aliases `mcpgo "github.com/mark3labs/mcp-go/mcp"`, `"github.com/mark3labs/mcp-go/server"`.
- No new deps. gofmt/vet clean; `go test ./internal/cli/mcp/...` green after every task.
- Prompts are pure templating — their handlers must NOT call the device/gRPC; they only return message text describing a tool workflow.
- Removing `filesync_sync` is a deliberate tool removal (decided: remove, not implement). Update every reference so the package compiles and tests pass.
- Confirmed mcp-go v0.54.0 prompt API: `mcpgo.NewPrompt(name, mcpgo.WithPromptDescription(...), mcpgo.WithArgument(name, mcpgo.ArgumentDescription(...)))`; `srv.AddPrompt(prompt, handler)`; handler is `func(ctx, mcpgo.GetPromptRequest) (*mcpgo.GetPromptResult, error)`; args via `req.Params.Arguments` (`map[string]string`); build result with `mcpgo.NewGetPromptResult(desc, []mcpgo.PromptMessage{mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(text))})`. Verify `mcpgo.NewTextContent` and `mcpgo.ArgumentDescription` exist in v0.54.0 before use; if the argument-description option has a different name, grep the package and use the actual one.

---

## Task 1: Workflow prompts

**Files:**
- Create: `go/internal/cli/mcp/prompts.go` (+ `prompts_test.go`)
- Modify: `server.go` `Start()` — call `s.registerPrompts(srv)`; add `server.WithPromptCapabilities(false)` to `NewMCPServer` options.

**Interfaces:**
- Produces: `(s *mcpServer) registerPrompts(srv *server.MCPServer)`; handlers `handleDeployAppPrompt`, `handleDiagnoseContainerPrompt`, `handleProvisionDevicePrompt`.

Three prompts, each returns a single user-role message templating the known tool sequence in prose (using any provided arguments):
- `deploy_app` — args: `project_path` (optional), `device` (optional). Message walks: ensure connected (`device_connect`/`cloud_connect`), then `run` with the project path, then check `container_list`/`telemetry_logs`.
- `diagnose_container` — args: `app_name` (optional). Message walks: `container_list` (look at running_state/termination_reason; ENTITLEMENT_DENIED error_code → fix `wendy.json`), `container_stats`, `telemetry_logs`, and check `wendy://diagnostics` for proxy issues.
- `provision_device` — args: `address` (optional). Message walks: `device_connect`, `provisioning_status`, `provisioning_start` (enrollment_token/cloud_host/organization_id), watch progress.

- [ ] **Step 1: Write `prompts_test.go`**

```go
package mcp

import (
	"context"
	"strings"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func getPromptReq(args map[string]string) mcpgo.GetPromptRequest {
	req := mcpgo.GetPromptRequest{}
	req.Params.Arguments = args
	return req
}

func TestDeployAppPrompt_MentionsRunAndPath(t *testing.T) {
	s := New(nil, nil)
	res, err := s.handleDeployAppPrompt(context.Background(), getPromptReq(map[string]string{"project_path": "/tmp/myapp"}))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Messages) == 0 {
		t.Fatal("expected at least one message")
	}
	tc, ok := res.Messages[0].Content.(mcpgo.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Messages[0].Content)
	}
	if !strings.Contains(tc.Text, "/tmp/myapp") || !strings.Contains(tc.Text, "run") {
		t.Errorf("prompt should reference the project path and the run tool; got: %s", tc.Text)
	}
}

func TestDiagnoseContainerPrompt_MentionsDiagnostics(t *testing.T) {
	s := New(nil, nil)
	res, err := s.handleDiagnoseContainerPrompt(context.Background(), getPromptReq(nil))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := res.Messages[0].Content.(mcpgo.TextContent)
	if !strings.Contains(tc.Text, "container_list") {
		t.Errorf("diagnose prompt should reference container_list; got: %s", tc.Text)
	}
}
```

(`New(nil, nil)` is safe — prompt handlers never touch cfg/conn. If `New` dereferences cfg, pass `&config.Config{}` instead.)

- [ ] **Step 2: Run — verify fail.** `go test ./internal/cli/mcp/... -run 'Prompt' -v`
- [ ] **Step 3: Write `prompts.go`** — `registerPrompts` adds the three prompts with `WithArgument`s; each handler reads its args and returns `mcpgo.NewGetPromptResult(<desc>, []mcpgo.PromptMessage{mcpgo.NewPromptMessage(mcpgo.RoleUser, mcpgo.NewTextContent(<templated prose>))})`. For `deploy_app`, interpolate `project_path` (default `"."`) and `device` into the prose. Keep prose concise and tool-name-accurate (use real tool names: `device_connect`, `cloud_connect`, `run`, `container_list`, `telemetry_logs`, `container_stats`, `provisioning_status`, `provisioning_start`, and the `wendy://diagnostics` resource).
- [ ] **Step 4: Register in `Start()`** — add `s.registerPrompts(srv)` next to the resource registrations, and add `server.WithPromptCapabilities(false)` to the `server.NewMCPServer(...)` option list (verify the exact option name in v0.54.0; if absent, prompts still register — AddPrompt sets capability implicitly — so skip the option and note it).
- [ ] **Step 5: Run tests → green.** `gofmt -w`.
- [ ] **Step 6: Commit** `feat(mcp): add deploy/diagnose/provision workflow prompts`

---

## Task 2: Richer descriptions + entitlement guidance

**Files:** Modify `tools_container.go` (`container_start`), `tools_cloud.go` (`run`/`cloud_run`), and audit other `WithString`/`WithNumber` `Description`s for missing examples. Add/adjust a guide paragraph in `tools_guide.go` about entitlements.

- [ ] **Step 1: Add a test** in `tools_container_test.go` asserting the `container_start` tool description mentions entitlements (registration probe like Task 5's, via `srv.ListTools()["container_start"].Tool.Description`):

```go
func TestContainerStart_DescriptionMentionsEntitlements(t *testing.T) {
	srv := server.NewMCPServer("t", "0")
	s := New(&config.Config{}, nil)
	s.registerContainerTools(srv)
	tool, ok := srv.ListTools()["container_start"]
	if !ok {
		t.Fatal("container_start not registered")
	}
	if !strings.Contains(strings.ToLower(tool.Tool.Description), "entitlement") {
		t.Errorf("container_start description should mention entitlements; got: %s", tool.Tool.Description)
	}
}
```

- [ ] **Step 2: Run — verify fail.**
- [ ] **Step 3: Enrich descriptions.**
  - `container_start` description: add that the app runs with the entitlements declared in its `wendy.json` (gpu/network/persistence/etc.), and that missing entitlements surface as an `ENTITLEMENT_DENIED` error_code (visible in `container_list` termination_reason).
  - `run`/`cloud_run` description: add a one-line worked note — points at `wendy.json` entitlements the project may need and that entitlement failures return `ENTITLEMENT_DENIED`.
  - Audit remaining `Description(...)` strings in all `tools_*.go` for parameters lacking an example or unit; add brief examples where a value's format is non-obvious (addresses, ports, tokens, durations). Keep edits minimal and accurate — do not invent parameters.
- [ ] **Step 4: Guide note** in `tools_guide.go` — a short "Entitlements" paragraph: apps declare capabilities in `wendy.json`; denials surface as `ENTITLEMENT_DENIED`.
- [ ] **Step 5: Run tests → green.** `gofmt -w`.
- [ ] **Step 6: Commit** `docs(mcp): document entitlements in container_start/run descriptions and guide`

---

## Task 3: Remove the dead `filesync_sync` tool

**Files:**
- Delete: `go/internal/cli/mcp/tools_filesync.go`
- Modify: `server.go` (remove `s.registerFileSyncTools(srv)` at line ~108), `server_helpers_test.go` (remove the `case "filesync_sync":` at ~85-86), `tools_hardware_provisioning_test.go` (remove `TestFileSyncSync_AlwaysReturnsError` and `TestFileSyncSync_UnsupportedCode` + the `--- FileSync tests ---` comment), `tools_guide.go` (remove the `- filesync_sync` bullet at line ~43 and any surrounding filesync mention; add a one-line note that host↔device file sync is a CLI operation — `wendy device sync`).

- [ ] **Step 1: Delete the tool + registration.** Remove `tools_filesync.go`; remove the `registerFileSyncTools` call in `server.go`.
- [ ] **Step 2: Remove test references.** Delete the two filesync test funcs and the `callTool` switch case. (These deletions are the "test" for removal — `filesync_sync` must no longer be registered or handled.)
- [ ] **Step 3: Guide update.** Replace the `filesync_sync` bullet with a note pointing to the CLI.
- [ ] **Step 4: Build + full suite.** `go build ./internal/cli/mcp/...` (must compile — no dangling references), `go test ./internal/cli/mcp/...` → green. `go vet`, `gofmt -l` clean.
- [ ] **Step 5: Optional guard test** — add a test asserting `filesync_sync` is NOT in `srv.ListTools()` after registering all tools, to lock the removal:

```go
func TestFileSyncSync_NotRegistered(t *testing.T) {
	srv := server.NewMCPServer("t", "0")
	s := New(&config.Config{}, nil)
	s.registerFileSyncTools(srv) // <-- if this line fails to compile, delete it; the point is the tool is gone
	_ = srv
}
```

If `registerFileSyncTools` no longer exists (it shouldn't), do NOT add that test; instead add one that registers ALL tools via the same path `Start` uses and asserts `filesync_sync` absent. Keep it simple; if awkward, rely on the removed switch-case + compile as sufficient.

- [ ] **Step 6: Commit** `refactor(mcp): remove dead filesync_sync tool (use wendy CLI for file sync)`

---

## Self-Review (PR3)

- #7: three pure-templating prompts registered; handlers don't touch the device; tested for tool-name references.
- #8: entitlement guidance on container_start/run + guide; param examples audited.
- #10: filesync_sync removed end-to-end (tool, handler, tests, guide, switch); package compiles and tests pass; guide points to CLI.
