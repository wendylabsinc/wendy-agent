# `http` entitlement: declared HTTP port over gRPC + auto-open on `wendy run`

Date: 2026-08-06
Status: Approved, not yet implemented

## Problem

Apps that serve a web UI have no way to declare "this is my HTTP port" such that:

1. A remote management client (iOS/macOS app, or any gRPC client) calling `apps list` can discover the port and offer to open it in a browser.
2. `wendy run` can automatically open that URL in the developer's browser once the app is actually reachable, without requiring a `postStart` hook.

## Existing precedent

The `mcp` entitlement (`{ "type": "mcp", "port": N }`) already does something structurally identical for MCP servers: a single declared port, persisted as a containerd label at container-create time, echoed back verbatim (no liveness check) as `AppContainer.mcp_port` in `ListContainers`. This design mirrors that pattern for a general HTTP port, rather than overloading the `network` entitlement's `mode`/`ports` fields (which are a host↔container forwarding list, wired today only for `network.mode == "mesh"`, and conceptually distinct from "the port to browse to").

A third, unrelated port mechanism (`GetContainerPorts`) does live `/proc/net` scanning for `wendy device top`. This design does not touch it — `http` is a static, wendy.json-declared value, matching `mcp_port`'s behavior, not a discovery mechanism.

`wendy run` already has a full readiness-gate → build-URL → open-browser pipeline (`go/internal/cli/commands/run.go`): `waitForReadiness` polls `readiness.tcpSocket`, `announceReachableURL` builds a URL from the agent's reported network interfaces, and `startPostStartHook` opens `hooks.postStart.openURL` via the cross-platform `browseropen.Open()` helper. This design extends that pipeline to trigger automatically off the new entitlement instead of requiring an explicit hook.

## Design

### 1. `wendy.json` schema

New entitlement type, same shape as `mcp`:

```json
{ "type": "http", "port": 8080 }
```

- `go/internal/shared/appconfig/appconfig.go`:
  - Add `EntitlementHTTP = "http"` to the entitlement-type const block and `ValidEntitlementTypes`.
  - `allowedKeys[EntitlementHTTP] = []string{"type", "port"}`.
  - Reuse the existing `Entitlement.Port int` field (already backing `mcp`'s `port`); update its doc comment to `// MCP, HTTP`.
  - Validation (`validateEntitlements`): `case EntitlementHTTP` — same `1–65535` range check as `mcp`, and the same "at most one `http` entitlement per app" cap (mirrors the existing at-most-one-`mcp` rule, since there is exactly one port slot per `AppContainer`).
- `wendy.schema.json` / `wendy-fleet.schema.json`: add the `http` entitlement shape alongside `mcp`.
- Docs (`go/internal/cli/assets/docs/content/docs/advanced/apps/wendy.json.md`): document `http` next to `mcp`, including the same caveat that it's typically combined with `{ "type": "network", "mode": "host" }` (or an explicit `bridge` port forward) to actually be reachable from outside the container.

No changes to `go/internal/agent/oci/entitlements.go`'s `ApplyEntitlements` switch — like `mcp`, `http` carries no OCI-spec mutation of its own; it's pure metadata consumed by the container-listing path.

### 2. Proto: `AppContainer.http_port`

`Proto/wendy/agent/services/v1/shared.proto`, in `message AppContainer`:

```proto
uint32 http_port = 13;  // wendy.json `http` entitlement's declared port, if any; 0 if none declared.
```

(13 is the next free field number — 7–10 are reserved for the in-flight provenance PR, 11/12 are `exit_code`/`termination_reason`.)

Regenerate:
- Go: `agentpb` (`go/internal/agent/pb` or wherever the generated package lives).
- Swift: `WendyAgentGRPC` proto sources under `swift/WendyAgentCore/Sources/WendyAgentGRPC/Proto/`.

### 3. Go/Linux agent (containerd backend)

Mirrors `mcp_port` exactly — static, declared-only, no liveness check:

- `go/internal/agent/containerd/helpers.go`: add `labelKeyHTTPPort = "sh.wendy/http.port"`. In `wendyLabels`, alongside the existing scan for `EntitlementMCP`, scan for `EntitlementHTTP` and write its port as this label at container-create time.
- `go/internal/agent/containerd/client.go`, `ListContainers`: read `info.Labels[labelKeyHTTPPort]`, parse to `httpPort uint32`, merge across a multi-service app's containers (first non-zero wins — same rule as `mcpPort`), and set `AppContainer.HttpPort` on the returned struct.

### 4. Swift Mac agent (parity)

`swift/WendyAgentCore/Sources/WendyAgent/Services/ContainerService.swift`, `listContainers`: currently sets neither `mcpPort` nor (post-design) `httpPort` — it only fills in `appName`/`runningState`. As part of this work, add entitlement-derived port population there for **both** `mcpPort` and the new `httpPort`, reading whatever per-container metadata the Mac `container`-backed runtime already retains for the app's `wendy.json` entitlements (label/annotation equivalent — exact mechanism to be confirmed against the Mac container backend during implementation, since it doesn't use containerd labels). This closes `mcp_port`'s existing Mac-side gap as a side effect of building the same code path for `http_port`; not a separate scope expansion.

