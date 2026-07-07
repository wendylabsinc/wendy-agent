# Claude-on-device: Sub-project 1 — "Claude operates & debugs the device"

**Date:** 2026-06-29
**Status:** Draft (design)
**Depends on:** PR #1239 (`admin` entitlement + agent local unix-socket gRPC),
**already merged to `main`** (verified present in this worktree: `EntitlementAdmin`
in appconfig, `applyAdmin` in oci/entitlements, `internal/agent/localsocket`, and the
`{"type":"admin"}` schema entry). The agent serves its full gRPC surface on a
host socket (`oci.AdminAgentSocketHostPath` = `/var/lib/wendy/agent-control/agent.sock`),
and `{"type":"admin"}` bind-mounts that socket into the container at
`/run/wendy/agent/agent.sock` and sets `WENDY_AGENT_SOCKET` (consumers should
read the env var rather than hard-coding the path).
**Followed by:** Sub-project 2 — on-device image builder (`wendy run` builds on the
Jetson). Out of scope here.

## Goal

Run the real Claude Code CLI inside an `admin`-entitled container on a Jetson, log
in with a normal Claude.ai subscription, and let it **operate and debug the device
itself** over the local agent socket — inspect device/hardware state, list and
control apps (start/stop/restart/delete), stream logs and telemetry, and exec into
any container to debug it. Creating brand-new apps (image builds) is deliberately
deferred to SP2.

This decomposition matters: "debug itself" needs only the socket transport, an
interactive terminal, and auth — **not** the on-device builder, which is the hard,
risky part. SP1 delivers real value and de-risks the foundation; SP2 adds the
builder on top.

## Non-goals (SP1)

- On-device image building / `wendy run` producing a new image on the Jetson (SP2).
- A web UI, custom chat front-end, or Agent-SDK app (we use the stock Claude Code CLI).
- Any auth/hardening on the local socket beyond PR #1239's entitlement gate.
- Editing-and-redeploying app source (needs the builder → SP2). SP1 debugging is
  observe + lifecycle-control + exec-in.

## Components

### A. wendy CLI local-socket transport

`go/internal/cli/grpcclient/client.go` + `go/internal/cli/commands/helpers.go`.

- New `grpcclient.ConnectUnix(ctx, socketPath) (*Conn, error)`: dials a unix
  domain socket with plain h2c (`insecure.NewCredentials()` + a
  `grpc.WithContextDialer` that `net.Dial("unix", path)`s). Models the existing
  plaintext `Connect()` (client.go:69) and the proven tunnel-dialer pattern
  (`cloud_tunnel.go:506`).
- Single chokepoint `connectWithAutoTLSDiagnostics` (helpers.go:977): if
  `WENDY_AGENT_SOCKET` is set and non-empty, dial that socket via `ConnectUnix`
  and **skip all mTLS/discovery/cert logic** entirely. Otherwise unchanged.
- Because every command funnels through this one function, `wendy device info`,
  `wendy device apps`, `wendy device telemetry`, etc. all inherit socket transport
  with **zero per-command changes**.
- Off-device behavior is byte-for-byte unchanged when the env var is unset
  (the default everywhere except inside the `admin` container).

### B. Agent interactive exec + PTY (`ExecContainer`)

`proto` (service-protos), `go/internal/agent` (ContainerService), CLI subcommand.

The existing `AttachContainer` streams the **main process** stdout/stderr and
carries `StdinData`, but allocates **no PTY** and has **no resize**. A full-screen
TUI (Claude Code) needs a PTY with dimensions. Rather than retrofit the
main-process attach, add a docker-`exec -it`-style RPC:

- New bidirectional RPC `ExecContainer(stream ExecContainerRequest) returns
  (stream ExecContainerResponse)` on `WendyContainerService`.
  - First client frame: `{app_name, command[], term: {rows, cols}}`.
  - Subsequent client frames: `stdin_data` bytes, or a `resize {rows, cols}` frame.
  - Server frames: `stdout_data` / `stderr_data`; a final `exit_code`.
- Agent implementation: a containerd **task Exec** with `Terminal: true`, wiring the
  PTY master to the gRPC stream and applying resize via the PTY's window-size ioctl.
  Exec-a-new-process (not attach-to-main) lets Claude exec into **any** app's
  container to debug it, and decouples the TUI lifecycle from the container's.
- Registered on **both** the mTLS server and the local socket (it goes through the
  same `registerAllServices` closure), so on-device Claude reaches it over the
  socket and a laptop reaches it over mTLS.

New CLI subcommand `wendy device attach <app> [-- <cmd>]`
(`go/internal/cli/commands/`):

- Defaults `<cmd>` to an interactive shell; for the Claude container we attach
  `claude` directly (or a shell, then run `claude`).
- Puts the local terminal into raw mode, streams stdin → `stdin_data`, forwards
  `SIGWINCH` → `resize` frames (seeded with the initial size), renders
  `stdout/stderr`, and exits with the remote `exit_code`.

### C. The `claude-on-device` container app

A Wendy app (its own directory; ships as a sample/first-party app).

- Base: arm64 image with Node.js + git + a minimal userland.
- Installs `@anthropic-ai/claude-code` (npm, global) and the `wendy` CLI binary
  built from the block-A branch (so it understands `WENDY_AGENT_SOCKET`).
