# IP cameras as a first-class camera transport

Date: 2026-08-05

## Problem

An Internet Protocol (IP) camera cabled into a device's ethernet port takes
substantial manual work to reach. Getting a Reolink RLC-520A working on
`wendyos-wendystudio-parakeet-demo.local` required, by hand: bringing up `eth0`
with an address on the camera's subnet, discovering that the camera had no
address at all, running a Dynamic Host Configuration Protocol (DHCP) server to
lease it one, finding the leased address, and writing an application to pull Real
Time Streaming Protocol (RTSP) and re-serve it.

None of that is visible to `wendy device camera list`, which sees only Universal
Serial Bus (USB) and Camera Serial Interface (CSI) devices. The camera is
invisible to the platform even while an application is streaming from it.

CSI is the precedent to follow. CSI cameras were not bolted on beside USB
cameras; they were added *into* the existing camera surface as a second
transport, with transport classification in `internal/agent/camera`, a
transport-specific capture path in `runProducer`, and transport-specific
preflight in `StreamVideo`. IP becomes the third transport by the same route.

## Goal

`wendy device camera list` shows IP cameras next to USB and CSI cameras.
`wendy device camera view` streams them. A container holding the `camera`
entitlement opens `/dev/videoN` and gets IP camera frames without knowing the
camera is remote. When several cameras exist, an interactive picker appears.

## Non-goals

- Recording, motion detection, or inference. Those belong to applications.
- Camera configuration (exposure, infrared mode, on-screen display). The camera's
  own web interface remains the place for that.
- Pan-tilt-zoom control.
- Cameras reachable only through a cloud relay. Local network only.

## Architecture

A new device-side package, `go/internal/agent/ipcam`, holds everything specific
to IP cameras. Existing code gains branches, not logic.

### Identity and addressing

An IP camera is identified by its **Media Access Control (MAC) address**, not its
IP address, which moves. MAC is the IP camera's equivalent of a USB camera's
stable device path.

