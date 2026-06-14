# MCP Apps for WendyOS — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the Wendy MCP server speak the MCP Apps extension so WendyOS renders as an interactive in-chat app (dashboard/controls/launcher), add an HTTP transport, and pass through container apps' own UIs.

**Architecture:** A new transport-agnostic `appsui` helper attaches `_meta.ui` to tools/results and serves `ui://` HTML resources. Existing Wendy tools gain `_meta.ui` annotations + structured view data. `wendy mcp serve` keeps stdio by default and gains `--http`. The existing byte-level container MCP proxy is extended (CLI-side) to forward resources and namespace container `ui://` URIs. Tools gain an optional `device` arg backed by a per-device connection cache.

**Tech Stack:** Go, `github.com/mark3labs/mcp-go v0.54.0` (`server`, `mcp`, `client`), `go:embed`, Cobra, gRPC; embedded HTML/CSS/JS bundle (vanilla JS, postMessage `ui/` dialect).

**Reference:** Design spec `Documentation/2026-06-14-mcp-apps-wendyos-design.md`. Approved UI mockups in `.superpowers/brainstorm/*/content/` (`wendyos-app-dashboard-v2.html`, `wendyos-controls-apps.html`) — source the visual markup/styles from these.

---

## File Structure

- **Create** `go/internal/cli/mcp/appsui/appsui.go` — `WithUI`, `ResultWithUI`, `RegisterUIResource`, `UIResourceOptions`, `uiMeta`. The single place the `_meta.ui` wire shape lives.
- **Create** `go/internal/cli/mcp/appsui/appsui_test.go` — unit tests for the above.
- **Create** `go/internal/cli/mcp/appsui/web/wendy-app.html` — the embedded adaptive UI bundle (HTML+CSS+JS in one file).
- **Create** `go/internal/cli/mcp/appsui/web.go` — `go:embed` of `web/wendy-app.html`, exposes `WendyAppHTML []byte`.
- **Modify** `go/internal/cli/mcp/server.go` — register the UI resource; split `Start` into `buildServer` + `Serve`; per-device connection cache; resource passthrough.
- **Create** `go/internal/cli/mcp/ui_views.go` — `dashboardData`, `controlsData`, launcher helpers + the tool→view annotation wiring.
- **Create** `go/internal/cli/mcp/ui_views_test.go` — view-data + annotation tests.
- **Create** `go/internal/cli/mcp/tools_apps.go` — new `apps_list` and `app_open` tools.
- **Create** `go/internal/cli/mcp/tools_apps_test.go`.
- **Modify** `go/internal/cli/mcp/server.go` (`connectContainerMCPTools`) + **create** `go/internal/cli/mcp/passthrough.go` — forward `resources/list`/`resources/read`, namespace `ui://` URIs, preserve `_meta.ui` on proxied tools.
- **Create** `go/internal/cli/mcp/passthrough_test.go`.
- **Modify** `go/internal/cli/commands/mcp.go` — `--http`, `--addr`, `--token` flags.
- **Modify** `go/internal/cli/mcp/server.go` (`mcpServer` connection fields) + **create** `go/internal/cli/mcp/devices.go` — keyed connection cache + `resolveConn(ctx, req)` honoring an optional `device` arg.
- **Create** `go/internal/cli/mcp/devices_test.go`.

Run all Go commands from `/Users/joannisorlandos/git/wendy/wendyos/go`.

---

## Task 1: `appsui` helper package

**Files:**
- Create: `go/internal/cli/mcp/appsui/appsui.go`
- Test: `go/internal/cli/mcp/appsui/appsui_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/mcp/appsui/appsui_test.go
package appsui

import (
	"encoding/json"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

func TestWithUISetsMetaResourceUri(t *testing.T) {
	tool := WithUI(mcpgo.NewTool("demo"), "ui://wendy/app")
	if tool.Meta == nil || tool.Meta.AdditionalFields["ui"] == nil {
		t.Fatalf("expected _meta.ui to be set")
	}
	ui := tool.Meta.AdditionalFields["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://wendy/app" {
		t.Fatalf("got resourceUri %v", ui["resourceUri"])
	}
}

func TestResultWithUISetsViewAndData(t *testing.T) {
	res := ResultWithUI(mcpgo.NewToolResultText("ok"), "ui://wendy/app", "dashboard", map[string]any{"x": 1})
	sc := res.StructuredContent.(map[string]any)
	if sc["view"] != "dashboard" {
		t.Fatalf("got view %v", sc["view"])
	}
	// _meta.ui must round-trip through JSON as a top-level _meta.ui.resourceUri.
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	meta := raw["_meta"].(map[string]any)
	ui := meta["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://wendy/app" {
		t.Fatalf("got resourceUri %v", ui["resourceUri"])
	}
}

func TestUIMetaIncludesCSPAndPermissions(t *testing.T) {
	m := uiMeta("ui://x", &UIResourceOptions{CSP: []string{"https://cdn.example"}, Permissions: []string{"camera"}})
	if got := m["csp"].([]string)[0]; got != "https://cdn.example" {
		t.Fatalf("csp = %v", got)
	}
	if got := m["permissions"].([]string)[0]; got != "camera" {
		t.Fatalf("permissions = %v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/mcp/appsui/ -run TestWithUISetsMetaResourceUri -v`
Expected: FAIL — package/`WithUI` undefined.

- [ ] **Step 3: Write minimal implementation**

