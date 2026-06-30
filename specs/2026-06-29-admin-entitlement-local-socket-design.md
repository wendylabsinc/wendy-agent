# `admin` entitlement + agent local unix-socket gRPC (Phase 2, sub-project A)

**Date:** 2026-06-29
**Status:** Draft (design)
**Parent:** native-rendering shell (`wendyos-display`); this unblocks live data
(the shell reading real device info + the app list from the on-device agent).

## Goal

Let an on-device container talk to the wendy-agent's gRPC **without device PKI /
mTLS**, gated solely by an opt-in entitlement:

- The agent serves its **existing gRPC services** on a **local unix domain
  socket** (`/run/wendy/agent.sock`), with **no mTLS** — the socket is not
  reachable off-device.
- A new **`admin`** entitlement bind-mounts that socket into a container (only
  containers that declare it) and sets `WENDY_AGENT_SOCKET`.
- The shell (sub-project B) connects over the socket via grpc-swift to get live
  device info + the app list. Deploy (sub-project C) ships the new agent + the
  shell with `admin`.

This sub-project (A) is the **agent side only**: the entitlement + the local
listener. B (Swift client) and C (deploy) are separate specs.

## Security model (read carefully — this is the whole trust boundary)

- The local socket exposes the **full agent service set** (AgentService,
  ContainerService, …) with **no authentication**. Anything with the socket can
  fully control the device's apps (start/stop/delete), read telemetry, etc.
- The **only** gate is the `admin` entitlement: the socket is bind-mounted
  **only** into containers whose `wendy.json` declares `{"type":"admin"}`.
  Containers without it never see the socket.
- Therefore `admin` is a **privileged, deliberate grant** — equivalent to giving
  an app local control of the device. This must be documented loudly in the
  entitlements docs, and the entitlement name (`admin`) is intentionally blunt.
- The socket lives at `/run/wendy/agent.sock`, owned root, mode `0660`. Off-device
  reachability is impossible (unix socket); on-device, non-entitled containers
  can't reach it (not in their mount namespace) and host processes need root.

## Components

### 1. `admin` entitlement (appconfig)
`go/internal/shared/appconfig/appconfig.go`, mirroring `gpu`/`display`:
- `EntitlementAdmin = "admin"` in the type constants + `ValidEntitlementTypes`.
- `allowedKeys["admin"] = {"type"}` (capability flag, no sub-keys).
- Validation: at most one `admin` entitlement per app (mirrors the `mcp` rule).

### 2. `applyAdmin` (OCI)
`go/internal/agent/oci/entitlements.go`, wired into the `ApplyEntitlements`
switch, following the `applyAudio`/`applyGPU` pattern:
- Bind-mount host `/run/wendy/agent.sock` → container `/run/wendy/agent.sock`
  (`rbind,nosuid,noexec`; **not** `nodev` is irrelevant for a socket).
- `spec.Process.Env += "WENDY_AGENT_SOCKET=/run/wendy/agent.sock"`.
- **Conditional on the host socket existing** (like the audio/PipeWire mount), so
  an app with `admin` still starts if the socket is absent (older agent / socket
  not yet up) — no-op-safe.
- Invariant: an app **without** `admin` gets a byte-for-byte unchanged spec (no
  socket mount, no env). Tested.

### 3. Agent local unix-socket server
`go/cmd/wendy-agent/main.go`. A third listener alongside the mTLS TCP server,
reusing the existing `registerAllServices(srv *grpc.Server)` closure (main.go:392):
- Create the runtime dir (`/run/wendy`), remove any stale socket, `net.Listen("unix", "/run/wendy/agent.sock")`, `chmod 0660`.
- `localSrv := grpc.NewServer(` with the **error interceptors only** (`UnaryErrorInterceptor`/`StreamErrorInterceptor`) and **NOT** the mTLS interceptors `)`.
- `registerAllServices(localSrv)` — same handlers as the mTLS server.
- `go localSrv.Serve(localLis)`; add to graceful-shutdown.
- Starts **unconditionally at agent startup** (unlike the mTLS server it needs no
  provisioning certs), behind a small feature check so it can be disabled if
  needed (env `WENDY_LOCAL_SOCKET=off`).

## Data flow

```
shell container (admin entitlement)
  └─ /run/wendy/agent.sock  ◄── bind mount (applyAdmin) ──┐
       │ grpc-swift (plain h2c over UDS)                  │
       ▼                                                  │
  wendy-agent localSrv (no mTLS) ── registerAllServices ──┘
  GetAgentVersion → device info;  ListContainers → live app list
```

## Testing

- **appconfig (unit):** `admin` valid; duplicate rejected; JSON unknown-key warns.
- **oci (unit):** `applyAdmin` adds the socket mount + `WENDY_AGENT_SOCKET` when
  the host socket exists (stub the path); adds nothing when absent; the
  **non-`admin`-app-unchanged invariant**.
- **Local server (integration, this repo):** start `localSrv` on a temp unix
  socket, dial it with a plain gRPC client, assert `GetAgentVersion` returns and
  `ListContainers` streams — proving no-mTLS access works over UDS. (Run with a
  fake/in-memory containerd where the existing service tests already provide
  mocks.)
- The socket perms / mount-namespace isolation is validated on-device (C).

## Risks & realities

1. **Device needs a new agent.** The `admin` entitlement + local socket only
   exist in a new agent build; the device's released agent (2026.06.28) rejects
   unknown `admin` and has no socket. Sub-project C ships the new agent via
   `wendy device update --binary` or an OS update — over the flaky USB link.
2. **Security surface.** Documented above; `admin` = full local control. Acceptable
   per the explicit decision, but the docs must make the blast radius obvious.
3. **Socket lifecycle.** Stale socket on restart (remove-before-listen handles
   it); the conditional mount keeps apps bootable if the socket is down.

## Phasing within A

1. `admin` entitlement (appconfig) + tests.
2. `applyAdmin` (oci) + tests (incl. the unchanged-invariant).
3. Agent local unix-socket server in main.go + an integration test.
4. Docs: the `admin` entitlement entry with the blast-radius warning.
