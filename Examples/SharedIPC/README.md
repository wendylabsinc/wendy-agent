# SharedIPC

Demonstrates `isolation: shared-ipc` — a multi-service app where all
containers share Linux IPC, network, and UTS namespaces plus a common
`/dev/shm` (WDY-881, WDY-882, WDY-883, PR #897).

```
SharedIPC/
├── wendy.json    ← isolation: shared-ipc, primary + secondary services
├── primary/      ← namespace owner; writes messages to /dev/shm/channel
└── secondary/    ← joins primary's namespaces; reads from /dev/shm/channel
```

## What shared-ipc provides

| Resource | Behaviour |
|---|---|
| IPC namespace | Shared: POSIX semaphores, message queues, and shared memory objects are visible to all services |
| Network namespace | Shared: all services bind on the same network interfaces |
| UTS namespace | Shared: all services see the same hostname |
| `/dev/shm` | Shared bind-mount at `/run/wendy/shm/<appId>`; backed by `tmpfs` on the host (WDY-882) |

The **primary** service starts first (it is the namespace owner).  When the
**secondary** starts, the agent opens namespace file descriptors under
`/proc/<primary-pid>/ns/` and embeds the `/proc/self/fd/<n>` paths into the
secondary's OCI spec.  Opening the fds *before* releasing the mutex ensures
the namespace cannot be recycled by a concurrent PID reuse — a TOCTOU guard
introduced in WDY-881.

## Use cases

- **Robotics** — a sensor driver and a processing node share a zero-copy ring
  buffer in `/dev/shm`.
- **High-performance networking** — two services bind to the same port on the
  same network stack (HAProxy + app, DPDK + worker, etc.).
- **Shared POSIX semaphores** — coordinate two processes without a network
  round-trip.

## wendy.json

```jsonc
{
  "appId": "sh.wendy.examples.sharedipc",
  "platform": "linux",
  "isolation": "shared-ipc",
  "services": {
    "primary": {
      "context": "./primary"
    },
    "secondary": {
      "context": "./secondary",
      "dependsOn": ["primary"]   // primary starts first; it owns the namespaces
    }
  }
}
```

## Run

```sh
cd Examples/SharedIPC
wendy run
```

Expected output:

```
[primary] WENDY_HOSTNAME = primary.local
[primary] writing to /dev/shm/channel
[primary] wrote: 'ping-1'
[secondary] WENDY_HOSTNAME = secondary.local
[secondary] waiting for primary to write to /dev/shm/channel ...
[secondary] read: 'ping-1'
[primary] wrote: 'ping-2'
[secondary] read: 'ping-2'
[primary] wrote: 'ping-3'
[secondary] read: 'ping-3'
[primary] wrote: 'done'
[secondary] read: 'done'
[secondary] ✓ wrote ack to /dev/shm — shared memory works
[primary] waiting for secondary to acknowledge ...
[primary] ✓ secondary acknowledged, /dev/shm sharing works
```

## See also

- [IsolatedServices](../IsolatedServices) — isolated mode with CNI + /etc/hosts
- [ROS2](../ROS2) — shared-ipc isolation with ROS2 framework config
