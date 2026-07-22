# MCP Agent-Support PR2 — Reliability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add output bounding (#3), progress notifications for long operations (#4), and visible proxy diagnostics (#5) to the `wendy mcp serve` server.

**Architecture:** Builds on PR1's shared helpers. Adds `progress.go` (progress-notification helper), extends `results.go` (byte-clamping), adds `diagnostics.go` (+ a field on `mcpServer`). Handlers keep their signatures; they reach the server for notifications via `server.ServerFromContext(ctx)`.

**Tech Stack:** Go, `mark3labs/mcp-go` v0.54.0, gRPC. Base branch: `jo/mcp-agent-support-1-foundation` (stacked on PR1).

## Global Constraints

- Import aliases: `mcpgo "github.com/mark3labs/mcp-go/mcp"`, `"github.com/mark3labs/mcp-go/server"`.
- Backward compatibility: no tool renames; the `max_lines` parameter must keep working as an accepted alias when renamed to `max_chunks`. Results keep a text fallback; errors keep `IsError=true`.
- No new dependencies. gofmt/vet clean; `go test ./internal/cli/mcp/...` green after every task.
- gRPC streams are NOT resumable — bounding is a byte cap + explicit `truncated` signal + narrower-query guidance, NOT a resumable cursor. Say so in the PR body; do not fake a cursor.
- Progress notifications are best-effort: when no `progressToken` is supplied by the client, or no server is in context, `reportProgress` is a silent no-op. Never fail a tool because a progress send failed.
- `time.Now()` is allowed in this runtime code (not a workflow script).

---

## Task 1: Progress-notification helper + wiring

**Files:**
- Create: `go/internal/cli/mcp/progress.go`
- Create: `go/internal/cli/mcp/progress_test.go`
- Modify: `tools_os.go` (handleOSUpdate), `tools_provisioning.go` (handleProvisioningStart), `tools_cloud.go` (handleRun)

**Interfaces:**
- Produces: `progressToken(req mcpgo.CallToolRequest) any`; `reportProgress(ctx context.Context, token any, progress, total float64, message string)`.

- [ ] **Step 1: Write `progress.go`**

```go
package mcp

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// progressToken returns the client-supplied progress token for this request,
// or nil if the client did not request progress.
func progressToken(req mcpgo.CallToolRequest) any {
	if req.Params.Meta == nil {
		return nil
	}
	return req.Params.Meta.ProgressToken
}

// reportProgress emits an MCP progress notification for token. It is a silent
// no-op when token is nil or no server is bound to ctx, and it never returns an
// error — progress is best-effort telemetry, not part of a tool's contract.
func reportProgress(ctx context.Context, token any, progress, total float64, message string) {
	if token == nil {
		return
	}
	srv := server.ServerFromContext(ctx)
	if srv == nil {
		return
	}
	params := map[string]any{
		"progressToken": token,
		"progress":      progress,
	}
	if total > 0 {
		params["total"] = total
	}
	if message != "" {
		params["message"] = message
	}
	_ = srv.SendNotificationToClient(ctx, "notifications/progress", params)
}
```

- [ ] **Step 2: Write `progress_test.go`** (tests the no-op paths — the only paths unit-testable without a live transport)

```go
package mcp

import (
	"context"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestProgressToken_NilWhenNoMeta(t *testing.T) {
	if progressToken(mcpgo.CallToolRequest{}) != nil {
		t.Error("expected nil token when no _meta")
	}
}

func TestReportProgress_NoTokenNoPanic(t *testing.T) {
	// nil token + no server in ctx must be a safe no-op.
	reportProgress(context.Background(), nil, 1, 2, "x")
}

func TestReportProgress_NoServerNoPanic(t *testing.T) {
	// non-nil token but no server bound to ctx must not panic.
	reportProgress(context.Background(), "tok-1", 1, 2, "x")
}
```

