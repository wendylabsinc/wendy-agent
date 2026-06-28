# Local display output for WendyOS — native rendering (compositor + shell)

**Date:** 2026-06-28
**Status:** Draft (design)
**Branch:** `jo/local-display-kiosk-spec`
**Supersedes:** the earlier kiosk-browser draft (same file, pre-rewrite). WendyOS
renders its **own** native UI; it does **not** run a web browser as the display
surface.

## Goal

Make a WendyOS device drive a monitor plugged into its HDMI/DisplayPort output
with a **first-party, natively-rendered UI**:

- By default the device shows a **WendyOS shell** — an interactive UI (boot
  splash, device status/identity, app launcher/switcher, settings) that WendyOS
  owns and renders, with touch/keyboard input.
- An app can **take over the screen** by rendering its own graphics as a Wayland
  client (GPU-accelerated via EGL/GLES).

Targets **Raspberry Pi 4/5 and NVIDIA Jetson** as co-equal boards.

Explicitly **not** in this design: a kiosk web browser, or treating an app's
HTTP server as the display surface. The display is native.

## Background — why nothing renders today

WendyOS is headless by design, verified across all layers (and confirmed by
auditing the external Yocto layers `wendyos-builder` / `meta-wendyos-rpi`):

- **OS image:** no compositor/Wayland/X11/mesa runtime installed. `wayland` and
  `x11` are explicitly removed in `conf/distro/wendyos.conf:99–105`; `opengl` is
  added (`:108`) only for TensorRT compute. **The blocker is a deliberate config
  decision, not missing hardware** — KMS drivers are present: Pi has
  `CONFIG_DRM_VC4=y` compiled in (`vc4graphics.cfg`) with the `vc4-kms-v3d`
  overlay; Jetson has Tegra DRM in the upstream meta-tegra BSP.
- **Agent:** containers receive devices via OCI entitlements
  (`go/internal/agent/oci/entitlements.go`). No entitlement grants display/DRI
  access. `applyGPU` exposes only NVIDIA *compute* nodes (`/dev/nvidia*`, major
  195) and never `/dev/dri`. A general-purpose **CDI mechanism already exists**
  (`go/internal/agent/cdi/apply.go`) that bind-mounts host libraries + device
  nodes + env + hooks into a container — this is the same mechanism used to
  inject NVIDIA userspace, and it is what we reuse for graphics on Jetson.
- **Apps:** every visual example serves a web UI over HTTP, consumed from a
  remote browser. There is no local rendering path.

## Architecture

Three new components plus an expanded agent role. The agent decides **policy**
(who is on screen) and **plumbing** (what an app container gets); the compositor
is the only process that touches the GPU/DRM.

```
┌──────────────────────────── WendyOS device ────────────────────────────┐
│                                                                         │
│  wendyos-compositor  (wlroots/smithay, systemd)                         │
│    • owns DRM/KMS + libinput (the only thing touching the display)      │
│    • exposes the Wayland socket; composites surfaces                    │
│    • policy: show SHELL by default; fullscreen the APP when it owns     │
│      the display; route input to the focused surface                    │
│         ▲ Wayland            ▲ Wayland            ▲ control socket       │
│         │                    │                    │                     │
│  wendyos-shell (Slint)   app container        wendy-agent ──────────────┤
│    • splash / status      (display entitlement)   • orchestrator        │
│    • launcher / switcher  • Wayland client        • serves shell data   │
│    • settings               drawing via EGL/GLES    + actions (gRPC)    │
│         │ gRPC (local)    • wl socket + /dev/dri   • applies display     │
│         └───────────────────  injected by agent     entitlement         │
│                                                   • display arbitration  │
└─────────────────────────────────────────────────────────────────────────┘
```

### 1. `wendyos-compositor`
A thin wlroots-based Wayland compositor — preferred implementation **smithay
(pure Rust)** to share the Slint toolchain; wlroots/C is the fallback. It is the
only process touching DRM/KMS and input. Deliberately minimal: single output,
single foreground surface, no desktop chrome. It renders the shell, and when an
app owns the display it fullscreens that app's surface and routes input to it.

