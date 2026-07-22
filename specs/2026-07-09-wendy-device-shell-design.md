# `wendy device shell` — Host TTY over gRPC

Date: 2026-07-09
Status: Approved (design)

## Goal

Add `wendy device shell` to the CLI and a supporting RPC to wendy-agent so a
developer can open a full interactive TTY (login shell / bash) **on the device
host** and poke around — systemd, journald, mounts, containerd state, etc.

Transport is gRPC over the existing mTLS/PKI trust, **not** SSH. No new key
material, no new trust store: reuse the org client cert the CLI already presents
to every agent RPC.

## Background & precedent

`wendy device attach` already opens an interactive PTY over mTLS gRPC into a
**running container** via the `ExecContainer` bidi RPC. That command is the
template for everything on the wire and on the client:

- Proto: `WendyContainerService.ExecContainer` in
  `Proto/wendy/agent/services/v1/wendy_agent_v1_container_service.proto`
  (message shapes: `ExecStart{command[], tty, term_size}`, `stdin_data`,
  `resize` → `stdout_data`, `stderr_data`, `exit_code`).
- Agent handler: `go/internal/agent/services/container_service.go:708`
  (`io.Pipe` stdin, buffered `resize chan [2]uint32`, mutex-wrapped
  `stream.Send`, one Recv-pump goroutine).
- CLI: `go/internal/cli/commands/device_attach.go` (`connectToAgent` →
  `conn.ContainerService.ExecContainer`, `term.MakeRaw`/`term.Restore`,
  SIGWINCH via `notifyTerminalResize`, stdin→stream pump, Recv loop).
- mTLS dial: `connectToAgent` in `go/internal/cli/commands/helpers.go:956`,
  backed by `grpcclient.ConnectWithTLS*` in
  `go/internal/cli/grpcclient/client.go`.

The **only** genuinely new capability is a host-level PTY spawner. The existing
exec path is container-scoped (`runningContainerForApp`) and there is no
`creack/pty` dependency in the repo today; containerd owns the PTY for
container exec.

## Decisions (from brainstorming)

- **Target:** host shell (device root filesystem), not a container.
- **Access gate:** PKI trust only — the RPC is registered on the mTLS server
  unconditionally. One audit log line per session (start + end). No debug flag,
  no opt-in toggle.
- **Shell selection:** resolve the target user's login shell from
  `/etc/passwd` (fall back to `$SHELL`, then `/bin/sh`). An optional
  `-- <cmd...>` override runs a specific argv with a TTY instead.
- **Target user:** root. The agent runs as root; "poke around" implies full
  access. No `--user` flag in v1.
- **RPC placement:** a new dedicated `WendyShellService` (Approach A) — a host
  shell is not a container concept, so it does not belong on
  `WendyContainerService`.

## Architecture

### 1. Proto — new service

New file `Proto/wendy/agent/services/v1/wendy_agent_v1_shell_service.proto`,
generated into Go package `agentpb`. Reuse the existing `WindowSize{rows,cols}`
message.

```proto
service WendyShellService {
  // Interactive host PTY. First client frame MUST be Start.
  rpc HostShell(stream HostShellRequest) returns (stream HostShellResponse);
}

message HostShellRequest {
  message Start {
    // Empty => resolve the target user's login shell.
    // Non-empty => run this argv with a TTY.
    repeated string command   = 1;
    WindowSize      term_size  = 2;
  }
  oneof request_type {
    Start      start      = 1;  // first frame, required
    bytes      stdin_data = 2;  // subsequent stdin
    WindowSize resize     = 3;  // subsequent resize events
  }
}

message HostShellResponse {
  oneof response_type {
    // The PTY master merges the child's stdout+stderr; all output is
    // delivered on stdout_data.
    bytes stdout_data = 1;
    int32 exit_code   = 2;  // final frame
  }
}
```

### 2. Host PTY spawner — `go/internal/agent/hostexec`

A new package behind an interface so the shell service is testable without a
real PTY:

```go
type HostShellSpawner interface {
    // command empty => resolve login shell. Returns the child exit code.
    Run(ctx context.Context, command []string, stdin io.Reader,
        stdout io.Writer, resize <-chan [2]uint32) (int, error)
}
```

Implementation:

- **Shell resolution** (when `command` is empty): look up root in
  `/etc/passwd` for its login shell; fall back to `$SHELL`, then `/bin/sh`.
- **Spawn**: `os/exec` + `github.com/creack/pty` (new go.mod dependency).
  Environment seeded with `HOME=/root`, `TERM` (from a sane default), cwd
  `/root`.