- [ ] **Step 3: Run — verify fail** (`undefined: progressToken`/`reportProgress`). `go test ./internal/cli/mcp/... -run 'TestProgress|TestReportProgress' -v`
- [ ] **Step 4: Verify pass** after writing progress.go.
- [ ] **Step 5: Wire into `handleOSUpdate`** (`tools_os.go`). Capture the token once and emit on each progress frame. In the loop's `UpdateOSResponse_Progress_` case, after `sb.WriteString(...)`, add `reportProgress(ctx, tok, float64(p.GetPercent()), 100, fmt.Sprintf("[%s] %d%%", p.GetPhase(), p.GetPercent()))`. Define `tok := progressToken(req)` just before the loop. (Handler already receives `req`.)
- [ ] **Step 6: Wire into `handleProvisioningStart`** (`tools_provisioning.go`). `tok := progressToken(req)` after param validation; in the `case <-ticker.C:` branch, before/after the IsProvisioned call, emit `reportProgress(ctx, tok, 0, 0, "waiting for device to finish provisioning…")` (indeterminate — progress 0, no total).
- [ ] **Step 7: Wire into `handleRun`** (`tools_cloud.go`). `tok := progressToken(req)`; emit `reportProgress(ctx, tok, 0, 0, "running wendy…")` right before `cmd.CombinedOutput()`, and `reportProgress(ctx, tok, 1, 1, "done")` right after it returns (before building the result). Keep all existing behavior.
- [ ] **Step 8: Run full suite** `go test ./internal/cli/mcp/...` → green. `gofmt -w` changed files.
- [ ] **Step 9: Commit** `feat(mcp): emit MCP progress notifications for os_update, provisioning_start, run`

**Testing note to record in report:** the send path (token present + server in ctx) is not unit-testable without a live stdio session; only the no-op guards are covered. State this honestly.

---

## Task 2: Output bounding (byte cap + truncation signal + max_chunks rename)

**Files:**
- Modify: `results.go` (+ `results_test.go`) — add `clampStructured`.
- Modify: `tools_telemetry.go`, `tools_container.go` (attach + start), `tools_cloud.go` (run).

**Interfaces:**
- Produces: `okResultBounded(v any, maxBytes int) *mcpgo.CallToolResult` — like `okResult`, but if the JSON text fallback would exceed `maxBytes`, returns a truncation envelope instead.
- Produces: `intParamAlias(req, primary, alias string, def int) int` (added to `server.go` helpers) — reads `primary`, else `alias`, else `def`.

- [ ] **Step 1: Write failing test** in `results_test.go`

```go
func TestOkResultBounded_TruncatesOversize(t *testing.T) {
	big := make([]string, 0, 1000)
	for i := 0; i < 1000; i++ {
		big = append(big, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	}
	r := okResultBounded(big, 200)
	if r.IsError {
		t.Fatal("truncation is not an error result")
	}
	sc, ok := r.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("expected truncation envelope map, got %T", r.StructuredContent)
	}
	if sc["truncated"] != true {
		t.Errorf("expected truncated=true, got %v", sc["truncated"])
	}
}

func TestOkResultBounded_PassesSmall(t *testing.T) {
	r := okResultBounded(map[string]any{"k": "v"}, 100000)
	sc, _ := r.StructuredContent.(map[string]any)
	if sc["truncated"] == true {
		t.Error("small payload should not be truncated")
	}
}
```

- [ ] **Step 2: Run — verify fail.**
- [ ] **Step 3: Add `okResultBounded` to `results.go`**

```go
// okResultBounded is okResult with a byte ceiling on the JSON text fallback.
// gRPC streams are not resumable, so when the payload exceeds maxBytes we do
// not paginate — we return a truncation envelope telling the agent to narrow
// the query. maxBytes <= 0 disables the cap (behaves as okResult).
func okResultBounded(v any, maxBytes int) *mcpgo.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return errResultf(errCodeInternal, "marshaling result: %s", err.Error())
	}
	if maxBytes > 0 && len(b) > maxBytes {
		env := map[string]any{
			"truncated": true,
			"max_bytes": maxBytes,
			"bytes":     len(b),
			"note":      "output exceeded max_bytes; narrow the query (reduce max_batches / max_chunks, add filters, or raise max_bytes)",
		}
		eb, _ := json.MarshalIndent(env, "", "  ")
		return mcpgo.NewToolResultStructured(env, string(eb))
	}
	return mcpgo.NewToolResultStructured(v, string(b))
}
```

- [ ] **Step 4: Add `intParamAlias` to `server.go`** (near `intParam`):

```go
// intParamAlias reads primary, falling back to alias, then defaultVal.
func intParamAlias(req mcpgo.CallToolRequest, primary, alias string, defaultVal int) int {
	if v := req.GetInt(primary, -1<<62); v != -1<<62 {
		return v
	}
	return req.GetInt(alias, defaultVal)
}
```

