# MCP Apps for WendyOS — Design

**Date:** 2026-06-14
**Status:** Implemented (Tasks 1–8); cloud gateway deferred (see plan)
**Branch:** `jo.wdy-1546-mcp-setup-version-refresh`

## Summary

Render WendyOS as an interactive **MCP App** inside chat hosts (ChatGPT, Claude
Desktop, Claude Code, VS Code Copilot), and let each container app expose its
own MCP App through Wendy. This requires implementing the [MCP Apps
extension](https://modelcontextprotocol.io/extensions/apps/overview) (`ui://`
resources + tool/result `_meta.ui` annotations), adding an HTTP transport to the
existing MCP server, building Wendy's own adaptive UI, and passing through
container apps' UIs.

The work spans four pieces, layered so each is independently testable:

1. A transport-agnostic **MCP App-extension core** (tools + `ui://` resources +
   `_meta.ui`).
2. **Wendy's own adaptive UI** — one `ui://wendy/app` resource with three views
   (dashboard, controls, app launcher).
3. A **local HTTP transport** (`wendy mcp serve --http`) for desktop/dev-mode
   hosts.
4. **Container app UI passthrough** — the agent↔container proxy forwards `ui://`
   resources and `_meta.ui` metadata.

The **cloud HTTPS gateway** (for ChatGPT.com) is **out of scope for this spec's
implementation** but its contract is defined here; it gets its own spec + plan.

## Goals

- WendyOS exposes a single adaptive MCP App that renders in-chat.
- Container apps with an `mcp` entitlement that expose a `ui://` resource can be
  opened from within the Wendy app, rendered in the same host iframe.
- `wendy mcp serve` can serve over HTTP in addition to stdio, sharing one
  registration path.
- Tools accept an optional `device` target so one app instance can address
  multiple devices.

## Non-goals (this spec)

- Building the cloud gateway's OAuth 2.1 / dynamic client registration / hosting.
  Only its interface and the shared core it reuses are defined here.
- Authoring container-app UIs (that's app-author territory; we provide the
  passthrough + an example).
- Changing the host-side postMessage `ui/` dialect — that is the host's concern;
  our server only serves resources and annotates tools/results.

## Background — what already exists

(From `go/internal/cli/mcp/`, `go/internal/agent/`, `go/internal/shared/appconfig/`.)

- `wendy mcp serve` runs a `mark3labs/mcp-go v0.54.0` server over **stdio**
  (`go/internal/cli/mcp/server.go`).
- An **`mcp` entitlement** already exists: `{"type":"mcp","port":N}`
  (`appconfig.EntitlementMCP`). A container app can run its own MCP server; the
  agent proxies it **CLI → agent (gRPC `StreamMCP`) → container TCP**
  (`go/internal/agent/services/container_service.go`, `mcp/proxy.go`).
- Discovered container tools are re-registered into the main session as
  `app_name__tool_name` (`registerContainerMCPTools` in `server.go`). Only
  **tools** are proxied today — not resources, and `_meta` is not preserved.
- Container MCP port is stored on a containerd label `sh.wendy/mcp.port` and
  surfaced as `AppContainer.mcp_port` in proto.

### Library feasibility (verified against `mcp-go@v0.54.0`)

- `mcp.Meta` has `AdditionalFields map[string]any`, marshaled as `_meta`. Both
  `mcp.Tool.Meta` and `mcp.Result.Meta` (embedded in `CallToolResult`) exist →
  we can attach `_meta.ui = {resourceUri, csp, permissions}` to tools and
  results **without forking the library**.
- `server.NewStreamableHTTPServer` exists → HTTP transport is a drop-in.
- `mcp.TextResourceContents` + `MIMEType` → serving `ui://` HTML is direct; the
  server already registers resources (`registerGuideResource`).

## Architecture

```
                         ┌─────────────────────────────────────┐
   ChatGPT.com  ──OAuth──▶│  Cloud gateway (HTTPS MCP) [future] │
                         │   reuses core + cloud tunnel → device│
   Claude/desktop ──────▶│  Local HTTP (wendy mcp serve --http) │
   Claude Code ─stdio───▶│  stdio      (wendy mcp serve)        │
                         └──────────────┬──────────────────────┘
                                        │  transport-agnostic core
                                        ▼
              MCP App core: tools + ui:// resources + _meta.ui
                                        │
                  agent (gRPC StreamMCP) ──▶ container TCP (passthrough)
```

