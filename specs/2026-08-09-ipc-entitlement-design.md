# `ipc` entitlement: app-to-app unix sockets without sharing a data volume

Date: 2026-08-09
Status: Proposed

## Problem

Two apps on one device can already share a unix socket — by declaring the same
`persist` name. That works, and it over-grants badly: `persist` mounts the whole
volume, so an app that only needs to *call* a service also gets read/write on
that service's database. There is no way to express "may connect to this
service" without also saying "may rewrite its data".

A hardware spike on an AGX Orin confirmed both halves: two distinct `appId`s
sharing a `persist` `name` do see the same mount, a unix socket created by one
is connectable by the other, and each app has to hand-roll everything around it.
The spike surfaced four problems the platform should own rather than each app:

1. **Stale sockets.** The provider had to `rm -f` its socket on boot. A provider
   that dies without cleanup leaves a socket file consumers can open but not
   connect to, and that the provider itself cannot rebind (`EADDRINUSE`).
2. **Discovery by convention.** Both sides hardcode the path. A typo in the
   shared namespace is indistinguishable from "provider not running".
3. **Permissions.** Two non-root apps sharing a volume need uid/gid coordination
   that nothing manages.
4. **Cold-start ordering.** Consumers must hand-roll bounded retry.

## Why not `network` mode `mesh`

`mesh` is a *cross-device* mode and does not cover this. Its design goal is "let
a container on device A reach a service on device B"
(`specs/2026-07-02-mesh-data-plane-design.md:8-16`); addressing is by cloud asset
ID mapped to a VIP in the service CIDR (`go/internal/agent/mesh/vip.go:26-34`);
transport is LAN-direct mTLS gRPC or cloud-broker relay
(`go/internal/agent/services/mesh_dialer.go:152-175`); and `discoverOnLAN`
deliberately discards loopback addresses
(`go/internal/agent/services/mesh_dialer.go:211-236`). Service-name discovery is
explicitly out of scope for v1 (`specs/2026-07-02-mesh-data-plane-design.md:161-163`).
The only same-board mechanism that exists today is `/etc/hosts` generation for
services *within one isolated multi-service app*
(`go/internal/agent/containerd/client.go:1330-1352`) — not across apps.

## Existing precedent

WendyOS already has exactly this pattern for *agent*-provided sockets, and it is
the code path this design generalizes rather than replacing:

- `admin` — `applyAdmin` (`go/internal/agent/oci/entitlements.go`) bind-mounts
  the *parent directory* of the agent control socket read-only and injects
  `WENDY_AGENT_SOCKET`. Its docs say "the entitlement-gated socket mount is the
  entire trust boundary".
- `notifications` — `AppSystemAPISocketManager` owns a per-app directory under
  `/var/lib/wendy/app-system` with mode `0750 root:2000`, `applySystemAPI`
  mounts it read-only, injects `WENDY_SYSTEM_SOCKET`, and adds GID 2000 so
  non-root apps can connect. Ownership is refcounted per service container and
  restored from container labels after an agent restart.

Both mount a **directory**, never the socket inode, so socket recreation is
transparent to a running container; both live on `/var/lib` rather than tmpfs so
the mount's inode survives reboot. This design reuses all of it.

## Design

### 1. `wendy.json`

```json
{ "type": "ipc", "name": "world", "role": "provide" }
{ "type": "ipc", "name": "world", "role": "consume" }
```

- `name` — lowercase RFC 1123 label, validated by
  `appconfig.ValidateIPCName` with the same pattern as `serviceName`.
  Unlike `appID` (which permits dots and is therefore hashed before use as a
  path), a name that passes is safe to use verbatim as a directory component, so
  `ls /var/lib/wendy/ipc` stays a readable list of what is registered.
- `role` — `provide` or `consume`.
- At most one entitlement per name per container (two entries would produce two
  bind mounts on one destination).

### 2. Agent: `services.IPCSocketManager`

Modelled directly on `AppSystemAPISocketManager`.

- Host directory `/var/lib/wendy/ipc/<name>`, mode `2770`, owner
  `root:2001`. Disk-backed for the same reason as `AdminAgentSocketHostPath`:
  the bind mount pins the inode and tmpfs is wiped on reboot.
- `Ensure(name, role, appID, serviceName)` registers a container as an owner and
  returns the directory. **First claim wins for `provide`**: a second app cannot
  take a name a consumer already trusts. Never touches the socket, so the
  agent-restart restore path can call it against a live provider.
- `PrepareProvider(name, appID, serviceName)` removes a stale socket, called
  from `StartContainer` before `container.NewTask` so the provider's first
  `bind()` always lands on an empty directory. No-op for a container that does
  not provide the name.
- `Release` / `ReleaseApp` drop claims. The directory is removed only once **no**
  provider and **no** consumer remains — removing it while a consumer still has
  it mounted would replace the inode the consumer is pinned to.
