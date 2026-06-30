# Mount a WendyOS volume as a network drive

**Date:** 2026-06-30
**Branch base:** `jo/claude-on-device`
**Status:** Design — approved, pending spec review

## Summary

Let a user mount a WendyOS app's persistent volume on their macOS, Linux, or
Windows PC as a read-write network drive. The volume must be mountable wherever
the `wendy` CLI can already reach the device — on the LAN, over USB-C, or
remotely through the cloud tunnel.

The device runs **no file server** and opens **no new ports**. A new gRPC
filesystem service on the agent answers per-operation RPCs. A **host-side
gateway** inside the `wendy` CLI translates those RPCs into a locally-mountable
network filesystem (NFSv3 on macOS/Linux, WebDAV on Windows), bound to
`127.0.0.1`, and auto-mounts it.

## Goals

- Mount a named volume as a drive on macOS, Linux, and Windows.
- Work both locally (mDNS/IP, mTLS) and remotely (cloud tunnel) — reuse the
  CLI's existing transport so "works anywhere" is automatic.
- Read-write by default.
- No kernel extensions to install (no macFUSE/WinFsp); rely on each OS's
  built-in NFS or WebDAV client.
- Keep the edge device light: no Samba/NFS daemon on the device.

## Non-goals (explicitly out of v1)

- Detached / daemon mount mode (`--detach`). v1 mount is foreground.
- Host-side write-back caching. v1 is correctness-first, pass-through writes.
- Creating symlinks (we resolve and read existing ones; block creating links
  that escape the volume root).
- SMB frontend.
- Mounting arbitrary device paths. **Volumes only.**

## Background (current state)

- Volumes are plain directories at `/var/lib/wendy/volumes/<name>` on the
  device, bind-mounted into containers. Defined via the `persist` entitlement.
- The agent already exposes `ListVolumes` / `RemoveVolume` on
  `WendyContainerService`
  (`Proto/wendy/agent/services/v2/container_service.proto`).
- CLI is Go + Cobra. Device subcommands live in
  `go/internal/cli/commands/` and register under the `device` group
  (`device.go`). `volumes.go` is the closest existing sibling.
- The CLI reaches the agent via `resolveTarget(ctx)` →
  `*grpcclient.AgentConnection`, over mTLS (LAN/mDNS/IP) or the cloud tunnel.
  The claude-on-device branch also added a local admin Unix socket
  (`WENDY_AGENT_SOCKET`).
- `file_sync_service` v2 exists in proto but is an unimplemented stub. It models
  push-sync (manifest/chunk/commit), **not** POSIX filesystem ops, so it is not
  reused here.

## Architecture

```
your PC                                              WendyOS device
┌─────────────────────────────────────┐             ┌────────────────────────────┐
│ Finder / Explorer / file manager     │             │ wendy-agent                │
│        │ (native mount)              │             │   WendyVolumeFsService     │
│   127.0.0.1 NFSv3  (mac/linux)       │             │     ├ Stat/ReadDir/Read    │
│   127.0.0.1 WebDAV (windows)         │  gRPC+mTLS  │     ├ Write/Create/Mkdir   │
│        │                             │  — or —     │     ├ Rename/Unlink/SetAttr│
│   ┌────▼─────────────────────┐       │  cloud      │     └ StatFs               │
│   │ wendy device mount       │◄──────┼──tunnel─────┤            │               │
│   │  go-nfs / x-net-webdav   │       │             │   scoped to                │
│   │  frontend                │       │             │ /var/lib/wendy/volumes/<v> │
│   │      ▲                   │       │             └────────────────────────────┘
│   │  VolumeFS gRPC adapter ──┼───────┘
│   └──────────────────────────┘
└─────────────────────────────────────┘
```

The device answers stateless, path-based filesystem RPCs. The host gateway
re-exposes them as a network filesystem the OS mounts natively.

## Device side — `WendyVolumeFsService`

New proto: `Proto/wendy/agent/services/v2/volume_fs_service.proto`. A dedicated
service (not the `file_sync` stub) with path-based, mostly-unary RPCs. NFS is
stateless/handle-based and WebDAV is path-based, so a path+volume model maps
cleanly to both.

| RPC | Purpose |
|-----|---------|
| `Stat(volume, path)` → `Attr` | getattr / lookup (size, mode, mtime, type) |
| `ReadDir(volume, path)` → `repeated DirEntry{name, Attr}` | readdir-plus: attrs inline to avoid per-entry stats |
| `Read(volume, path, offset, length)` → `bytes` | capped at ~1 MiB/call (NFS rsize is 64 KiB) |
| `Write(volume, path, offset, bytes)` → `written` | |
| `Create(volume, path, mode)` | new file |
| `Mkdir(volume, path, mode)` / `Rmdir(volume, path)` | |
| `Unlink(volume, path)` | |
| `Rename(volume, from, to)` | |
| `SetAttr(volume, path, {mode?, size?, mtime?})` | chmod / truncate / touch |
| `StatFs(volume)` → `{total_bytes, free_bytes}` | so `df` and the Finder size bar work |