### 2. `wendyos-shell`
A **Slint** application; a Wayland client of the compositor. Slint is a
declarative embedded UI toolkit, GPU/EGL-accelerated, with built-in
touch/keyboard input, running on both Pi (mesa) and Jetson (NVIDIA EGL). The
shell owns all first-party UI: splash, idle/status (device identity, IP,
agent+OS version, enrollment QR), app launcher/switcher, settings. It is a
**thin client** — it pulls app list / device state and issues
start/stop/claim-display actions from **wendy-agent over the local gRPC
socket**, holding no business logic of its own.

### 3. App as a Wayland client
An app declaring the `display` entitlement gets the Wayland socket + `/dev/dri`
injected and connects to the compositor as a normal `xdg-shell` client, drawing
via EGL/GLES.

### 4. wendy-agent (expanded)
The orchestrator and single source of truth: serves the shell's data/actions
over gRPC, applies the `display` entitlement on app start, and owns **display
arbitration** (one foreground app at a time; the shell is the fallback). The
agent never touches the GPU or DRM — it only sets policy and plumbing.

## The `display` entitlement

A capability flag, like `gpu` (not a port/route declaration):

```json
{ "entitlements": [ { "type": "display" } ] }
```

- `appconfig`: add `EntitlementDisplay = "display"` to the type constants and
  `ValidEntitlementTypes`; `allowedKeys["display"] = {"type"}`.
- Validation: at most one `display` entitlement per app (mirrors the `mcp`
  rule). No required sub-keys for v1.
- Unlike the earlier browser-based sketch, this **is** a real device-passthrough
  entitlement handled in `oci/entitlements.go`'s apply switch (a new
  `applyDisplay`), following the `applyGPU`/`applyAudio` pattern.

### What `applyDisplay` injects

| | Raspberry Pi (mesa) | Jetson (NVIDIA) |
|---|---|---|
| Render devices | `/dev/dri/card*` + `/dev/dri/renderD*` (major 226), cgroup `rw` (no mknod), `render`+`video` GIDs | same `/dev/dri` nodes |
| GL userspace | **app ships its own mesa** — works against the host vc4 kernel driver because the DRM render-node ABI is stable across versions | **injected from host via CDI** — reuse `cdi.ApplyCDIDevice` to bind-mount `libEGL_nvidia` / `libGLESv2_nvidia` / etc., the same way CUDA is injected; requires `NVIDIA_DRIVER_CAPABILITIES` to include `graphics,display` |
| Wayland | bind-mount the compositor's socket into the container's `XDG_RUNTIME_DIR`; set `WAYLAND_DISPLAY` + `XDG_RUNTIME_DIR` | same |

The Pi/Jetson asymmetry is the crux of the whole feature: **Pi is easy** (stable
kernel-ABI render node, app-shipped mesa); **Jetson requires host-userspace
injection via CDI** and depends on `nvidia-l4t-graphics` being installed in the
image (an OS-image task). Pi is the reference target; Jetson rides the existing
CDI mechanism.

## Agent ↔ compositor control channel

The compositor exposes a small **root-only control socket**
(`/run/wendyos/compositor.sock`, line-delimited JSON) with commands:
`present <app_id>`, `release`, `status`.

Flow:
1. Agent starts an app carrying `display`, setting a deterministic Wayland
   `app_id` on the container (derived from `WENDY_APP_ID`).
2. The app connects to the compositor as an `xdg-shell` client advertising that
   `app_id`.
3. Agent calls `present <app_id>`; the compositor fullscreens that surface and
   tells the shell to step back.
4. App stops/crashes → agent calls `release` (also via OCI poststop hook) → the
   compositor falls back to the shell.

The agent owns arbitration (one foreground display-app at a time). The shell
only *requests* transitions through the agent's gRPC; it never drives the
compositor directly.

## Security stance

- **Apps without `display` are unaffected** — `/dev/dri` stays withheld exactly
  as today, so the default container sandbox is byte-for-byte unchanged. This is
  an enforced invariant with a test.
- **Apps with `display`** gain GPU render-node + Wayland access; a real
  privilege escalation gated behind the opt-in entitlement.