- **I/O**: copy PTY master → `stdout`, `stdin` → PTY master.
- **Resize**: range the `resize` channel, `pty.Setsize` per frame
  (rows/cols).
- Return the process exit code; ensure the child is reaped and the PTY closed
  on all exit paths (no zombies).

### 3. Agent service — `go/internal/agent/services/shell_service.go`

Modeled line-for-line on `container_service.go:708`:

- Recv the first frame, assert `GetStart()`; otherwise `codes.InvalidArgument`.
- `io.Pipe()` for stdin; buffered `resize := make(chan [2]uint32, 8)` seeded
  from `Start.term_size`.
- One goroutine pumps client `Recv()` → stdin pipe / resize channel;
  closes stdin on client `CloseSend`.
- Mutex-wrapped sender (gRPC forbids concurrent `SendMsg`) adapted to an
  `io.Writer` for stdout.
- Call `hostexec.Run`; send the final `exit_code` frame.
- **Audit**: log a structured line at session start and end (client identity
  from the peer cert if available, argv, exit code).

Construction + registration in `go/cmd/wendy-agent/main.go` `registerAllServices`
(line 443): build `shellSvc := services.NewShellService(...)` near the other
`NewXxxService` calls and
`agentpb.RegisterWendyShellServiceServer(srv, shellSvc)` on all three servers
(plaintext, admin control socket, mTLS), mirroring the container service.

### 4. CLI client

- `go/internal/cli/grpcclient/client.go`: add a `ShellService
  agentpb.WendyShellServiceClient` field to `AgentConnection`; construct it in
  `newAgentConnection` from the shared `*grpc.ClientConn`.
- `go/internal/cli/commands/device_shell.go`: near-copy of `device_attach.go`.
  - Parse optional `-- <cmd...>` via `cmd.ArgsLenAtDash()`.
  - Require an interactive terminal (`term.IsTerminal`); error early otherwise.
  - `connectToAgent(ctx, ...)` → `conn.ShellService.HostShell(ctx)`.
  - Send `Start{command, term_size: termSize(fd)}` first (mutex-guarded send).
  - `term.MakeRaw(fd)` with deferred `term.Restore`.
  - Reuse the existing `notifyTerminalResize` SIGWINCH helper
    (`device_attach_unix.go` / `device_attach_windows.go` — already
    build-tagged) to send `resize` frames.
  - stdin→stream pump goroutine; `stream.CloseSend()` on read EOF.
  - Main `Recv()` loop: write `stdout_data` to `os.Stdout`, return on
    `exit_code`.
- Register `newDeviceShellCmd()` in `device.go` `newDeviceCmd()` (manage group).

## Data flow

```
CLI raw-mode terminal
   ⟷ bidi mTLS gRPC stream (WendyShellService.HostShell)
      ⟷ agent handler (shell_service.go)
         ⟷ host PTY master (hostexec)
            ⟷ login shell / bash child on the device host
```

- **Resize:** local SIGWINCH → `resize` frame → `pty.Setsize`.
- **Exit:** child exits → `exit_code` frame → CLI restores the terminal.

## Error handling

- **Not a TTY locally:** CLI errors before dialing — "shell requires an
  interactive terminal". TTY-only in v1 (no piped/non-TTY mode).
- **Shell binary missing:** agent returns a clear error (`codes.Internal`)
  naming the paths tried.
- **Stream / connection drop:** `term.Restore` always runs via `defer`; agent
  closes the PTY and reaps the child.
- **Concurrent-send safety:** mutex around `stream.Send` on both client and
  server.

## Testing

- **Agent (`shell_service`):** unit test `HostShell` with a fake
  `HostShellSpawner` (echo stdin→stdout, honor resize, return a set exit code).
  Assert frame ordering, `Start`-first enforcement, resize delivery, exit
  propagation.
- **`hostexec`:** unit test shell resolution logic; a real `pty` echo
  round-trip on Linux (build-tagged) to cover `Setsize` and exit code.
- **Manual / E2E:** `wendy device shell` into a real device, exercised and
  documented in the PR (raw-mode CLI is not unit-testable).

## Scope (v1, YAGNI)

- Root only — no `--user`.
- TTY-only — must be an interactive terminal.
- No session recording beyond the audit log line.
- Windows CLI reuses the existing no-op resize stub from `device_attach`.

## Out of scope / future

- Non-root / `--user <name>` target.
- Non-TTY piped exec (`echo x | wendy device shell -- cmd`).
- Full session recording / replay.
- Gating behind a debug/opt-in flag (explicitly rejected: PKI trust is the
  gate).