```go
// go/internal/cli/mcp/appsui/appsui.go
// Package appsui implements the MCP Apps extension wire shape (ui:// resources
// and _meta.ui annotations) for the Wendy MCP server. It is the single place
// the extension's metadata format is defined.
package appsui

import (
	"context"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// UIResourceOptions configures a ui:// resource.
type UIResourceOptions struct {
	// CSP lists external origins the app may load resources from.
	CSP []string
	// Permissions lists host capabilities the app requests (e.g. "camera").
	Permissions []string
	// Description is shown to hosts listing resources.
	Description string
}

// uiMeta builds the _meta.ui object for a resource URI.
func uiMeta(resourceURI string, opts *UIResourceOptions) map[string]any {
	ui := map[string]any{"resourceUri": resourceURI}
	if opts != nil {
		if len(opts.CSP) > 0 {
			ui["csp"] = opts.CSP
		}
		if len(opts.Permissions) > 0 {
			ui["permissions"] = opts.Permissions
		}
	}
	return ui
}

func ensureMeta(m *mcpgo.Meta) *mcpgo.Meta {
	if m == nil {
		m = &mcpgo.Meta{}
	}
	if m.AdditionalFields == nil {
		m.AdditionalFields = map[string]any{}
	}
	return m
}

// WithUI annotates a tool so hosts render the given ui:// resource for it.
func WithUI(tool mcpgo.Tool, resourceURI string) mcpgo.Tool {
	tool.Meta = ensureMeta(tool.Meta)
	tool.Meta.AdditionalFields["ui"] = uiMeta(resourceURI, nil)
	return tool
}

// ResultWithUI annotates a tool result with the ui:// resource to render plus
// the view name and data the iframe should display.
func ResultWithUI(res *mcpgo.CallToolResult, resourceURI, view string, data any) *mcpgo.CallToolResult {
	res.Meta = ensureMeta(res.Meta)
	res.Meta.AdditionalFields["ui"] = uiMeta(resourceURI, nil)
	res.StructuredContent = map[string]any{"view": view, "data": data}
	return res
}

// RegisterUIResource serves html at resourceURI as text/html, recording any
// CSP/permissions in the resource's _meta.ui for hosts that read them there.
func RegisterUIResource(srv *server.MCPServer, resourceURI, name string, html []byte, opts *UIResourceOptions) {
	desc := "WendyOS interactive app UI"
	if opts != nil && opts.Description != "" {
		desc = opts.Description
	}
	res := mcpgo.NewResource(resourceURI, name,
		mcpgo.WithResourceDescription(desc),
		mcpgo.WithMIMEType("text/html"),
	)
	res.Meta = &mcpgo.Meta{AdditionalFields: map[string]any{"ui": uiMeta(resourceURI, opts)}}
	srv.AddResource(res, func(_ context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
		return []mcpgo.ResourceContents{
			mcpgo.TextResourceContents{URI: req.Params.URI, MIMEType: "text/html", Text: string(html)},
		}, nil
	})
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/cli/mcp/appsui/ -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Commit**

```bash
git add go/internal/cli/mcp/appsui/appsui.go go/internal/cli/mcp/appsui/appsui_test.go
git commit -m "feat(mcp): add appsui helper for MCP Apps _meta.ui annotations"
```

---

## Task 2: Embed the Wendy adaptive UI bundle and register it

**Files:**
- Create: `go/internal/cli/mcp/appsui/web/wendy-app.html`
- Create: `go/internal/cli/mcp/appsui/web.go`
- Modify: `go/internal/cli/mcp/server.go` (add `registerWendyAppUI`, call it in `Start`)
- Test: `go/internal/cli/mcp/appsui/appsui_test.go` (add embed test) and `go/internal/cli/mcp/server_test.go`

- [ ] **Step 1: Write the failing test (embed non-empty + registered resource)**

Add to `go/internal/cli/mcp/appsui/appsui_test.go`:

```go
func TestWendyAppHTMLEmbedded(t *testing.T) {
	if len(WendyAppHTML) < 100 {
		t.Fatalf("embedded UI bundle looks empty: %d bytes", len(WendyAppHTML))
	}
	if !bytesContains(WendyAppHTML, []byte("ui/initialize")) {
		t.Fatalf("UI bundle missing postMessage ui/ bridge")
	}
}

func bytesContains(h, n []byte) bool { return bytes.Contains(h, n) }
```

Add `"bytes"` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/mcp/appsui/ -run TestWendyAppHTMLEmbedded -v`
Expected: FAIL — `WendyAppHTML` undefined.

- [ ] **Step 3: Create the UI bundle**

Create `go/internal/cli/mcp/appsui/web/wendy-app.html`. Use the **approved mockup markup/styles** from `.superpowers/brainstorm/*/content/wendyos-app-dashboard-v2.html` (dashboard) and `wendyos-controls-apps.html` (controls + launcher) for the visual layer. Replace the static demo content with three view-render functions driven by data, and add the postMessage bridge below. The bundle MUST contain a `<script>` implementing exactly this bridge (the test asserts `ui/initialize` is present):

```html
<script>
  // --- MCP Apps postMessage bridge (host <-> iframe) ---
  let _seq = 0;
  const _pending = {};
  function _rpc(method, params) {
    return new Promise((resolve, reject) => {
      const id = ++_seq;
      _pending[id] = { resolve, reject };
      parent.postMessage({ jsonrpc: "2.0", id, method, params }, "*");
    });
  }
  window.addEventListener("message", (e) => {
    const msg = e.data || {};
    if (msg.id && _pending[msg.id]) {
      const p = _pending[msg.id]; delete _pending[msg.id];
      msg.error ? p.reject(msg.error) : p.resolve(msg.result);
      return;
    }
    // Host pushes fresh tool result data into the app.
    if (msg.method === "ui/render" || msg.method === "ui/data") {
      render(msg.params && (msg.params.structuredContent || msg.params));
    }
  });
  // Ask the host to invoke a Wendy tool (Stop, Update, Open app, switch device).
  function callTool(name, args) { return _rpc("tools/call", { name, arguments: args || {} }); }
  // Re-render after an action by re-reading the result it returns.
  async function act(name, args) {
    try { const r = await callTool(name, args); if (r && r.structuredContent) render(r.structuredContent); }
    catch (err) { console.error("tool call failed", err); }
  }

  // --- View router: structuredContent = { view, data } ---
  let _device = null;
  function render(sc) {
    if (!sc) return;
    const view = sc.view || "dashboard";
    const data = sc.data || {};
    if (data.device) _device = data.device;
    document.body.dataset.view = view;
    if (view === "dashboard") renderDashboard(data);
    else if (view === "controls") renderControls(data);
    else if (view === "apps") renderApps(data);
  }
  // renderDashboard/renderControls/renderApps: populate the DOM from `data`,
  // wiring buttons to act(...) e.g.
  //   stopBtn.onclick = () => act("container_stop", { device: _device, name: c.name });
  //   updateBtn.onclick = () => act("os_update", { device: _device });
  //   openBtn.onclick = () => act("app_open", { device: _device, app: a.name });
  //   deviceSelect.onchange = () => act("device_info", { device: deviceSelect.value });

  // Handshake: announce readiness; host replies with the initial result data.
  _rpc("ui/initialize", { capabilities: {} }).then(render).catch(() => {});
</script>
```