Each registered camera is allocated a **device ID from a reserved band starting
at 200** at registration time, recorded in the registry and stable across
reboots. `maxVideoDeviceID` is 255 (the kernel's `VIDEO_NUM_DEVICES` bound), so
the band holds 56 cameras, and physical `/dev/videoN` nodes never reach it.

That number is also the camera's future v4l2loopback node number. In phase 1 the
camera's `id` is 203 and its `path` is empty; in phase 3 the same camera is
`/dev/video203`. The identifier the user learns does not change when loopback
lands.

### Components

**`ipcam.Registry`** — the set of known cameras, persisted as JSON under the
agent state directory. Per camera: MAC, allocated ID, last-known address,
vendor and model, Open Network Video Interface Forum (ONVIF) service address,
RTSP stream paths, whether credentials are stored, the link it was seen on, and
first and last seen timestamps. The registry is the only component that writes
persistent state.

**`ipcam.Discoverer`** — three read-only probes, on a timer and on link change:

1. ONVIF WS-Discovery: a multicast `Probe` for `NetworkVideoTransmitter` on
   User Datagram Protocol (UDP) port 3702, parsing `XAddrs` from responses. This
   is the industry standard and covers Reolink among others.

   The probe is sent from every local IPv4 address, not from an unbound socket.
   An unbound socket sends multicast out of the default route only, which is the
   uplink, so a camera on its own link would never see it.
2. The DHCP lease table from the link manager, which catches a camera that will
   not answer ONVIF until it has an address.
3. A direct liveness check of cameras already known, because a camera holding a
   lease from an earlier session needs nothing and so answers no probe. Without
   this a known camera would report offline forever.

A camera found by any probe is upserted into the registry by MAC.

**`ipcam.LinkManager`** — makes a directly-cabled camera work without manual
setup. For each ethernet interface that has carrier and no IPv4 address of its
own, it watches DHCP traffic on an `AF_PACKET` socket.

A packet socket rather than a UDP one, for two reasons that only appear on real
hardware. The link has no IPv4 address yet, which is the entire point, and the
kernel does not deliver IPv4 datagrams to a socket on an addressless interface,
so a UDP listener sees nothing at all. And a competing server's `DHCPOFFER` is
addressed to the client port, 68, which a socket bound to the server port would
never see, defeating the guard that exists to detect exactly that.

The guards matter more than the feature:

- It serves DHCP only after observing a `DHCPDISCOVER` that no other server
  answered within a timeout.
- If it ever sees a `DHCPOFFER` from another server on that link, it marks the
  link as having upstream DHCP and never serves there again.
- It never touches an interface that already holds an IPv4 address, which is how
  the device's own uplink is excluded. Wireless and point-to-point interfaces are
  excluded outright.

Lifecycle matters as much as the guards. Claiming a link adds our address to it,
which makes the link fail the very eligibility test that got it claimed, so a
claimed link is judged only on carrier from then on. Carrier loss is debounced
over two scans, because a camera rebooting drops carrier for a few seconds and
tearing the segment down for that would churn both the address and the lease. A
release removes the address we added and cancels that link's server: leaving the
address behind would keep the link permanently ineligible, stranding the segment
so the camera could never renew.

Leases come from `10.98.<n>.0/24`, chosen over `192.168.x` to avoid colliding
with a home or office network on the other side of the uplink.

The DHCP server is implemented in Go rather than by adding `dnsmasq` to the
rootfs: no new Yocto dependency, unit-testable against canned packets, and the
lease table becomes a direct input to the registry instead of a file to scrape.

**`ipcam.Credentials`** — IP cameras need authentication and USB cameras do not.
This is the one irreducible difference in the user experience.
Credentials resolve in two steps, and only for a camera that reported it has
none, so a local camera never prompts: `wendy.json`'s camera entitlement first
(`user`/`password`, for unattended deploys), then an interactive prompt without
echo, or `WENDY_CAMERA_PASSWORD` where there is no terminal.

The agent stores the secret under its state directory at mode 0600, keyed by
MAC, written atomically. It is **not encrypted at rest**: the protection is file
permissions in a root-owned directory, so anything with root on the device can
read it. Encrypting it with the device identity key material would be a genuine
improvement and is deliberately out of scope here.

The value is write-only across the wire: `list` reports `has_credentials`, never
the secret.

**`ipcam.Loopback`** (phase 3) — creates a v4l2loopback node at the camera's
allocated number through the module's control device, and supervises a pump
feeding it from RTSP. The pump uses GStreamer
(`rtspsrc ! rtph264depay ! avdec_h264 ! videoconvert ! v4l2sink`) because
GStreamer is already in the image and the agent already shells out to
`gst-launch-1.0`. Pumps are started when an entitled container starts,
stopped after the last container consumer goes idle; `camera view` streams
RTSP directly and neither starts nor needs a pump (WDY-2474).

## Changes to existing code

**Protocol** (`wendy_agent_v1_video_service.proto`), all additive:

- `VideoTransport` gains `VIDEO_TRANSPORT_IP = 3`.
- `VideoDevice` gains `address`, `model`, `mac`, `has_credentials`, `online`.
- New remote procedure calls: `SetCameraCredentials`, `ForgetCamera`,
  `RefreshCameras`.

**`video_service.go`**

`listV4L2Devices` becomes `listCameras`: physical enumeration exactly as today,
then registry entries appended. The existing CSI libcamera enrichment is
untouched.

`StreamVideo` currently derives everything from a formatted path:

```go
path := fmt.Sprintf("/dev/video%d", devID)
```

That becomes a `resolveSource(devID)` returning a `videoSource` that is either a
V4L2 node or an IP camera. Three consequences:

- The IP branch sits **ahead of** the `Lstat` and major-81 validation, which an
  IP camera cannot satisfy in phase 1 because no node exists yet.
- The IP branch skips `validateStreamParams`. Its resolution allowlist excludes
  the RLC-520A's 2560x1920, and correctly so: we do not transcode, so the
  resolution is whatever the camera sends. Stream selection replaces it.
- `hubs` is already keyed by an opaque string, so an IP camera keys on `ip:203`
  and the entire `deviceHub` fan-out, subscriber accounting, and teardown
  machinery is reused unchanged.

IP capture **does not transcode**. The camera already emits H.264; the producer
depayloads RTSP to Annex-B and broadcasts. This is cheaper than the USB path, and
the command-line interface's existing keyframe buffer consumes it as-is.

Because this path needs no kernel module, `camera view` works against IP cameras
on current 0.17.0 devices. Only in-container consumption waits on phase 3.

`StreamVideo` gains an IP preflight mirroring the existing Tegra firmware check:
a camera with no stored credentials fails with `ErrorInfo` reason
`IP_CAMERA_NO_CREDENTIALS`, and the command-line interface turns that into
"run `wendy device camera login <id>`", the same shape as the existing
`TEGRA_FIRMWARE_MISMATCH` diagnostic.

**`entitlements.go`** — `applyCamera` already globs `/dev/video*` and needs no
change. The container create path gains one call to ensure loopback nodes exist
before start, for entitled applications.

**`camera.go`** — `transportLabel` gains `ip`. `list` gains Address and Online
columns. New `login` and `forget` subcommands. A picker, below.

## Picker

`camera view` with no `--id` and more than one camera opens
`tui.NewPickerWithTitleAndColumns("Select a camera", …)` with ID, Type, Name and
Address columns, mirroring `audio_devices.go` which already does exactly this for
microphones. A single camera auto-selects with no prompt.

This fixes USB and CSI too. Today `camera view` defaults to `--id 0` and gives no
indication that other cameras exist.

## Error handling

Every failure names the fix:

- No credentials: `IP_CAMERA_NO_CREDENTIALS` becomes a `camera login` hint.
- Wrong credentials: RTSP 401 reports authentication failure against a specific
  address, not a generic stream error.
- Camera registered but unreachable: `list` shows `online: false`; `view` reports
  the last-known address and when it was last seen.
- Discovery finds a camera on a link with upstream DHCP: served silently, since
  that is the normal LAN case.
- v4l2loopback missing (phase 3 on an older release): the pump reports that the
  running WendyOS build lacks the module and that `camera view` still works.

## Testing

Follow the injection-seam style already used in `camera/transport.go`
(`readDriverSymlink`, `runCamList`, `lookupCam`), so sockets and subprocesses are
replaceable in tests.

- ONVIF probe parsing: table tests over canned responses including the real
  RLC-520A reply.
- DHCP server: table tests over canned `DISCOVER` and `REQUEST` packets, plus a
  guard test asserting the link manager refuses a link where another server
  answers, and a second asserting it never touches an interface that already has
  an address.
- Registry: round-trip persistence, MAC-keyed upsert, ID allocation stability
  across restart, and exhaustion of the 200-255 band.
- Credentials: write-only over the wire, correct file mode, survives restart.
- `resolveSource`: physical and IP IDs, out-of-range, and unregistered IDs.
- Pump: interface-level fake, with the real GStreamer path behind an on-device
  integration tag.
- End to end against the RLC-520A on the parakeet device, which is a known-good
  target since it is already streaming.

## Phases

**Phase 1 — registration and viewing.** Registry, ONVIF and mDNS discovery,
credential store, `resolveSource`, the RTSP producer, `list`/`view`/`login`/
`forget`, and the picker. Ships with no kernel change. Testable against any
camera already on the local network.

**Phase 2 — camera link autoconfiguration.** Link manager and Go DHCP server, so
a directly-cabled camera gets an address with no manual step. This is what makes
the parakeet setup zero-touch.

**Phase 3 — container parity.** v4l2loopback node management, the pump, and the
entitlement hook, so containers see `/dev/videoN`.

Phase 3 additionally requires adding the `v4l2loopback` module to the kernel in
`wendyos-builder`, which is a separate repository and needs a release plus an
over-the-air update. That work is tracked separately from this specification, and
the agent-side code detects the module's absence and degrades to a clear message
rather than failing obscurely.

## Security notes

- The link manager serves DHCP only on links it has proven are unmanaged, and
  never on an interface holding an address. Both guards are unit-tested.
- Credentials are never returned over the wire and never written to a container
  image, which is an improvement on the current practice of baking them into
  application environment variables.
- Three residual weaknesses, named rather than glossed: the store is 0600 but not
  encrypted; on a device that is not yet provisioned the command line reaches the
  agent without mutual Transport Layer Security, so the password crosses the local
  network in the clear; and the capture pipeline receives the password in its
  argument vector, so it is visible in the process table to root on the device.
  Redaction covers logs, not `ps`.
- Discovery is passive and read-only. The agent does not attempt authentication
  against devices it finds; credentials are supplied by the operator per camera.
- The `camera` entitlement remains one-size-fits-all, so granting it exposes IP
  camera loopback nodes alongside physical cameras. This matches the existing
  documented behaviour of that entitlement rather than introducing a new rule.