- Hardening for v1: (a) the compositor exposes **no** screencopy /
  foreign-toplevel / virtual-input protocols to app clients — a client cannot
  capture the screen, read other surfaces, or synthesize input; (b) the control
  socket is agent-only by filesystem permissions.

## OS image (external Yocto layers)

A dedicated **display-enabled image variant** — not a flag on the base distro,
because `wayland` is stripped distro-wide in `wendyos.conf` and re-adding it
would change every build. The variant adds:

- `wayland` back into `DISTRO_FEATURES`; `libdrm` + `libgbm` + `libinput` +
  `libxkbcommon`.
- `wendyos-compositor` and `wendyos-shell` recipes (Rust toolchain via
  `meta-rust` / `cargo-bitbake`; Slint resolves through Cargo).
- systemd units: `wendyos-compositor.service` (owns the seat/DRM), then
  `wendyos-shell.service` ordered after it.

Per board:
- **Pi** — `vc4-kms-v3d` already in the kernel; add the mesa runtime
  (`libgles2` / `libegl` / `libgbm`) and set `gpu_mem≥128` via a `config.txt`
  bbappend. Lowest risk; reference target.
- **Jetson** — Tegra DRM is in the upstream BSP; enable the currently
  **commented-out `nvidia-l4t-graphics`** package, and generate a CDI spec with
  `graphics,display` capabilities (`nvidia-ctk`) so `applyDisplay` can inject it
  into app containers. Higher risk; rides existing CDI.

## Testing strategy

- **Agent (this repo, CI-testable):** `display` entitlement parse/validate;
  `applyDisplay` injects the correct `/dev/dri` nodes + cgroup rules + Wayland
  mount + (Jetson) CDI edits; the **invariant that a non-`display` app's OCI
  spec is unchanged**; arbitration (one foreground app, fallback to shell on
  release).
- **Compositor + shell (CI-testable in a VM):** wlroots/smithay run in a
  **nested backend** — the compositor renders into a window on a dev machine's
  existing Wayland/X session, so shell rendering and app-switching logic are
  testable without a device (compositor shows the Slint shell, switches to a
  stub Wayland app on `present`).
- **Hardware-only (Pi first, then Jetson):** real DRM/KMS scan-out, GPU
  passthrough into a container, and Jetson NVIDIA-userspace injection.

## Risks & open questions

1. **Jetson GPU-userspace ABI across the container boundary (highest risk).**
   An app Wayland client needs an EGL/GLES stack matching the host NVIDIA
   driver, injected via CDI. This is the same hard problem as GPU containers
   generally; depends on `nvidia-l4t-graphics` in the image and a correct CDI
   graphics spec. Validate on hardware in phase 4.
2. **Distro-level `wayland` removal.** Forces a separate display-enabled image
   variant rather than a base-distro flag.
3. **New language/toolkit in the stack.** Rust + Slint (compositor + shell) are
   new to a primarily Go/Swift org. Accepted trade-off: no mature Swift Wayland
   GUI toolkit exists, and Slint is the fastest path to a cross-board
   interactive shell.
4. **Image size / boot time.** Compositor + shell + mesa/NVIDIA userspace add
   size and a few seconds of boot. Acceptable for a display variant; measure.
5. **Multi-output / rotation / HiDPI.** v1 is single-output, no rotation. Note
   the limitation; revisit on demand.
6. **Input device security.** libinput in the compositor sees all input; ensure
   app clients only receive input when foreground.

## Recommended phasing

1. **Agent (this repo):** `display` entitlement + `applyDisplay` (per-board
   passthrough) + arbitration + the compositor control-channel client. Lands
   no-op-safe (does nothing visible until a compositor exists), fully
   unit-tested. **This is the first implementation PR.**
2. **Compositor + shell:** develop against the nested backend on a dev machine —
   compositor renders the Slint shell and switches to a stub Wayland app on
   `present`.
3. **Pi image variant:** wire onto real hardware (mesa + vc4); validate shell +
   a real app client end-to-end.
4. **Jetson image variant:** `nvidia-l4t-graphics` + CDI graphics injection;
   validate on Orin/Thor (deepest risk).
5. **Shell breadth:** launcher/switcher/settings/enrollment-QR feature-out.