> Implementation note: keep the three `render*` functions data-driven over the
> shapes defined in Task 4 (`dashboardData`, `controlsData`) and Task 5
> (`appsList`). The mockup files are the source of truth for markup and CSS.

- [ ] **Step 4: Create the embed**

```go
// go/internal/cli/mcp/appsui/web.go
package appsui

import _ "embed"

//go:embed web/wendy-app.html
var WendyAppHTML []byte
```

- [ ] **Step 5: Register the resource in the server**

In `go/internal/cli/mcp/server.go`, add an import for the package and a method, then call it from `Start` right after `registerGuideResource(srv)`:

```go
// import block: add
//   "github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"

// WendyAppURI is the ui:// resource for Wendy's adaptive in-chat app.
const WendyAppURI = "ui://wendy/app"

func (s *mcpServer) registerWendyAppUI(srv *server.MCPServer) {
	appsui.RegisterUIResource(srv, WendyAppURI, "WendyOS App", appsui.WendyAppHTML, &appsui.UIResourceOptions{
		Description: "Adaptive WendyOS app: device dashboard, controls, and app launcher.",
	})
}
```

In `Start`, after line `s.registerGuideResource(srv)`:

```go
	s.registerWendyAppUI(srv)
```

- [ ] **Step 6: Add a server-level test that the resource is registered**

Add to `go/internal/cli/mcp/server_test.go` (mirror existing test style in that file):

```go
func TestRegisterWendyAppUIServesHTML(t *testing.T) {
	s := New(nil, nil)
	srv := server.NewMCPServer("wendy", "test", server.WithResourceCapabilities(true, false))
	s.registerWendyAppUI(srv)
	// Read the resource back through the server's handler registry.
	res, err := srv.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"ui://wendy/app"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !bytes.Contains(b, []byte("text/html")) {
		t.Fatalf("expected text/html resource, got %s", b)
	}
}
```

Ensure `server_test.go` imports `bytes`, `context`, `encoding/json`, and `github.com/mark3labs/mcp-go/server`.

- [ ] **Step 7: Run tests**

Run: `go test ./internal/cli/mcp/... -run 'WendyApp' -v`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/mcp/appsui/web go/internal/cli/mcp/appsui/web.go go/internal/cli/mcp/server.go go/internal/cli/mcp/server_test.go go/internal/cli/mcp/appsui/appsui_test.go
git commit -m "feat(mcp): embed and register the WendyOS adaptive UI resource"
```

---

## Task 3: Dashboard view — annotate status/container tools (thin slice)

**Files:**
- Create: `go/internal/cli/mcp/ui_views.go`
- Modify: `go/internal/cli/mcp/tools_status.go` (annotate `wendy_status`, attach result UI)
- Test: `go/internal/cli/mcp/ui_views_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/mcp/ui_views_test.go
package mcp

import (
	"encoding/json"
	"testing"
)

