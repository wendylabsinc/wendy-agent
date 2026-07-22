# Warn on Publicly-Exposed Ports + Document Port Exposure — Design

Date: 2026-07-05
Status: Approved (design review with Joannis)
Builds on: `jo/mesh-foundation` (mesh data plane). Separate PR from the
prefer-LAN work (`2026-07-05-mesh-prefer-lan-design.md`).
Related: PR #1361 question #2 (how a human reaches a meshed service /
loopback-only mesh ports).

## Problem

A container's ports are publicly reachable — on the device's real network
interfaces, i.e. the LAN and potentially the internet — whenever the app runs
with **host networking**. Host networking is selected by a `network`
entitlement whose `mode` is `host`, `host-admin`, **or omitted** (`applyNetwork`
in `oci/entitlements.go:432-438` maps an empty mode to `host` and strips the
container's network namespace; `hasHostNetworkEntitlement` in
`client.go:1725-1733` encodes the same `{host, host-admin, ""}` set).

The omitted-mode case is the trap: a bare `{ "type": "network" }` reads as
"just give me networking," but it grants host networking, so any port the app
binds on `0.0.0.0`/`::` is exposed to the whole network. Operators deploying
what they think is an internal or mesh-only service can silently expose it. The
agent gives no signal, and the docs actively mislead (see Docs below).

Mesh (`mode: "mesh"`) and no-network-entitlement apps run in their own network
namespace on a private CNI bridge, so a non-loopback bind there is **not**
publicly reachable (mesh ports reach the container only via the loopback DNAT +
`MeshDial`). Those must not be warned about.

## Goal

1. **Behavioral warning:** at runtime, detect an app that is *actually*
   listening on a publicly-reachable address and log a clear `WARN`, so the
   operator learns their service is exposed. Behavioral (reads real listening
   sockets) rather than a static config guess, so it reflects what the app
   truly bound.
2. **Docs:** document the port-exposure model (mesh = private, host = public),
   fix the incorrect "omitted = isolated" claim, and document `mode: "mesh"`.

## Scope / non-goals

- Warn only for **host-networking** apps (host / host-admin / omitted mode) —
  the only apps whose non-loopback binds are genuinely public. Isolated and
  mesh apps are skipped (their non-loopback binds sit on a private bridge, not
  the public LAN).
- No enforcement/blocking — this is advisory logging only.
- No change to how ports are published or to the mesh data path.
- Not a runtime *active connection* probe (no dialing); it reads the app's own
  listening-socket table, which the agent already exposes.

## Component 1 — Exposure probe (agent)

### Pure classification core (unit-testable, no containerd)

```go
// isPubliclyBoundAddress reports whether a listening socket's bind address is
// reachable from outside the host — i.e. a wildcard (0.0.0.0 / ::) or a
// specific non-loopback interface address. Loopback (127.0.0.0/8, ::1) is
// private; an empty or unparseable address is treated as not-public (we only
// warn on a definite exposure).
func isPubliclyBoundAddress(addr string) bool {
    a, err := netip.ParseAddr(addr)
    if err != nil {
        return false
    }
    return !a.IsLoopback()
}
```

`netip.Addr.IsLoopback()` is false for the unspecified wildcard addresses
(`0.0.0.0`, `::`) and for specific interface IPs, and true for loopback — which
is exactly the public/private split we want.

### I/O wrapper

`Client.WarnPubliclyExposedPorts(ctx)`:

1. Lists running Wendy containers (existing enumeration).
2. For each, reconstructs entitlements from container labels
   (`parseEntitlementsFromAnnotations`, already used on the start path) and
   keeps only apps for which host networking is in effect. To guarantee the
   classification never drifts from the code that actually selects host netns,
   `hasHostNetworkEntitlement` is refactored to take `[]appconfig.Entitlement`
   (a new `entitlementsUseHostNetwork(ents)`), with the existing
   `hasHostNetworkEntitlement(appCfg)` delegating to it. The probe calls the
   same predicate.
3. For each such app, calls the existing `GetListeningPorts(ctx, appName)`
   (`procnet.go:151`) and collects every `PortEntry` whose `Address` satisfies
   `isPubliclyBoundAddress`.
