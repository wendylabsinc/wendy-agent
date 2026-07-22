# RemoteCam

**Device B** in a two-device demo: this app captures a webcam and streams
raw RGB frames to a single remote peer over the WendyOS mesh, controlled by
simple start/stop commands sent over the same TCP connection.

(Sources live in [`./camserver`](./camserver); the service is named
`camserver` in [`wendy.json`](./wendy.json).) A separate app — "Device A", a
viewer that dials `camserver` and renders the stream — is built to the same
wire protocol as a companion piece to this one; it is not part of this repo.

## What it demonstrates

One `camera` entitlement plus one `network` entitlement on the `camserver`
service in [`wendy.json`](./wendy.json):

```json
{ "type": "camera" }
```

```json
{
  "type": "network",
  "mode": "mesh",
  "serviceCIDR": "10.99.0.0/16",
  "ports": [{ "host": 9090, "container": 9090 }]
}
```

- `camera` grants access to the host's V4L2 device (`/dev/video*`).
- `mode: "mesh"` grants the container egress to the mesh service CIDR, and
  `ports` publishes host port 9090 so a peer can dial in and reach
  `camserver`'s listener at `device-<idOfThisDevice>.cloud.wendy.dev:9090`.
- `isolation: "isolated"` (top-level) is required for the mesh route to have
  a network namespace to live in — see [HelloMesh's
  README](../HelloMesh/README.md#why-isolation-isolated) for the full
  explanation; the same reasoning applies here.

Unlike [HelloMesh](../HelloMesh), which is a symmetric fleet where every node
is a peer, RemoteCam is a directed pair: this app only serves (it never
dials out), and only ever talks to one client at a time.

## Wire protocol

Raw TCP, one connection at a time, both directions on the same socket. Every
frame is `[1-byte type][4-byte big-endian uint32 length][length bytes payload]`:

| Type | Name        | Direction       | Payload                                                                 |
| ---- | ----------- | --------------- | ------------------------------------------------------------------------ |
| 0x01 | `CMD_START` | client → server | empty — begin capturing + streaming                                    |
| 0x02 | `CMD_STOP`  | client → server | empty — stop streaming, keep the connection open                       |
| 0x10 | `FRAME_RGB` | server → client | `[uint16 width][uint16 height][width*height*3 bytes RGB, row-major]`   |
| 0x7F | `ERR`       | either          | UTF-8 message; receiver logs it and closes                             |

Fixed demo parameters (no negotiation): 320x240, ~2fps (500ms between
captures), port 9090. `camserver` accepts one connection at a time — a second
connection attempt while one is active is rejected/closed immediately rather
than queued. On disconnect (or a protocol error), it stops streaming and lets
the camera go: capture opens and closes the V4L2 device on every single
frame (see [`CamCapture.swift`](./camserver/Sources/CamCapture/CamCapture.swift)),
so there is nothing left holding `/dev/video*` open once the connection ends.

The full spec (session behavior, rationale) lives in
[`camserver/Sources/RemoteCamWire/WireProtocol.swift`](./camserver/Sources/RemoteCamWire/WireProtocol.swift).

## Code layout

- [`CLinuxVideo`](./camserver/Sources/CLinuxVideo) — the same C V4L2 ioctl
  shim as [HelloVideo](../HelloVideo), vendored in so this app builds as its
  own standalone container image.
- [`CamCapture`](./camserver/Sources/CamCapture) — a trimmed, JPEG-free port
  of HelloVideo's `LinuxVideo` module: opens a V4L2 device, runs the same
  ioctl sequence (`S_FMT` → `REQBUFS` → `QUERYBUF` → `QBUF` → `STREAMON` →
  mmap → `DQBUF` → `STREAMOFF`) HelloVideo uses, and converts YUYV to flat
  RGB bytes instead of JPEG (this app sends raw RGB, no JPEG dependency
  needed). **HARDWARE-UNVERIFIED** — see below.
- [`RemoteCamWire`](./camserver/Sources/RemoteCamWire) — the wire protocol
  and TCP plumbing: plain POSIX sockets (`socket`/`bind`/`listen`/`accept`/
  `read`/`write` via Glibc on Linux, Darwin on macOS — no SwiftNIO, no new
  dependencies) plus frame encode/decode. This is the one part of the app
  that builds and type-checks on macOS too.
- [`camserver`](./camserver/Sources/camserver) — the executable: accepts one
  connection at a time, runs a command-reader loop (CMD_START/CMD_STOP) on
  one thread and a capture-and-send loop on another, coordinated by a small
  locked state object.

## Run it

Deploy to Device B:

```bash
cd Examples/RemoteCam
wendy run
```

Note the asset ID `wendy run` reports (or `wendy cloud discover --json`) —
Device A dials this device at `device-<idOfDeviceB>.cloud.wendy.dev:9090`.

Logs on Device B look like:

```
[camserver] RemoteCam camserver starting (V4L2 capture path is HARDWARE-UNVERIFIED — see README)
[camserver] listening on 0.0.0.0:9090
[camserver] client connected
[camserver] CMD_START received; streaming at ~2fps, 320x240
[camserver] CMD_STOP received; streaming paused
[camserver] client disconnected: connection closed by peer
[camserver] connection closed; camera released
```

## HARDWARE-UNVERIFIED

There is no V4L2-capable Linux device with a webcam available in this
environment, so **the camera-capture path (`CamCapture`) has never been run
against real hardware** — only type-checked by careful reading against
HelloVideo's proven ioctl sequence, which it copies verbatim. Do not treat it
as tested. What *has* been verified:

- `wendy json validate` passes against this branch's schema (the globally
  installed `wendy` CLI predates mesh-mode support and rejects `mode: "mesh"`
  on **any** app, including the pre-existing HelloMesh example — that's a
  stale-binary issue, not a problem with this app's `wendy.json`).
- `swift build --target RemoteCamWire` succeeds on macOS: the wire protocol
  and socket code type-check and build cleanly, cross-platform.
- The framing logic (`readFrame`/`writeFrame`/`encodeFrameRGBPayload`) was
  exercised against an in-memory fake socket in a throwaway harness,
  confirming big-endian length encoding, exact `FRAME_RGB` payload byte
  layout, unknown-frame-type rejection, and truncated-read handling all match
  the spec.
- `swift build` (full package) fails at the same boundary HelloVideo already
  fails at on macOS — `CLinuxVideo` needs `<linux/videodev2.h>`, which
  doesn't exist outside Linux — so `CamCapture` and the `camserver`
  executable target were never compiled here, only type-checked by
  inspection.

## Debugging tips

- No frames arriving after `CMD_START`: check `camserver`'s logs for
  `capture/send failed` — that means `VideoDevice.firstCaptureDevice()`
  couldn't find a `/dev/video*` node with capture capability, or a V4L2
  ioctl failed (permissions, wrong pixel format support, device busy).
- Client can connect but the server immediately closes: it likely sent
  something other than `CMD_START`/`CMD_STOP` first — `camserver` treats any
  other frame type as a fatal protocol error (`ERR`, then close).
- Peer can't reach `device-<id>.cloud.wendy.dev:9090` at all: same mesh
  plumbing checks as HelloMesh apply — see [HelloMesh's "Debugging the
  plumbing"](../HelloMesh/README.md#debugging-the-plumbing) (iptables
  `WENDY-MESH` chain, `nsenter`-based route check, etc).