One core registers tools, `ui://` resources, and `_meta.ui`. Transports differ;
registration does not.

## Components

### 1. Apps-extension helper (`go/internal/cli/mcp/appsui`, new)

A small, isolated package so the `_meta.ui` shape lives in one place.

- `RegisterUIResource(srv, uri, html []byte, opts UIResourceOptions)` — registers
  a resource at `uri` returning `TextResourceContents{MIMEType:"text/html", Text:html}`.
  `opts` carries `CSP []string` and `Permissions []string`.
- `WithUI(tool *mcp.Tool, uri string)` — sets
  `tool.Meta.AdditionalFields["ui"] = {"resourceUri": uri}` (creating `Meta` if nil).
- `ResultWithUI(res *mcp.CallToolResult, uri, view string, data any)` — sets
  `res.Meta.AdditionalFields["ui"] = {"resourceUri": uri}` and
  `res.StructuredContent = {"view": view, "data": data}` so the iframe knows
  which view to render and with what data.

**What it does:** centralizes the extension's wire shape. **How it's used:** tool
registration calls `WithUI`; handlers call `ResultWithUI`. **Depends on:**
`mcp-go` types only.

### 2. Wendy adaptive UI (`ui://wendy/app`)

A single HTML/JS/CSS bundle embedded via `go:embed` (e.g.
`mcp/appsui/web/wendy-app.html`). One app, **view selected from the tool result
data**, not from separate resources:

| Tool(s) | View |
|---|---|
| `wendy_status`, `device_info`, `container_list`, `container_stats` | Dashboard |
| `container_start`, `container_stop`, `wifi_connect/disconnect`, `os_update`, `device_set_default` | Controls |
| `apps_list`, `app_open` (new) | Apps launcher |

- Each such tool is annotated with `WithUI(tool, "ui://wendy/app")`; its handler
  returns `ResultWithUI(res, "ui://wendy/app", "<view>", data)` **in addition to**
  the existing text content (backward compatible for non-Apps hosts).
- The iframe renders the view from `structuredContent.view` + `.data`, and uses
  the host's postMessage `ui/` dialect to issue `tools/call` for interactive
  actions (Stop, Update, Open app). The host forwards those to our server.
- The three views match the approved mockups (dashboard with device summary +
  stat tiles + container rows; controls with toggles + OS-update banner +
  Wi-Fi/default/reboot; launcher grid distinguishing UI-capable apps).

**What it does:** the rendered surface. **How it's used:** host fetches the
resource, renders the iframe, pushes result data. **Depends on:** the extension
helper + the data shapes tools already return.

### 3. Device targeting (optional `device` arg)

Today the server holds one active connection (`mcpServer` in `server.go`). To
support the UI's device dropdown:

- Tools gain an **optional `device` string parameter** (address or known device
  name/id). When present, the handler resolves/reuses a connection to that device
  for the call; when absent, it falls back to the current active connection
  (existing behavior — backward compatible).
- A small per-device connection cache replaces the single-connection field, keyed
  by resolved device identity. Discovery sources unchanged (LAN + cloud).
- `device_list` returns the candidate devices the dropdown shows; selecting one
  re-issues subsequent tool calls with that `device`.

**What it does:** lets one app instance address N devices. **How it's used:** the
UI passes `device`; CLI users may still omit it. **Depends on:** existing connect
+ discovery code, refactored from one connection to a keyed cache.

### 4. Container app UI passthrough

Extend the existing proxy so container `ui://` apps surface through Wendy.

- **Forward resources:** the proxy (CLI side `registerContainerMCPTools` +
  `proxy.go`, agent side `StreamMCP`) additionally forwards `resources/list` and
  `resources/read` for each MCP-exposing container.
- **Namespace `ui://` URIs:** when a proxied container tool declares
  `_meta.ui.resourceUri: ui://X`, rewrite it to `ui://app/<sanitized_app>/X`
  (mirrors the existing `app__tool` name prefix). `resources/read` on the
  `ui://app/<sanitized_app>/…` prefix routes back to the owning container, with
  the prefix stripped. `_meta.csp`/`permissions` are preserved verbatim.