- Every identity is re-validated at the manager boundary; labels and RPC input
  are untrusted (SOC2-CC6, NIST-SI-10) and the name becomes a host path.

### 3. Agent: OCI spec

`applyIPCSocket` mounts `/run/wendy/ipc/<name>` with
`rbind,nosuid,noexec,nodev` plus `rw` for `provide` and `ro` for `consume`, adds
supplementary GID 2001, and injects
`WENDY_IPC_<NAME>_SOCKET=/run/wendy/ipc/<name>/ipc.sock`.

A read-only mount still permits `connect()`: the kernel's read-only-superblock
check (`sb_permission`) returns `EROFS` only for `S_ISREG`/`S_ISDIR`/`S_ISLNK`,
never for sockets. Read-only therefore denies a consumer exactly the right
thing — unlinking or replacing the provider's socket — while allowing the
connection.

Only names present in `ApplyOptions.IPCSocketDirs` are mounted, so an app
cannot mount a name the manager refused by declaring it in its own `wendy.json`.

### 4. Agent: lifecycle wiring

| Point | Action |
|---|---|
| `CreateContainerWithProgress` | `Ensure` every declared name; **fail-closed** — a provider must not start believing it owns a socket it doesn't, and a consumer must not start with a silently absent mount. Rolled back on any later create failure. |
| Replace path | Release the replaced container's claims so a redeploy that drops a `provide` frees the name. |
| `StartContainer` | `PrepareProvider` for each provided name before the task is created. Fail-closed. |
| `deleteOne` | Release claims *after* the container is gone — a claim released while a merely-stopped container exists would let another app take the name. |
| `DeleteContainer` (whole app) | `ReleaseApp` sweeps claims `deleteOne` could not attribute. |
| Agent start | `RestoreIPCSockets` rebuilds claims from container labels, never touching sockets. |

Stopped containers keep their claim: they retain the bind mount and are expected
to reclaim their socket on the next start. This matches `notifications`.

## How this addresses the spike's four problems

1. **Stale sockets** — owned by the platform (`PrepareProvider`).
2. **Discovery** — the path is injected as an env var and is identical on both
   sides; a consumer whose name has no provider is warned at deploy time, with
   the names that do exist.
3. **Permissions** — the setgid `2770 root:2001` directory plus supplementary
   GID 2001 handles *traversal* for both sides. It does **not** fully solve the
   socket's own mode; see below.
4. **Cold-start ordering** — **not** addressed; see below.

## Known limitations

### Non-root socket mode

`connect()` on a unix socket requires write permission on the socket inode, and
the socket's mode is set by whoever calls `bind()`. With a default umask that is
`0755`, so a **non-root** consumer connecting to a **non-root** provider's socket
gets `EACCES`. The setgid directory fixes the socket's *group* automatically but
cannot fix its *mode*.

Root-run providers and consumers — the current WendyOS default, and what the
hardware spike exercised — are unaffected. For non-root, the provider must
`umask(0o007)` before binding or `chmod` the socket to `0660`. This is
documented on all three doc surfaces rather than left implicit.

The clean fix is **socket activation**: the agent binds and listens, with a mode
and owner it fully controls, and passes the listening fd into the container. That
removes both this limitation and the cold-start ordering problem (a consumer can
connect before the provider is up, and the kernel queues it). It is deliberately
**deferred**: passing an fd into a containerd-managed task needs runtime plumbing
(`--preserve-fds` / `LISTEN_FDS`) that is a substantial change on its own and
should not be entangled with the entitlement's schema and lifecycle.

## Naming

This was first drafted as a `service` entitlement. That collides conceptually
with two existing `wendy.json` concepts — the top-level `services` map
(containers within one app) and `serviceName` — and since the keyword is public
API it was cheaper to settle before it landed. Renamed to `ipc`, which does not
collide with anything in the schema and says what the mechanism is. The `role`
values stay `provide`/`consume`: those read fine and collide with nothing.

## Testing

- `appconfig`: name/role validation, traversal rejection, duplicate-name
  rejection, unknown-key rejection, schema↔Go sync guard on the role enum and
  name pattern, annotation round-trip.
- `oci`: provider gets `rw`, consumer gets `ro`, both get the env var and GID,
  multiple names get separate mounts, an ungranted or malformed name is never
  mounted, a consumer receives no unrelated mount.
- `services`: directory mode and ownership, provider-conflict rejection, stale
  socket removal, non-provider cannot clear a live socket, directory survives a
  provider release while consumers remain, refcounting across sibling service
  containers, malformed identities create nothing on disk, a failed `Ensure`
  leaves no phantom claim.
- `containerd`: label round-trip of name+role, malformed label entries dropped,
  nil-provider no-op.

## Out of scope

- Socket activation (see above).
- Cross-device service access — that is `network` mode `mesh`.
- Any wire protocol over the socket. The platform provides the socket; what
  flows through it is the apps' business.
- Multiple sockets per IPC name.