`Attr` carries: type (file/dir/symlink), size, mode bits, mtime, and for
symlinks the target (read-only).

### Path scoping & security

- Every RPC takes a `volume` name and a volume-relative `path`.
- The agent resolves `/var/lib/wendy/volumes/<volume>/<clean(path)>` and
  **rejects any path that escapes the volume root**: reject absolute paths,
  reject `..` traversal, and resolve symlinks — refusing any that resolve
  outside the volume root.
- Auth is the existing mTLS / cloud-tunnel channel. This is the same trust level
  as today's `ListVolumes` / `RemoveVolume`, so **no new entitlement** is
  introduced.

### Write durability

- The agent applies each `Write` / `SetAttr` directly to the volume directory
  and is `fsync`-aware so a clean unmount leaves a consistent tree.
- No server-side write-back cache that could lose data on disconnect.

## Host side — gateway & frontends

One `VolumeFS` adapter in the CLI translates filesystem calls into
`WendyVolumeFsService` RPCs. Two thin frontends sit on top, selected by OS:

- **NFSv3** (macOS, Linux) via `github.com/willscott/go-nfs`, a userspace NFS
  server backed by a `go-billy` filesystem. The adapter implements
  `billy.Filesystem`.
- **WebDAV** (Windows) via `golang.org/x/net/webdav`. The adapter implements
  `webdav.FileSystem`.

Both frontends are thin glue over the *same* gRPC adapter; no protocol-specific
business logic is duplicated.

### CLI surface

New file `go/internal/cli/commands/device_mount.go`, registered under the
existing `device` command group next to `volumes`.

```
wendy device mount <volume> [mountpoint] [flags]
  --protocol nfs|webdav   override the per-OS default
  --read-only             mount read-only (default: read-write)
  --port <n>              loopback port (default: ephemeral)

wendy device unmount <volume|mountpoint>   # convenience; Ctrl-C also unmounts
```

### Mount flow

1. `resolveTarget(ctx)` → device gRPC connection (same helper `volumes.go`
   uses). Error clearly if the target is not a WendyOS device.
2. Validate the volume exists via the existing `ListVolumes`.
3. If `used_by` is non-empty (a running app holds the volume), print a one-time
   warning that concurrent writes from the PC and the app can corrupt data, and
   recommend mounting volumes of stopped apps. Proceed (read-write is the
   default), but loudly.
4. Start the loopback server on `127.0.0.1:<port>` (NFS or WebDAV).
5. Auto-mount with a user-writable mountpoint (no root):
   - macOS/Linux: shell out to `mount_nfs` / `mount -t nfs` against
     `127.0.0.1`.
   - Windows: `net use <drive>: http://127.0.0.1:<port>/`.
6. Foreground: the process holds the mount and prints the mountpoint/drive.
   `Ctrl-C` (or `wendy device unmount`) cleanly `umount`s then stops the server.

### Default mountpoint

- macOS/Linux: `~/Wendy/<device-name>/<volume>`, created if absent.
- Windows: next free drive letter.
- Overridable via the positional `mountpoint` argument.

## Error handling — no silent failures

- The agent returns typed gRPC codes: `NotFound`, `PermissionDenied`,
  `InvalidArgument` (path escape), `ResourceExhausted` (ENOSPC).
- The host adapter maps these to POSIX errno (`ENOENT`, `EACCES`, `EINVAL`,
  `ENOSPC`) so the OS reports sane errors to apps.
- Mount-time failures (no NFS support on the OS, `mount` exits non-zero, port in
  use) surface a clear message and a non-zero exit. Never a half-mounted silent
  state.

## Performance

- v1 leans on the NFS client's own attribute/read caching (`actimeo` mount
  option); `ReadDir` returns attrs inline to avoid per-entry round-trips.
- Read/Write chunked at ~1 MiB.
- Host-side read-ahead and write coalescing are noted as a *later* optimization,
  not v1.

## Testing

- **Agent unit:** path-containment (reject `..`, absolute, symlink-escape) and
  each RPC exercised against a temp directory.
- **Host adapter unit:** the billy and webdav adapters against an in-memory fake
  `WendyVolumeFsService`.
- **E2E:** mount → write a file from the host → assert it appears under
  `/var/lib/wendy/volumes/<volume>` on the device → read it back → unmount
  cleanly. Follows the existing wendy-agent E2E pattern.

## Files touched (anticipated)

- `Proto/wendy/agent/services/v2/volume_fs_service.proto` (new) + generated
  Go stubs.
- Agent: new `volume_fs_service.go` service implementation under
  `go/internal/agent/services/`, registered with the gRPC server; path-scoping
  helper.
- CLI: `go/internal/cli/commands/device_mount.go` (new), registered in
  `device.go`. `VolumeFS` gRPC adapter + go-nfs/webdav frontends, likely under
  `go/internal/cli/mount/` (or similar).
- Tests as above.