func TestDashboardDataShape(t *testing.T) {
	d := dashboardData("jetson-orin-01", "cloud", map[string]any{"cpu": 38})
	b, _ := json.Marshal(d)
	var got map[string]any
	_ = json.Unmarshal(b, &got)
	if got["device"] != "jetson-orin-01" {
		t.Fatalf("device = %v", got["device"])
	}
	if got["connection_type"] != "cloud" {
		t.Fatalf("connection_type = %v", got["connection_type"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/mcp/ -run TestDashboardDataShape -v`
Expected: FAIL — `dashboardData` undefined.

- [ ] **Step 3: Implement the view-data helper**

```go
// go/internal/cli/mcp/ui_views.go
package mcp

// dashboardData assembles the structuredContent.data payload the iframe renders
// for the Dashboard view.
func dashboardData(device, connType string, stats map[string]any) map[string]any {
	return map[string]any{
		"device":          device,
		"connection_type": connType,
		"stats":           stats,
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cli/mcp/ -run TestDashboardDataShape -v`
Expected: PASS.

- [ ] **Step 5: Annotate `wendy_status` with the UI**

In `go/internal/cli/mcp/tools_status.go`, wrap the tool registration and result. Replace `registerStatusTools` and the two `return` lines in `handleWendyStatus`:

```go
// add import: "github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"

func (s *mcpServer) registerStatusTools(srv *server.MCPServer) {
	tool := appsui.WithUI(mcpgo.NewTool("wendy_status",
		mcpgo.WithDescription("Return current MCP session connection state and a plain-English suggested next step. Call this first to orient yourself."),
	), WendyAppURI)
	srv.AddTool(tool, s.handleWendyStatus)
}
```

In `handleWendyStatus`, for the connected branch change:

```go
	b, _ := json.Marshal(out)
	res := mcpgo.NewToolResultText(string(b))
	return appsui.ResultWithUI(res, WendyAppURI, "dashboard", dashboardData(host, connType, nil)), nil
```

Leave the not-connected branch returning plain text (no device to render).

- [ ] **Step 6: Run the package tests**

Run: `go test ./internal/cli/mcp/ -run 'Status|Dashboard' -v`
Expected: PASS. Confirm existing `tools_status_test.go` still passes (the text content is unchanged; only `_meta`/`structuredContent` were added).

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mcp/ui_views.go go/internal/cli/mcp/ui_views_test.go go/internal/cli/mcp/tools_status.go
git commit -m "feat(mcp): render dashboard view from wendy_status via _meta.ui"
```

---

## Task 4: Controls view — annotate action tools

**Files:**
- Modify: `go/internal/cli/mcp/ui_views.go` (add `controlsData`)
- Modify: `go/internal/cli/mcp/tools_container.go`, `tools_wifi.go`, `tools_os.go`, `tools_device.go` (annotate + result UI for `container_start`, `container_stop`, `wifi_connect`, `wifi_disconnect`, `os_update`, `device_set_default`)
- Test: `go/internal/cli/mcp/ui_views_test.go`

- [ ] **Step 1: Write the failing test**

Add to `ui_views_test.go`:

```go
func TestControlsDataIncludesContainers(t *testing.T) {
	d := controlsData("dev1", []containerState{{Name: "cam", Running: true}})
	cs := d["containers"].([]containerState)
	if len(cs) != 1 || cs[0].Name != "cam" || !cs[0].Running {
		t.Fatalf("containers = %+v", cs)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/mcp/ -run TestControlsDataIncludesContainers -v`
Expected: FAIL — `controlsData`/`containerState` undefined.

- [ ] **Step 3: Implement helpers**

Add to `go/internal/cli/mcp/ui_views.go`:

```go
// containerState is the per-container row the Controls view renders.
type containerState struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
	CPU     string `json:"cpu,omitempty"`
}

// controlsData assembles the Controls view payload.
func controlsData(device string, containers []containerState) map[string]any {
	return map[string]any{
		"device":     device,
		"containers": containers,
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cli/mcp/ -run TestControlsDataIncludesContainers -v`
Expected: PASS.

- [ ] **Step 5: Annotate the action tools**

For each of `container_start`, `container_stop`, `wifi_connect`, `wifi_disconnect`, `os_update`, `device_set_default`: wrap the `mcpgo.NewTool(...)` in `appsui.WithUI(..., WendyAppURI)` at registration, and at the end of each handler's success path replace the bare `return mcpgo.NewToolResultText(msg), nil` with:

```go
	res := mcpgo.NewToolResultText(msg)
	return appsui.ResultWithUI(res, WendyAppURI, "controls", controlsData(deviceLabel, nil)), nil
```

where `deviceLabel` is the connection host already available in the handler (use `s.GetConn().Host`, guarded for nil). Add the `appsui` import to each modified file. For handlers that already have the live container list in scope, pass it through `controlsData` instead of `nil`; otherwise `nil` is acceptable (the iframe falls back to its last data).

- [ ] **Step 6: Run package tests**

Run: `go test ./internal/cli/mcp/ -v`
Expected: PASS (existing handler text assertions unaffected).

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mcp/ui_views.go go/internal/cli/mcp/ui_views_test.go go/internal/cli/mcp/tools_container.go go/internal/cli/mcp/tools_wifi.go go/internal/cli/mcp/tools_os.go go/internal/cli/mcp/tools_device.go
git commit -m "feat(mcp): render controls view from action tools via _meta.ui"
```

---

## Task 5: Apps launcher — `apps_list` and `app_open` tools

**Files:**
- Create: `go/internal/cli/mcp/tools_apps.go`
- Modify: `go/internal/cli/mcp/server.go` (`Start` registers `registerAppsTools`)
- Test: `go/internal/cli/mcp/tools_apps_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/mcp/tools_apps_test.go
package mcp

import "testing"

func TestAppEntryClassification(t *testing.T) {
	// hasUI true only when the container exposes a ui:// resource.
	e := appEntry{Name: "cam", HasMCP: true, HasUI: true}
	if e.status() != "ui" {
		t.Fatalf("status = %q", e.status())
	}
	if (appEntry{Name: "api", HasMCP: true, HasUI: false}).status() != "tools" {
		t.Fatalf("tools-only misclassified")
	}
	if (appEntry{Name: "log", HasMCP: false}).status() != "none" {
		t.Fatalf("no-mcp misclassified")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/mcp/ -run TestAppEntryClassification -v`
Expected: FAIL — `appEntry` undefined.

- [ ] **Step 3: Implement the tools**

```go
// go/internal/cli/mcp/tools_apps.go
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/wendylabsinc/wendy/go/internal/cli/mcp/appsui"
	agentpb "github.com/wendylabsinc/wendy/go/proto/gen/agentpb"
)

// appEntry is one row in the Apps launcher.
type appEntry struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	HasMCP  bool   `json:"hasMcp"`
	HasUI   bool   `json:"hasUi"`
}

// status maps an entry to one of the launcher's three states.
func (e appEntry) status() string {
	switch {
	case e.HasMCP && e.HasUI:
		return "ui"
	case e.HasMCP:
		return "tools"
	default:
		return "none"
	}
}

func (s *mcpServer) registerAppsTools(srv *server.MCPServer) {
	list := appsui.WithUI(mcpgo.NewTool("apps_list",
		mcpgo.WithDescription("List the container apps on the connected device and whether each exposes an in-chat UI."),
		mcpgo.WithString("device", mcpgo.Description("Optional device name or host:port to target.")),
	), WendyAppURI)
	srv.AddTool(list, s.handleAppsList)

	open := appsui.WithUI(mcpgo.NewTool("app_open",
		mcpgo.WithDescription("Open a container app's own MCP UI inside the Wendy app."),
		mcpgo.WithString("app", mcpgo.Required(), mcpgo.Description("Container app name to open.")),
		mcpgo.WithString("device", mcpgo.Description("Optional device name or host:port to target.")),
	), WendyAppURI)
	srv.AddTool(open, s.handleAppOpen)
}

func (s *mcpServer) handleAppsList(ctx context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	conn := s.GetConn()
	if conn == nil {
		return errNotConnected(), nil
	}
	entries, err := s.listAppEntries(ctx, conn)
	if err != nil {
		return mcpgo.NewToolResultError(grpcErrString(err)), nil
	}
	b, _ := json.Marshal(entries)
	res := mcpgo.NewToolResultText(string(b))
	host := conn.Host
	if host == "" {
		host = "device"
	}
	return appsui.ResultWithUI(res, WendyAppURI, "apps", map[string]any{
		"device": host,
		"apps":   entries,
	}), nil
}

// listAppEntries enumerates running containers and flags UI capability. A
// container is UI-capable when its proxied MCP server exposes a ui:// resource;
// Task 8 replaces containerHasUI with a real probe cached during proxy setup.
func (s *mcpServer) listAppEntries(ctx context.Context, conn *grpcclient.AgentConnection) ([]appEntry, error) {
	stream, err := conn.ContainerService.ListContainers(ctx, &agentpb.ListContainersRequest{})
	if err != nil {
		return nil, err
	}
	var out []appEntry
	for {
		resp, err := stream.Recv()
		if err != nil {
			break
		}
		c := resp.GetContainer()
		if c == nil {
			continue
		}
		out = append(out, appEntry{
			Name:    c.GetAppName(),
			Version: c.GetAppVersion(),
			HasMCP:  c.GetMcpPort() > 0,
			HasUI:   s.containerHasUI(c.GetAppName(), c.GetMcpPort() > 0),
		})
	}
	return out, nil
}

// containerHasUI reports whether a container exposes a ui:// resource. This is a
// temporary heuristic (mirrors mcp availability); Task 8 replaces it with a
// probe cached during connectContainerMCPTools.
func (s *mcpServer) containerHasUI(_ string, hasMCP bool) bool {
	return hasMCP
}

func (s *mcpServer) handleAppOpen(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
	app := stringParam(req, "app")
	if app == "" {
		return mcpgo.NewToolResultError("app is required"), nil
	}
	uri := namespacedUIURI(app) // defined in Task 8 passthrough.go
	res := mcpgo.NewToolResultText(fmt.Sprintf("opening %s", app))
	return appsui.ResultWithUI(res, uri, "app", map[string]any{"app": app}), nil
}
```

> Imports needed: `grpcclient "github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"`.
> `namespacedUIURI` is introduced in Task 8 (`passthrough.go`). Because tasks
> commit sequentially, define a one-line stub now and let Task 8 replace it — see
> Step 4.

- [ ] **Step 4: Add a stub for the Task 8 symbol so this task compiles**

Create `go/internal/cli/mcp/passthrough.go` with only the namespacing constant +
`namespacedUIURI` (Task 8 adds the rest to the same file):

```go
package mcp

// uiAppPrefix namespaces container app ui:// resources surfaced through Wendy.
const uiAppPrefix = "ui://app/"

// namespacedUIURI builds the host-visible URI for an app's main UI entry point.
func namespacedUIURI(app string) string {
	return uiAppPrefix + sanitizeMCPPrefix(app) + "/main"
}
```

This is real code Task 8 builds on (not a throwaway shim) — Task 8 adds
`namespacedUIURI2`, `parseNamespacedUIURI`, and `rewriteToolUIMeta` to it.

- [ ] **Step 5: Register in `Start`**

In `server.go` `Start`, after `s.registerCloudTools(srv)`:

```go
	s.registerAppsTools(srv)
```

- [ ] **Step 6: Run tests**

Run: `go test ./internal/cli/mcp/ -run 'AppEntry|Apps' -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mcp/tools_apps.go go/internal/cli/mcp/passthrough.go go/internal/cli/mcp/tools_apps_test.go go/internal/cli/mcp/server.go
git commit -m "feat(mcp): add apps_list and app_open tools for the launcher view"
```

---

## Task 6: Local HTTP transport

**Files:**
- Modify: `go/internal/cli/mcp/server.go` (split `Start` into `buildServer` + `ServeStdio`/`ServeHTTP`)
- Modify: `go/internal/cli/commands/mcp.go` (`--http`, `--addr`, `--token` flags)
- Test: `go/internal/cli/mcp/server_test.go`

- [ ] **Step 1: Write the failing test**

Add to `server_test.go`:

```go
func TestBuildServerRegistersWendyAppResource(t *testing.T) {
	s := New(nil, nil)
	srv := s.buildServer(context.Background())
	res, err := srv.HandleMessage(context.Background(), []byte(`{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"ui://wendy/app"}}`))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if !bytes.Contains(b, []byte("text/html")) {
		t.Fatalf("buildServer did not register the UI resource: %s", b)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/mcp/ -run TestBuildServerRegistersWendyAppResource -v`
Expected: FAIL — `buildServer` undefined.

- [ ] **Step 3: Refactor `Start` into `buildServer` + serve methods**

Replace `Start` in `server.go` with:

```go
// buildServer constructs and fully registers the MCP server (transport-agnostic).
func (s *mcpServer) buildServer(ctx context.Context) *server.MCPServer {
	srv := server.NewMCPServer("wendy", version.Version,
		server.WithToolCapabilities(true),
		server.WithResourceCapabilities(true, false),
	)
	s.registerStatusTools(srv)
	s.registerGuideResource(srv)
	s.registerWendyAppUI(srv)
	s.registerDeviceTools(srv)
	s.registerContainerTools(srv)
	s.registerTelemetryTools(srv)
	s.registerWiFiTools(srv)
	s.registerBluetoothTools(srv)
	s.registerHardwareTools(srv)
	s.registerFileSyncTools(srv)
	s.registerProvisioningTools(srv)
	s.registerOSTools(srv)
	s.registerCloudTools(srv)
	s.registerAppsTools(srv)
	return srv
}

// Start registers all tools and serves MCP over stdio (default transport).
func (s *mcpServer) Start(ctx context.Context) error {
	srv := s.buildServer(ctx)
	cleanups := s.registerContainerMCPTools(ctx, srv)
	defer runCleanups(cleanups)
	return server.ServeStdio(srv)
}

// StartHTTP serves MCP over streamable HTTP at addr (e.g. "127.0.0.1:7777").
// token, if non-empty, is required as a Bearer token on every request.
func (s *mcpServer) StartHTTP(ctx context.Context, addr, token string) error {
	srv := s.buildServer(ctx)
	cleanups := s.registerContainerMCPTools(ctx, srv)
	defer runCleanups(cleanups)
	opts := []server.StreamableHTTPOption{}
	if token != "" {
		opts = append(opts, server.WithHTTPContextFunc(bearerCheck(token)))
	}
	httpSrv := server.NewStreamableHTTPServer(srv, opts...)
	return httpSrv.Start(addr)
}

func runCleanups(cleanups []func()) {
	for _, c := range cleanups {
		c()
	}
}
```

> Verify the exact option/middleware names against the installed library before
> finalizing: `go doc github.com/mark3labs/mcp-go/server.NewStreamableHTTPServer`
> and `go doc github.com/mark3labs/mcp-go/server.StreamableHTTPOption`. If the
> bearer hook signature differs, adapt `bearerCheck`. If no context hook exists,
> wrap `httpSrv` in a `http.Handler` that checks `Authorization` and mount it via
> `http.ListenAndServe(addr, handler)` using `httpSrv` (it implements `http.Handler`).

- [ ] **Step 4: Add the bearer check helper**

```go
// in server.go
func bearerCheck(token string) func(ctx context.Context, r *http.Request) context.Context {
	want := "Bearer " + token
	return func(ctx context.Context, r *http.Request) context.Context {
		if r.Header.Get("Authorization") != want {
			return context.WithValue(ctx, ctxKeyUnauthorized{}, true)
		}
		return ctx
	}
}

type ctxKeyUnauthorized struct{}
```

> If the library's context hook cannot reject requests on its own, prefer the
> `http.Handler` wrapper approach noted above instead of this hook, and drop
> `bearerCheck`. Add `"net/http"` to imports.

- [ ] **Step 5: Add CLI flags**

In `go/internal/cli/commands/mcp.go` `newMCPServeCmd`, add flags and branch:

```go
	var httpMode bool
	var httpAddr string
	var httpToken string
	// ... inside RunE, replace `return srv.Start(ctx)` with:
		if httpMode {
			fmt.Fprintf(os.Stderr, "wendy mcp: serving over HTTP at %s\n", httpAddr)
			return srv.StartHTTP(ctx, httpAddr, httpToken)
		}
		return srv.Start(ctx)
	// ... after existing flag:
	cmd.Flags().BoolVar(&httpMode, "http", false, "Serve over streamable HTTP instead of stdio")
	cmd.Flags().StringVar(&httpAddr, "addr", "127.0.0.1:7777", "HTTP listen address (with --http)")
	cmd.Flags().StringVar(&httpToken, "token", "", "Require this bearer token for HTTP requests (with --http)")
```

Update the command `Short`/`Long` to mention `--http`.

- [ ] **Step 6: Run tests + build**

Run: `go test ./internal/cli/mcp/ -run BuildServer -v && go build ./...`
Expected: PASS and a clean build.

- [ ] **Step 7: Manual smoke (optional but recommended)**

Run: `go run ./cmd/wendy mcp serve --http --addr 127.0.0.1:7777` in one shell; in another:
`curl -s -XPOST 127.0.0.1:7777 -H 'content-type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"resources/read","params":{"uri":"ui://wendy/app"}}'`
Expected: a JSON response containing `text/html`. (Adjust the cmd path to the repo's actual main package.)

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/mcp/server.go go/internal/cli/commands/mcp.go go/internal/cli/mcp/server_test.go
git commit -m "feat(mcp): add local streamable-HTTP transport to wendy mcp serve"
```

---

## Task 7: Optional `device` arg + per-device connection cache

**Files:**
- Modify: `go/internal/cli/mcp/server.go` (`mcpServer` gains a connection cache; keep `conn` as the active default)
- Create: `go/internal/cli/mcp/devices.go` (`resolveConn`)
- Test: `go/internal/cli/mcp/devices_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/mcp/devices_test.go
package mcp

import (
	"context"
	"testing"
)

func TestResolveConnFallsBackToActive(t *testing.T) {
	s := New(nil, nil)
	// No device arg, no active connection -> error, no panic.
	if _, err := s.resolveConn(context.Background(), ""); err == nil {
		t.Fatalf("expected error when nothing connected")
	}
}

func TestResolveConnUsesCacheByName(t *testing.T) {
	s := New(nil, nil)
	fake := &cachedConn{host: "dev1"}
	s.cacheConn("dev1", fake)
	got, err := s.resolveConn(context.Background(), "dev1")
	if err != nil {
		t.Fatal(err)
	}
	if got != fake {
		t.Fatalf("resolveConn returned wrong connection")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/mcp/ -run ResolveConn -v`
Expected: FAIL — `resolveConn`/`cacheConn`/`cachedConn` undefined.

- [ ] **Step 3: Implement the cache + resolver**

```go
// go/internal/cli/mcp/devices.go
package mcp

import (
	"context"
	"fmt"

	"github.com/wendylabsinc/wendy/go/internal/cli/grpcclient"
)

// cachedConn is the value stored per device key. It wraps a live agent
// connection; the host field is used as the cache key and UI label.
type cachedConn struct {
	host string
	conn *grpcclient.AgentConnection
}

func (s *mcpServer) cacheConn(key string, c *cachedConn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connCache == nil {
		s.connCache = map[string]*cachedConn{}
	}
	s.connCache[key] = c
}

// resolveConn returns the connection for `device`. Empty device falls back to
// the active connection. A device that names a cached connection reuses it; an
// uncached device that looks like an address is dialed on demand and cached.
func (s *mcpServer) resolveConn(ctx context.Context, device string) (*grpcclient.AgentConnection, error) {
	if device == "" {
		if c := s.GetConn(); c != nil {
			return c, nil
		}
		return nil, fmt.Errorf("no device connected — use device_connect first or pass a device argument")
	}
	s.mu.RLock()
	cached := s.connCache[device]
	s.mu.RUnlock()
	if cached != nil && cached.conn != nil {
		return cached.conn, nil
	}
	if s.connectFn == nil {
		return nil, fmt.Errorf("no connect function configured")
	}
	conn, err := s.connectFn(ctx, device)
	if err != nil {
		return nil, fmt.Errorf("connecting to %s: %w", device, err)
	}
	s.cacheConn(device, &cachedConn{host: conn.Host, conn: conn})
	return conn, nil
}
```

Add the cache field to `mcpServer` in `server.go`:

```go
	connCache map[string]*cachedConn
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cli/mcp/ -run ResolveConn -v`
Expected: PASS.

- [ ] **Step 5: Thread `device` through the UI-facing handlers**

In `handleAppsList`, `handleAppOpen`, and the Controls action handlers, replace `conn := s.GetConn()` with:

```go
	conn, err := s.resolveConn(ctx, stringParam(req, "device"))
	if err != nil {
		return mcpgo.NewToolResultError(err.Error()), nil
	}
```

and ensure each of those tools declares the optional `device` string param (Task 5 already did for apps tools; add `mcpgo.WithString("device", mcpgo.Description("Optional device name or host:port to target."))` to the Controls action tools and `device_info`/`container_list`/`container_stats`). The no-arg path is unchanged behavior.

- [ ] **Step 6: Run full package tests + build**

Run: `go test ./internal/cli/mcp/ -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 7: Commit**

```bash
git add go/internal/cli/mcp/devices.go go/internal/cli/mcp/devices_test.go go/internal/cli/mcp/server.go go/internal/cli/mcp/tools_container.go go/internal/cli/mcp/tools_wifi.go go/internal/cli/mcp/tools_os.go go/internal/cli/mcp/tools_device.go go/internal/cli/mcp/tools_apps.go
git commit -m "feat(mcp): optional device arg + per-device connection cache"
```

---

## Task 8: Container app UI passthrough

**Files:**
- Modify: `go/internal/cli/mcp/passthrough.go` (created in Task 5 — add URI rewrite/parse + `namespacedUIURI2`)
- Modify: `go/internal/cli/mcp/server.go` (`connectContainerMCPTools` — preserve `_meta.ui`, register resources; add `appHasUI` cache)
- Modify: `go/internal/cli/mcp/tools_apps.go` → replace `containerHasUI` heuristic with the cached probe
- Test: `go/internal/cli/mcp/passthrough_test.go`

- [ ] **Step 1: Write the failing test**

```go
// go/internal/cli/mcp/passthrough_test.go
package mcp

import "testing"

func TestNamespacedUIURIRoundTrip(t *testing.T) {
	got := namespacedUIURI2("my-detector", "ui://main")
	want := "ui://app/my_detector/main"
	if got != want {
		t.Fatalf("namespaced = %q want %q", got, want)
	}
	app, inner, ok := parseNamespacedUIURI(want)
	if !ok || app != "my_detector" || inner != "ui://main" {
		t.Fatalf("parse = (%q,%q,%v)", app, inner, ok)
	}
}

func TestRewriteToolUIMetaIsNamespaced(t *testing.T) {
	tool := withRawUI("ui://main") // helper builds a tool with _meta.ui.resourceUri
	rewriteToolUIMeta(&tool, "my-detector")
	ui := tool.Meta.AdditionalFields["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://app/my_detector/main" {
		t.Fatalf("rewritten = %v", ui["resourceUri"])
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/mcp/ -run 'Namespaced|RewriteTool' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Extend `passthrough.go` with rewrite/parse helpers**

`passthrough.go` already exists from Task 5 with `uiAppPrefix` and
`namespacedUIURI`. **Add** the following to it (do not redefine the two existing
symbols), and add the `strings` + `mcpgo` imports:

```go
// (append to go/internal/cli/mcp/passthrough.go; add imports "strings" and
//  mcpgo "github.com/mark3labs/mcp-go/mcp")

// namespacedUIURI2 wraps an inner container ui:// URI under the app namespace.
func namespacedUIURI2(app, inner string) string {
	return uiAppPrefix + sanitizeMCPPrefix(app) + "/" + strings.TrimPrefix(inner, "ui://")
}

// parseNamespacedUIURI splits a namespaced URI back into (sanitizedApp, innerURI).
func parseNamespacedUIURI(uri string) (app, inner string, ok bool) {
	rest, found := strings.CutPrefix(uri, uiAppPrefix)
	if !found {
		return "", "", false
	}
	app, path, found := strings.Cut(rest, "/")
	if !found {
		return "", "", false
	}
	return app, "ui://" + path, true
}

// rewriteToolUIMeta namespaces a proxied tool's _meta.ui.resourceUri (if any).
func rewriteToolUIMeta(tool *mcpgo.Tool, app string) {
	if tool.Meta == nil || tool.Meta.AdditionalFields == nil {
		return
	}
	ui, ok := tool.Meta.AdditionalFields["ui"].(map[string]any)
	if !ok {
		return
	}
	if inner, ok := ui["resourceUri"].(string); ok && strings.HasPrefix(inner, "ui://") {
		ui["resourceUri"] = namespacedUIURI2(app, inner)
	}
}
```

Add the test helper to `passthrough_test.go`:

```go
func withRawUI(uri string) mcpgo.Tool {
	t := mcpgo.NewTool("demo")
	t.Meta = &mcpgo.Meta{AdditionalFields: map[string]any{"ui": map[string]any{"resourceUri": uri}}}
	return t
}
```
(import `mcpgo "github.com/mark3labs/mcp-go/mcp"` in the test).

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/cli/mcp/ -run 'Namespaced|RewriteTool' -v`
Expected: PASS.

- [ ] **Step 5: Forward resources + preserve `_meta.ui` in `connectContainerMCPTools`**

In `server.go`, in the proxied-tool loop, before `srv.AddTool(proxied, ...)` add:

```go
		rewriteToolUIMeta(&proxied, appName)
```

(this preserves and namespaces the tool's UI annotation). After the tool loop, list and register the container's resources:

```go
	// Forward the container's resources (including ui:// app resources) under
	// the app namespace so the host can fetch them through Wendy.
	resList, err := mcpCli.ListResources(ctx, mcpgo.ListResourcesRequest{})
	if err == nil {
		for _, r := range resList.Resources {
			inner := r.URI
			nsURI := namespacedUIURI2(appName, inner)
			nsRes := r
			nsRes.URI = nsURI
			origURI := inner
			srv.AddResource(nsRes, func(ctx context.Context, req mcpgo.ReadResourceRequest) ([]mcpgo.ResourceContents, error) {
				out, rerr := mcpCli.ReadResource(ctx, mcpgo.ReadResourceRequest{Params: mcpgo.ReadResourceParams{URI: origURI}})
				if rerr != nil {
					return nil, rerr
				}
				// Re-stamp the host-visible (namespaced) URI on returned contents.
				for i := range out.Contents {
					switch c := out.Contents[i].(type) {
					case mcpgo.TextResourceContents:
						c.URI = req.Params.URI
						out.Contents[i] = c
					case mcpgo.BlobResourceContents:
						c.URI = req.Params.URI
						out.Contents[i] = c
					}
				}
				return out.Contents, nil
			})
		}
	}
```

> Verify field/type names against the library: `ListResourcesRequest`,
> `ReadResourceRequest{Params: ReadResourceParams{URI}}`, `ReadResourceResult.Contents`,
> `TextResourceContents`/`BlobResourceContents`. Run `go doc github.com/mark3labs/mcp-go/client.Client.ListResources`
> and `.ReadResource`, and `go doc github.com/mark3labs/mcp-go/mcp.ReadResourceResult`.
> Adjust the result-shape handling to match (the guide handler already uses
> `req.Params.URI`, confirming that field path).

- [ ] **Step 6: Replace the `containerHasUI` heuristic with the cached probe**

The per-app UI capability is discovered for free during Step 5 (a container is
UI-capable iff it exposed a `ui://` resource). Cache it on `mcpServer` and have
`containerHasUI` read the cache instead of the `hasMCP` heuristic. Update
`connectContainerMCPTools` (Step 5) to call `s.setAppHasUI(appName, true)` inside
the resource loop whenever a registered resource URI starts with `ui://`.

In `tools_apps.go`, change `containerHasUI` to:

```go
func (s *mcpServer) containerHasUI(app string, _ bool) bool {
	return s.getAppHasUI(app)
}
```

```go
// server.go: add field
//   appHasUI map[string]bool
// in connectContainerMCPTools, when registering a ui:// resource:
//   s.setAppHasUI(appName, true)

func (s *mcpServer) setAppHasUI(app string, v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appHasUI == nil {
		s.appHasUI = map[string]bool{}
	}
	s.appHasUI[app] = v
}

func (s *mcpServer) getAppHasUI(app string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.appHasUI[app]
}
```

Then `containerHasUI` returns `s.getAppHasUI(app)` (ignoring the `hasMCP` shortcut). Remove the temporary shim behavior.

- [ ] **Step 7: Run full package tests + build**

Run: `go test ./internal/cli/mcp/... -v && go build ./...`
Expected: PASS, clean build.

- [ ] **Step 8: Commit**

```bash
git add go/internal/cli/mcp/passthrough.go go/internal/cli/mcp/passthrough_test.go go/internal/cli/mcp/server.go go/internal/cli/mcp/tools_apps.go
git commit -m "feat(mcp): pass through container app ui:// resources and _meta.ui"
```

---

## Task 9: End-to-end verification + docs

**Files:**
- Modify: `go/internal/cli/mcp/tools_guide.go` (mention the app UI + `--http`)
- Modify: `Documentation/2026-06-14-mcp-apps-wendyos-design.md` (mark Status: implemented)

- [ ] **Step 1: Update the guide resource text**

In `tools_guide.go` `guideText`, add a short section:

```
## In-chat app (MCP Apps)

Wendy exposes an interactive UI at the ui://wendy/app resource. Tools like
wendy_status, container_*, wifi_*, os_update, apps_list, and app_open render it
(dashboard / controls / launcher). Container apps with an mcp entitlement that
expose a ui:// resource appear in apps_list and open via app_open.
```

- [ ] **Step 2: Full test sweep**

Run: `go test ./... 2>&1 | tail -30`
Expected: PASS across the module (or only pre-existing unrelated failures).

- [ ] **Step 3: Manual host validation (thin slice)**

- stdio: run `wendy mcp setup` (or point Claude Desktop/Code at `wendy mcp serve`), connect a device, call `wendy_status`, confirm the dashboard renders in-chat.
- http: `wendy mcp serve --http`, connect from an HTTP-capable host, confirm the same.
- passthrough: deploy an example container app exposing a `ui://` resource (mcp entitlement), confirm it shows as UI-capable in `apps_list` and opens via `app_open`.

Record results in the PR description.

- [ ] **Step 4: Commit**

```bash
git add go/internal/cli/mcp/tools_guide.go Documentation/2026-06-14-mcp-apps-wendyos-design.md
git commit -m "docs(mcp): document the in-chat app and HTTP transport"
```

---

## Self-Review notes (for the implementer)

- **Library API drift is the top risk.** Three places call library APIs whose
  exact shapes must be confirmed with `go doc` before coding: streamable-HTTP
  server options/middleware (Task 6), and resource list/read result types
  (Task 8). The plan flags each inline. Confirm first; adapt the snippet.
- **Task ordering creates forward references by design.** Task 5 lands
  `containerHasUI` as a heuristic and `passthrough.go` with just `namespacedUIURI`;
  Task 8 replaces the heuristic with the cached probe and extends `passthrough.go`.
  Each task compiles and tests green on its own commit — verify that before moving on.
- **Backward compatibility:** every annotated tool still returns its original
  text content; `_meta.ui`/`structuredContent` are additive. Non-Apps hosts are
  unaffected. Keep it that way — never remove the text branch.
- **Cloud gateway is out of scope** (design §6). Ensure nothing here hard-codes a
  transport assumption: all registration goes through `buildServer`, which the
  future gateway will reuse.