### 5. `wendy run`: readiness + auto-open

`go/internal/cli/commands/run.go`:

- **Readiness default.** In `runPostStartIfReady` (or just before `waitForReadiness` is invoked), if the app declares `http` and `wendy.json` has no explicit `readiness.tcpSocket`, synthesize one with `Port: httpPort` before calling the existing poller. No new polling logic — this only changes what `waitForReadiness` is told to wait for.
- **Auto-open.** In `startPostStartHook` (or a sibling step run right after readiness), if the app declares `http`:
  - Build the URL via the existing `bestReachableIP` + `reachableAppURL`-style logic, substituting `httpPort` for the readiness-probe port it uses today.
  - Call `browserOpen(url)` (the existing `browseropen.Open` var) automatically — no `hooks.postStart.openURL` config required.
  - If the app *also* explicitly configures `hooks.postStart.openURL`, that explicit hook wins (unchanged behavior, just now with an implicit fallback when it's absent).
- `announceReachableURL`'s printed "App reachable at %s" message should also prefer `httpPort` over the readiness port when both exist, so the printed URL and the auto-opened URL agree.

### 6. CLI surfacing

`go/internal/cli/commands/apps.go` (`wendy device apps` / `apps list`): add `httpPort` to the JSON output struct and as a column in the table renderer when non-zero — parity with what `mcp_port` should have but currently doesn't get in this command (that gap is pre-existing and out of scope here beyond piggybacking the same new column).

## Testing

- `appconfig` unit tests: valid/invalid `http` entitlement (port range, at-most-one, unknown keys).
- `entitlements_test.go`: confirm `http` is a no-op in `ApplyEntitlements` (like `mcp`) — no OCI spec mutation.
- `containerd` package: label round-trip test (`wendyLabels` writes `sh.wendy/http.port`, `ListContainers` reads it back), including the multi-service merge case.
- `run.go`: unit test for the readiness-default synthesis (http entitlement + no explicit readiness → synthesized TCPSocket), and for auto-open URL construction (httpPort takes precedence over readiness port when both present).
- Swift `ContainerServiceTests.swift`: add coverage for `httpPort` (and, incidentally, `mcpPort`) population in `listContainers`.
- E2E: hardware-unverified until a real device run, consistent with how most entitlement PRs in this repo ship (per project history — `mcp`/other entitlement additions are typically merged CI-green with hardware verification following separately).

## Out of scope

- The iOS/macOS *management app* UI that would render an "open in browser" affordance from `AppContainer.http_port` — no such app exists in this repo; this design only makes the field available over gRPC for that (separate-repo, or future) client to consume.
- `network.ports` (mesh ingress forwarding) and `GetContainerPorts` (live `/proc/net` scanning) are unchanged. `http` is a third, independent, simpler concept — a single author-declared port — not a replacement for either.
- Backfilling `mcp_port` into the Go CLI's `apps list` output is done here only because it's the same new table column as `httpPort`; no other `mcp_port` gaps are addressed.