4. Emits a deduplicated `WARN` per newly-exposed `(appID, protocol, port,
   address)` tuple, e.g.:
   > `WARN app "cam": port 8080/tcp is listening on 0.0.0.0 and is reachable from the device's network (network mode: host). For private cross-device access, use a "mesh" network entitlement.`

`GetListeningPorts` already attributes sockets to the app's own process tree
(it must, since host-net apps share the host's socket table), so the probe sees
only the app's listeners, not sshd/agent/etc.

### Dedup / re-warn

The Client holds `warnedExposures map[string]struct{}` (key
`appID|proto|port|addr`) under `c.mu`. Each run recomputes the current exposed
set across all host-net apps; it warns for tuples in `current \ warned`, then
sets `warned = current`. Recomputing the whole set each run means a tuple that
disappears (app stopped, or rebound to loopback) is pruned and will warn again
if it reappears — no unbounded growth, one warning per distinct exposure.

## Component 2 — Trigger (container monitor)

The probe runs on the container monitor's existing periodic health tick
(`ContainerMonitor.checkContainers`, `monitor.go:204`), which already iterates
running containers on a timer. Exposed via a new optional capability interface
the monitor type-asserts for, mirroring `GroupRestarter` / `AppStateRebuilder`
so the large `ContainerdClient` interface and its mocks stay untouched:

```go
// PortExposureProber is the optional capability to scan running host-network
// apps for publicly-bound listening ports and log a warning for each. The
// monitor calls it once per health tick; the implementation dedups so a given
// exposure is logged once.
type PortExposureProber interface {
    WarnPubliclyExposedPorts(ctx context.Context)
}
```

The monitor calls it once per tick (deduped inside), catching ports opened
after start and after restarts with no separate scheduler.

## Component 3 — Docs

In `apps/wendy.json.md`, `network` entitlement section (currently lines
186-202):

- **Fix the mode table:** the current "*(omitted)* | Default isolated network"
  row is wrong — an omitted mode is host networking. Correct it to state that
  omitting `mode` (and `host`) shares the host network stack, so bound ports
  are reachable on the device's interfaces.
- **Add `mode: "mesh"`** to the table: isolated network namespace; ports are
  private and reachable from other devices in the org by device name
  (`device-<id>...`) via the mesh, not from the LAN directly.
- **Add a short "Port exposure" note:** host modes publish on the device's real
  interfaces (public LAN); mesh keeps ports private (loopback + cross-device
  mesh). Mention that the agent logs a `WARN` when an app is listening on a
  public address, so operators can spot unintended exposure.

Docs live under `go/internal/cli/assets/docs/` (git-tracked; surfaced via the
repo `docs` symlink).

## Error handling

Best-effort and advisory, consistent with the surrounding agent code:
`WarnPubliclyExposedPorts` never returns an error and never affects container
lifecycle. A failure to list containers, read labels, or read listening ports
for one app logs at debug/warn and continues to the next; it never blocks the
monitor tick.

## Testing

- **Unit — `isPubliclyBoundAddress`:** table over `0.0.0.0`, `::`, `127.0.0.1`,
  `::1`, a specific LAN IP (`192.168.1.10`), empty, and garbage — asserting
  public vs private.
- **Unit — `entitlementsUseHostNetwork`:** host / host-admin / omitted → true;
  `mesh` → false; no network entitlement → false. Assert the existing
  `hasHostNetworkEntitlement(appCfg)` still returns the same results via the
  delegation.
- **Unit — classification + dedup core:** given per-app (mode, []PortEntry)
  fixtures and a running `warned` set, assert: host + `0.0.0.0` warns once;
  host + `127.0.0.1` never warns; mesh + `0.0.0.0` never warns; a repeat run
  with the same input warns nothing; a tuple that disappears then reappears
  warns again. (The pure diff is testable without containerd; the `c.client` /
  `GetListeningPorts` I/O is the thin untestable wrapper, matching the existing
  pattern for `RebuildAppStateCaches`.)
- **Monitor:** a fake implementing `PortExposureProber` records that
  `WarnPubliclyExposedPorts` is invoked on a health tick (mirrors the existing
  `AppStateRebuilder` monitor test).
- **Docs:** prose only.

## Out of scope (restated)

- Prefer-LAN dial fixes (separate spec/PR).
- Any blocking/enforcement of public exposure.
- Reachability of a meshed UI from a human's browser (PR #1361 question #2) —
  this design only *warns*; it does not add a LAN-facing publish path.