- **Launcher integration:** `apps_list` enumerates containers, flagging which
  expose a `ui://` resource (UI-capable) vs. tools-only vs. no `mcp` entitlement
  (the mockup's three states). `app_open` returns the chosen container's UI via
  `ResultWithUI`, so the host renders the container's own app in the same iframe.

**What it does:** makes container UIs first-class inside Wendy. **How it's used:**
launcher "Open" → `app_open` → host renders the container's `ui://`. **Depends
on:** the existing gRPC `StreamMCP` proxy + resource forwarding.

### 5. Local HTTP transport

- `wendy mcp serve --http [--addr 127.0.0.1:PORT]` constructs the server with the
  **same registration** as stdio, then serves via `NewStreamableHTTPServer`.
- Default bind loopback. Optional `--token` bearer for local auth. stdio remains
  the default (no flag).

**What it does:** lets HTTP-capable/dev-mode hosts connect. **How it's used:**
`wendy mcp serve --http`. **Depends on:** the transport-agnostic core (factor
registration out of `ServeStdio`).

### 6. Cloud gateway contract (defined, not built here)

The future cloud gateway MUST:
- Reuse the **same core registration** (tools + `ui://` resources + `_meta.ui`),
  compiled into / invoked by the gateway.
- Reach the user's enrolled devices over the existing **cloud tunnel**
  (`cloud_connect`/`cloud_tunnel`), populating the same per-device connection
  cache that `device` targeting uses.
- Terminate **OAuth 2.1** and scope tool calls to the authenticated user's
  devices.
- Serve `ui://` resources (Wendy's and namespaced container ones) over HTTPS so
  ChatGPT can fetch them.

This spec ensures the core is transport-agnostic and the device cache is
gateway-pluggable so the follow-up spec only adds OAuth + hosting + wiring.

## Data flow (ChatGPT, via future gateway)

```
User → ChatGPT → (OAuth) gateway MCP → tunnel → agent [→ container TCP]
   tool result carries _meta.ui.resourceUri=ui://wendy/app + structuredContent{view,data}
ChatGPT fetches ui://wendy/app → renders sandboxed iframe → pushes data
User clicks (Stop / Update / Open) → iframe tools/call → host → gateway → … down the chain
```

Local/stdio flow is identical minus OAuth and tunnel.

## Security

- `ui://` served as `text/html`; the **host** sandboxes the iframe. `_meta.ui.csp`
  declares external origins an app may load (container apps preserve their own).
- Local HTTP binds loopback; optional bearer token.
- Cloud (future): OAuth, per-user device scoping.
- Container passthrough keeps existing isolation; the UI runs only in the host
  sandbox and reaches the container solely through the established proxy.

## Testing

- **Unit:** `_meta.ui` marshaling on tool + result; `ui://` resource registration
  returns `text/html`; URI namespacing/strip round-trip; tool→view mapping;
  `device`-arg resolution falls back to active connection when absent.
- **Proxy:** `resources/list` + `resources/read` forwarded; `_meta.ui` rewrite
  routes `resources/read` back to the owning container; tools-only and no-mcp
  containers classified correctly by `apps_list`.
- **Integration:** stdio and `--http` serve an identical tool/resource set; an
  example container app exposing a `ui://` resource appears in `apps_list` and
  `app_open` returns its namespaced resource.
- **Manual thin slice:** render the Wendy app in Claude (stdio + local HTTP);
  open a container app's UI; verify a control action round-trips.

## Risks / open questions

- **mcp-go lacks first-class Apps helpers** — mitigated: we attach `_meta` via
  `AdditionalFields` (verified). If the host expects an exact `_meta.ui` schema
  (e.g. ChatGPT's), confirm field names against the host during the thin slice.
- **Connection-model refactor** (single → keyed cache) touches existing handlers;
  keep the no-`device` fallback to avoid regressions.
- **Host schema drift** — the Apps extension is young; field names/CSP semantics
  may vary per host. The thin slice validates against at least one real host
  before broadening annotations to all tools.

## Out-of-scope follow-ups

1. **Cloud gateway** — OAuth 2.1, dynamic client registration, hosting, ChatGPT
   connection (separate spec; contract defined above).
2. **Richer container-UI authoring** docs/templates for app authors.