- [ ] **Step 5: Apply bounding.**
  - `telemetry_logs/metrics/traces`: add optional `max_bytes` number param (default 100000) to each tool registration; in the handlers, replace `okResult(parts)` with `okResultBounded(parts, intParam(req, "max_bytes", 100000))`.
  - `container_attach` (`tools_container.go`): register a new `max_chunks` number param (description "max output chunks") **and keep `max_lines`**; change `maxChunks := intParam(req, "max_lines", 100)` → `maxChunks := intParamAlias(req, "max_chunks", "max_lines", 100)`. Wrap its final data result in `okResultBounded(..., intParam(req, "max_bytes", 100000))` (add `max_bytes` param). Update the `max_lines` description to note it is a deprecated alias for `max_chunks`.
  - `container_start`: add optional `max_chunks` number param (default 200) and use `intParam(req, "max_chunks", 200)` for the loop cap instead of the hardcoded 200; add `max_bytes` bound on its result.
  - `run` (`handleRun`): add optional `max_bytes` param (default 100000); the success text blob → if over cap, return the truncation envelope (reuse `okResultBounded` on `map[string]any{"output": text}` OR clamp the string and note truncation — pick the string-clamp: if `len(text) > maxBytes`, set `text = text[:maxBytes]` and append a truncation note line, still `okText`). Keep failure branches unchanged.
- [ ] **Step 6: Tests.** Add a `container_start` test asserting `max_chunks` alias works if a fake supports it; otherwise rely on the results_test coverage + full suite. Run `go test ./internal/cli/mcp/...` → green.
- [ ] **Step 7: Commit** `feat(mcp): bound tool output by bytes + rename max_lines→max_chunks (alias kept)`

---

## Task 3: Proxy diagnostics

**Files:**
- Create: `go/internal/cli/mcp/diagnostics.go` (+ `diagnostics_test.go`)
- Modify: `server.go` (add field + record calls, replace stderr-only warnings), `tools_guide.go` or wherever `wendy://` resources register (add `wendy://diagnostics`), `tools_status.go` (add proxy diag to wendy_status structured content).

**Interfaces:**
- Produces on `mcpServer`: `recordProxyDiag(appName, stage string, err error)`; `proxyDiagnostics() []proxyDiagEntry`.
- `type proxyDiagEntry struct { AppName, Stage, Error string; Time string }`.

- [ ] **Step 1: Write failing test** in `diagnostics_test.go`

```go
func TestProxyDiag_RecordAndRead(t *testing.T) {
	s := New(&config.Config{}, nil)
	s.recordProxyDiag("paperless", "initialize", errors.New("boom"))
	d := s.proxyDiagnostics()
	if len(d) != 1 || d[0].AppName != "paperless" || d[0].Stage != "initialize" || d[0].Error != "boom" {
		t.Fatalf("unexpected diagnostics: %+v", d)
	}
}
```

- [ ] **Step 2: Run — verify fail.**
- [ ] **Step 3: Write `diagnostics.go`**

```go
package mcp

type proxyDiagEntry struct {
	AppName string `json:"app_name"`
	Stage   string `json:"stage"`
	Error   string `json:"error"`
	Time    string `json:"time"`
}

func (s *mcpServer) recordProxyDiag(appName, stage string, err error) {
	if err == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proxyDiag = append(s.proxyDiag, proxyDiagEntry{
		AppName: appName,
		Stage:   stage,
		Error:   err.Error(),
		Time:    time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *mcpServer) proxyDiagnostics() []proxyDiagEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]proxyDiagEntry, len(s.proxyDiag))
	copy(out, s.proxyDiag)
	return out
}
```

Add `proxyDiag []proxyDiagEntry` to the `mcpServer` struct in `server.go` and import `time` there if not already.

- [ ] **Step 4: Replace stderr-only warnings** in `server.go` `connectContainerMCPTools`/`registerContainerMCPTools`: at each of the six `fmt.Fprintf(os.Stderr, "Warning: ...")` sites, add `s.recordProxyDiag(appName, "<stage>", err)` (stages: "list-containers", "read-container-list", "proxy", "client", "initialize", "list-tools"). Keep one concise stderr line for humans. For the two list-container sites that have no appName, use `""`.
- [ ] **Step 5: Expose.** Register a `wendy://diagnostics` resource (mirror `registerGuideResource`) returning `okResult`-style JSON of `s.proxyDiagnostics()` — as a resource, marshal to JSON text. Also add `"proxy_diagnostics": s.proxyDiagnostics()` to the `wendy_status` structured-content map in `tools_status.go`.
- [ ] **Step 6: Tests + full suite** green. gofmt.
- [ ] **Step 7: Commit** `feat(mcp): surface container-MCP proxy failures via diagnostics resource + wendy_status`

---

## Self-Review (PR2)

- #3: byte cap + `truncated` envelope + `max_chunks` rename with `max_lines` alias — applied to telemetry/attach/start/run; no fake cursor.
- #4: `reportProgress` best-effort, wired into the three long ops; no-op guards tested; send path documented as manual-verify.
- #5: proxy failures recorded + exposed; stderr retained for humans.
- Backward compat: `max_lines` still accepted; no renames; text fallbacks preserved.