- Entrypoint: a long-lived idle (e.g. `sleep infinity`) so the container stays up
  and `wendy device attach` execs `claude` into it with a fresh PTY on demand.
- `wendy.json`: declares `{"type":"admin"}` (and nothing else network-facing in
  SP1). The entitlement bind-mounts the host socket into the container at
  `/run/wendy/agent/agent.sock` and sets `WENDY_AGENT_SOCKET`, so the in-container
  `wendy` reaches the local agent with no configuration and no certs.
- Persistent volume(s): `/root/.claude` (OAuth token + Claude config, survives
  restarts) and `/workspace` (Claude's working directory).

### D. Auth flow

- First interactive `claude` run prints the OAuth URL + code (Claude Code's
  headless device-login flow). You approve in your laptop browser and paste the
  code back over the `wendy device attach` session.
- The resulting session token persists to the `/root/.claude` volume and is reused
  across container restarts.
- Uses the Claude.ai subscription. An `ANTHROPIC_API_KEY` env override remains
  possible but is not the documented path.

## Data flow

```
laptop                                  Jetson (WendyOS)
------                                  ----------------
wendy device attach claude-on-device
  └─ mTLS gRPC ExecContainer ───────────► wendy-agent
        raw-mode TTY  ◄── stdout/stderr ──┘   │ containerd task Exec (Terminal:true)
        stdin/resize  ──────────────────────► │ PTY ↔ claude-on-device container
                                              ▼
                                   claude (TUI) running in container
                                     │  wendy <cmd>  (WENDY_AGENT_SOCKET set)
                                     ▼
                                   /run/wendy/agent/agent.sock (bind-mounted by `admin`)
                                     │ plain h2c, no mTLS  (PR #1239)
                                     ▼
                                   wendy-agent localSrv → full service set
                                   (DeviceInfo, ContainerService incl. Exec,
                                    Telemetry, WiFi/BT, OS/Agent update, …)
```

So Claude, prompted by you, runs `wendy device info`, `wendy device apps`,
`wendy device telemetry …`, `wendy device attach <other-app>` etc. — each hitting
the local socket — to inspect and debug the device.

## Security model (stated plainly)

`admin` grants **full local device control with no authentication** beyond the
entitlement mount (per PR #1239). With Claude in the loop, that means it can:

- start / stop / **delete** any app on the device,
- read all telemetry and exec into any container,
- trigger **OS and agent updates** — i.e. brick or wipe the device if adversarially
  prompted.

The OAuth token also lives on the device volume. This is a **deliberate, accepted**
grant for a first-party self-managing device. SP1's mitigation is **honest
documentation** of the blast radius (in the app's README and the entitlement docs),
not code-level restriction. A future hardening option — a per-service allow-list on
the local socket, or a read-only `admin` variant — is noted but out of scope.

## Testing

TDD throughout (write the failing test first):

- **`ConnectUnix` (unit):** stand up an in-process gRPC server on a temp unix
  socket, dial via `ConnectUnix`, assert a simple call (e.g. `GetAgentVersion`)
  succeeds over plain h2c.
- **Chokepoint routing (unit):** with `WENDY_AGENT_SOCKET` set,
  `connectWithAutoTLSDiagnostics` dials the socket and never touches mTLS/discovery;
  with it unset, behavior is unchanged.
- **`ExecContainer` (integration):** against the existing containerd mocks/fakes —
  exec a command with `Terminal:true`, assert stdin echoes to stdout through the
  PTY, a `resize` frame is honored, and `exit_code` propagates.
- **`wendy device attach` (unit/e2e-light):** raw-mode setup/teardown, SIGWINCH →
  resize frame, exit-code propagation (server stream mocked).
- **On-device smoke (manual, like PR #1239):** cross-build the agent + CLI for
  arm64, deploy to an Orin, run the `claude-on-device` app with `admin`,
  `wendy device attach` it, complete OAuth, and confirm `wendy device info` over the
  socket returns real device data and `wendy device attach <other-app>` works.

## Risks & realities

1. **PTY over gRPC.** Wiring a containerd PTY master to a bidirectional gRPC stream
   with correct resize and clean teardown is the fiddliest part; the integration
   test plus the on-device smoke are the guards.
2. **Deployed agent must be new enough.** #1239 is merged to `main`, but a device
   in the field still runs an older agent; SP1 ships its agent (with `ExecContainer`)
   the same way #1239 did (`wendy device update --binary` / OS update) before the
   `claude-on-device` app can attach.
3. **Claude Code in a constrained container.** Node + the CLI's runtime
   assumptions (writable HOME, network egress for the API) must hold inside the
   container; the smoke test covers this.
4. **Security blast radius.** Documented above; accepted, but the docs must make it
   obvious.

## Phasing within SP1

1. wendy CLI `ConnectUnix` + `WENDY_AGENT_SOCKET` chokepoint routing + tests (A).
2. `ExecContainer` proto + agent PTY exec + `wendy device attach` + tests (B).
3. `claude-on-device` app (image + `wendy.json` admin + volumes) (C).
4. Headless OAuth wiring + persistence (D).
5. Docs: app README + entitlement blast-radius note; on-device smoke.
