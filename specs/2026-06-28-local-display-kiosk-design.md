# Local display output for WendyOS — kiosk-browser approach

**Date:** 2026-06-28
**Status:** Draft (design)
**Branch:** `jo/local-display-kiosk-spec`

## Goal

Let a WendyOS device render a graphical UI on a monitor plugged into its
HDMI/DisplayPort output, instead of only being reachable from a browser on a
separate laptop. An app declares, in `wendy.json`, that it has a UI worth
showing; a system-level kiosk service on the device brings up a display and
points a full-screen browser at that UI.

Targets **Raspberry Pi 4/5 and NVIDIA Jetson as co-equal first-class boards**,
with per-board display-driver sections.

This design covers the **kiosk-browser path only**. Native in-container
GUI rendering (Wayland/EGL passthrough, GPU render nodes) is deliberately out
of scope and captured as a roadmap section at the end.

## Background — why nothing works today

WendyOS is headless by design, verified across all three layers:

- **OS image:** no Weston/Wayland/X11/mesa/DRM-KMS/compositor recipes. HDMI is
  enumerated *only as an ALSA audio sink* (`wendy_agent_v1_audio_service.proto`,
  e.g. the Jetson Thor "HDMI 0" device), never as a video output. The Bluetooth
  pairing agent hardcodes the assumption — *"a headless device has nothing to
  display"* (`go/internal/agent/bluetooth/pairing_agent_linux.go`).
- **Agent:** containers receive host devices through OCI entitlements
  (`go/internal/agent/oci/entitlements.go`). None of the 14 entitlements grant
  display/framebuffer/DRI access. The `gpu` entitlement adds only the NVIDIA
  *compute* nodes (`/dev/nvidia0`, `nvidiactl`, `nvidia-uvm`, `nvidia-modeset`;
  major 195) and sets `NVIDIA_DRIVER_CAPABILITIES=all`, but **never exposes
  `/dev/dri/*` render nodes or `/dev/fb0`**. Grepping the OCI/CDI code for
  `/dev/dri`, `/dev/fb`, `wayland`, `X11-unix`, `drm`, `kms` returns zero hits.
- **Apps:** every visual example (HelloVideo, HelloLLM, HelloMLX, HelloHTTP,
  FastAPI) serves a web UI on `:8080` consumed from a remote browser via
  `http://wendyos-<host>.local:<port>` or `wendy cloud tunnel`.

The key insight that makes the kiosk path cheap: **the apps already speak
HTTP.** We don't need to teach apps to render to a screen — we need the device
to render an existing local web UI. That keeps app containers unchanged and
sidesteps GPU render-node passthrough entirely.

## Approach overview

```
┌────────────────────────── WendyOS device ──────────────────────────┐
│                                                                     │
│   app container (unchanged)                                         │
│     serves HTTP on 127.0.0.1:8080  ◄──────────────┐                 │
│                                                    │ localhost       │
│   wendy.json: entitlement { "type": "display",     │ HTTP            │
│                             "port": 8080,          │                 │
│                             "route": "/kiosk" }    │                 │
│            │                                        │                 │
│            ▼ agent reads entitlement                │                 │
│   wendy-agent ──writes──► kiosk target (URL) ──────┼──► kiosk svc     │
│                                                    │                 │
│   kiosk service (systemd):                          │                 │
│     cage  (Wayland kiosk compositor)  ──drives──► DRM/KMS ──► HDMI    │
│       └─ chromium --kiosk <URL> ───────────────────┘                 │
│                                                                     │
└─────────────────────────────────────────────────────────────────────┘
```

Three independently-shippable layers:

1. **OS image** (external Yocto `meta-wendyos-*` repos): a display stack —
   DRM/KMS + board GPU driver, a minimal Wayland kiosk compositor (`cage`), and
   a kiosk browser (`chromium` in `--kiosk`). Gated behind a build feature so
   headless images stay byte-identical when the feature is off.
2. **Agent** (this repo): a new `display` entitlement and the glue that turns a
   running app's declared port/route into a "kiosk target" the kiosk service
   consumes. No OCI device passthrough involved — modeled on `mcp`, not on
   `gpu`/`camera`.
3. **App**: opt in via one `wendy.json` entitlement. No code changes if the app
   already serves a web UI.

## Layer 1 — OS image (Yocto, external repos)

> These changes live in the external `meta-wendyos-jetson` / `meta-wendyos-rpi`
> / `meta-wendyos-virtual` layers, not in this repo. Listed here for
> completeness and to bound the cross-repo work.

### Common components
- **Compositor:** `cage` — a single-app fullscreen Wayland kiosk compositor.
  No desktop, no window chrome, exactly the kiosk model. Far lighter than
  Weston+shell.
- **Browser:** `chromium` launched `--kiosk --app=<URL>` (or a WPE/cog WebKit
  build if Chromium's footprint is too large for the image budget — decision
  deferred to Layer-1 implementation).
- **Service:** a `wendyos-kiosk.service` systemd unit that starts cage + browser
  on the seat/VT that owns the display, reads its target URL from a file the
  agent writes (see Layer 2), and restarts on target change.
- **Feature gate:** a `wendyos-display` toggle so the stack is absent from
  headless builds. Note `wayland` is currently removed at the *distro* level
  (`wendyos.conf`), shared by all images — so this most likely means a separate
  display-enabled image/distro variant, not a flag on the base distro (see
  "The real blocker" below). Headless image output must be unchanged when off.

### The real blocker is a config decision, not missing hardware support

An audit of the external Yocto layers (`wendyos-builder`, `meta-wendyos-rpi`)
found the key fact for scoping: **WendyOS deliberately strips graphics from the
distro**, but the underlying kernel/driver support is present. The blocker is a
feature-flag decision, not absent capability.

- `conf/distro/wendyos.conf:99–105` does
  `DISTRO_FEATURES:remove = " x11 wayland sysvinit ptest 3g "` — Wayland and X11
  are explicitly removed distro-wide (mirrored in `meta-wendyos-rpi`'s
  `edgeos-rpi.conf:31–37`).
- `wendyos.conf:108` adds `DISTRO_FEATURES:append = " opengl"` — but the comment
  says this is *for TensorRT*, i.e. headless GPU compute, not rendering.
- No image installs a compositor, browser, `libdrm`, `libgbm`, or mesa runtime.

Because `wayland` is removed at the *distro* level (shared by all images), the
feature gate (`wendyos-display`) most likely needs to be a **separate
display-enabled image/distro variant** rather than a flag toggled on the base
distro — re-adding `wayland` distro-wide would change every image. Confirm
during Layer-1 implementation.

### Per-board driver stack (evidenced)

| | Raspberry Pi 4/5 | NVIDIA Jetson |
|---|---|---|
| KMS kernel driver | **Present & compiled in** — `CONFIG_DRM=y`, `CONFIG_DRM_VC4=y`, `CONFIG_DRM_FBDEV_EMULATION=y` (`meta-raspberrypi/.../vc4graphics.cfg`); `vc4-kms-v3d` overlay set via `VC4DTBO` in the machine confs | **Present (upstream meta-tegra)** — Tegra DRM/display controller in the BSP kernel/DT; WendyOS kernel bbappends touch only USB-gadget/crypto, not display |
| Userspace GL | **Available in-layer, not installed** — mesa built with `gallium vc4 v3d kmsro` (`mesa_%.bbappend`); just absent from `IMAGE_INSTALL` | **EGL/GLES core already shipped** — `tegra-libraries-eglcore` + `glescore` installed for the container runtime; full display stack `nvidia-l4t-graphics` (`libEGL_nvidia`, `libGLESv2_nvidia`) exists but is **commented out** in `packagegroup-nvidia-container.bb:51` |
| Missing for kiosk | `libdrm`, `libgbm`, mesa runtime, `libxkbcommon`, compositor, browser; re-enable `wayland`; set `gpu_mem≥128` for 1080p | `libdrm`, `libgbm`, `nvidia-l4t-graphics`, compositor, browser; re-enable `wayland` |
| Risk | **Low** — mainline vc4 KMS, well-trodden kiosk path | **Moderate** — needs `nvidia-l4t-graphics` enabled + verified against the cage/Wayland-EGL path; lower than first feared since EGL/GLES core already ships and DRM is in the BSP |

Net: **neither board is fundamentally blocked.** Both have the KMS driver; both
are missing the same userspace (libdrm/gbm + compositor + browser) and both have
`wayland` removed at the distro level. Jetson additionally needs the
already-packaged-but-disabled `nvidia-l4t-graphics`. The Pi path is the
lower-risk reference implementation; Jetson follows once `nvidia-l4t-graphics`
on the cage/Wayland path is validated on hardware.

## Layer 2 — Agent (this repo)

### New `display` entitlement

Add to the entitlement system, mirroring the **`mcp` entitlement** — which is a
*port declaration consumed outside the OCI device-passthrough path*, not a
device mount. The `display` entitlement is the same shape: it does **not** add
anything to the OCI spec's `Linux.Devices`/`Resources.Devices`; it records a
kiosk target.

`go/internal/shared/appconfig/appconfig.go`:
- Add `EntitlementDisplay = "display"` to the type constants and
  `ValidEntitlementTypes`.
- `allowedKeys[EntitlementDisplay] = {"type", "port", "route"}`.
- Reuse the existing `Entitlement.Port int` field (currently used by `mcp`); add
  a `Route string \`json:"route,omitempty"\`` field for the URL path.
- Validation: `port` required and in 1–65535; `route` optional, must begin with
  `/` (default `/`). At most one `display` entitlement per app.

Example `wendy.json`:
```json
{
  "entitlements": [
    { "type": "network", "mode": "host" },
    { "type": "display", "port": 8080, "route": "/kiosk" }
  ]
}
```

### Wiring the target

Like `mcp`, the `display` entitlement is **not** handled in
`oci/entitlements.go`'s device switch (cases at lines 64–96). Instead, when the
agent starts an app whose config carries a `display` entitlement, it resolves
the target URL — `http://127.0.0.1:<port><route>` — and publishes it to the
kiosk service.

- **Publish mechanism:** agent writes the URL to a well-known file (e.g.
  `/run/wendyos/kiosk-target`) that `wendyos-kiosk.service` watches (path unit
  or inotify) and reloads the browser on change. A file is the simplest
  agent↔system contract and needs no new IPC surface.
- **Single-display arbitration:** there is one screen. If multiple apps declare
  `display`, the agent must pick one deterministically (last-started wins, or
  reject the second with a clear error). **Recommend: reject** a second
  `display` app with a descriptive error so behavior is predictable; revisit if
  multi-display demand appears.
- **Lifecycle:** when the owning app stops, the agent clears the target and the
  kiosk service shows a neutral idle/splash screen rather than a dead tab.
- **Graceful degradation:** on an image built *without* the `wendyos-display`
  feature, a `display` entitlement is accepted but logs a loud warning that the
  device has no display stack and the entitlement is a no-op. (No hard failure —
  the same app image should run on headless and display-capable devices.)

### Surfacing capability
- Extend hardware discovery (`go/internal/agent/hardware/discoverer.go`) and the
  `GetAgentVersionResponse` (`wendy_agent_v1_service.proto`) with a
  `has_display` / display-capability field, so the CLI can tell a user whether
  `display` will actually do anything on the target device before they deploy.

## Layer 3 — App

For an app that already serves a web UI: add one entitlement line (above). No
code change. The app keeps binding its HTTP server; it neither knows nor cares
whether a local monitor or a remote browser is consuming it.

New `display-aware` example app (proposed): a minimal dashboard that serves a
fullscreen-friendly page and declares the `display` entitlement — the canonical
"plug in a monitor and see it" demo.

## Documentation & entitlements skill

- Document the `display` entitlement in the entitlements docs and the
  `wendy-entitlements` skill (`plugins/wendy-agentic-coding/skills/
  wendy-entitlements/SKILL.md`), including the headless-device no-op behavior.
- Note the board support matrix and the feature-gate in the device docs.

## Testing strategy

- **Agent unit tests:** `display` entitlement parsing/validation; target-URL
  resolution; multi-`display` rejection; no-op + warning on headless;
  confirmation that the OCI spec is **unchanged** by a `display` entitlement
  (no stray devices/cgroup rules — the `mcp`-parity invariant).
- **E2E (VM):** the virtual image is headless, so E2E asserts the *agent-side*
  contract — the kiosk-target file is written/cleared correctly across app
  start/stop — not actual pixels.
- **Hardware (manual, both boards):** plug in a monitor, deploy the display-aware
  example, confirm it renders on Pi (vc4) and Jetson (Tegra). This is the only
  place the OS-image stack is truly validated; cannot run in CI (GitHub runners
  have no display and no nested virt).

## Risks & open questions

1. **Distro-level `wayland` removal (top structural decision).** `wayland`/`x11`
   are removed in `wendyos.conf` for *all* images. Re-enabling it on the base
   distro changes every build; the likely answer is a dedicated
   display-enabled image/distro variant. Decide this before any Layer-1 work —
   it shapes the whole OS-image change.
2. **Jetson `nvidia-l4t-graphics` on the Wayland path (moderate).** The package
   exists but is disabled, and the EGL/GLES *core* already ships. The open
   question is no longer "does display userspace exist" (it does) but "does
   `nvidia-l4t-graphics` + cage drive HDMI via Wayland-EGL/DRM-KMS on Orin/Thor
   cleanly?" — a hardware spike, not a BSP unknown.
3. **Image size budget.** Chromium is large (~300 MB; expect +200–500 MB total).
   Strongly consider **cog/WPE WebKit** as the default kiosk browser on size
   grounds, with Chromium as an opt-in for sites needing Blink.
4. **GPU memory (Pi).** No explicit `gpu_mem` is set; firmware defaults are
   borderline for a display. Set `gpu_mem≥128` via a `config.txt` bbappend for
   1080p.
5. **Boot UX.** What shows before an app claims the display — blank, WendyOS
   splash, or a "device ready, no display app" status page? Recommend a simple
   built-in status page.
6. **Security.** The kiosk browser renders `localhost` only by default; it must
   not be a pivot to arbitrary remote URLs unless explicitly configured. Keep
   the target strictly `127.0.0.1` for v1.
7. **Cross-repo coordination.** Layers 1 and 2 live in different repos and must
   land in a compatible order (agent no-op-safe first, OS image second).

## Roadmap — native in-container GUI (out of scope here)

A later phase could let an app render *directly* to the display with GPU
acceleration, rather than through a system kiosk browser. That is a materially
bigger lift and is recorded here only so the kiosk design doesn't paint us into
a corner:

- **New device passthrough in the `display` entitlement:** expose `/dev/dri/*`
  (card + render nodes) and the appropriate `video`/`render` GID into the
  container, following the existing `applyGPU`/`applyAudio` pattern in
  `oci/entitlements.go` (cgroup `rw`, no `mknod`).
- **Wayland socket passthrough:** bind-mount `$XDG_RUNTIME_DIR/wayland-*` from a
  host compositor into the container so a native app draws into a host-managed
  surface.
- **Jetson GPU graphics:** the `gpu` entitlement already sets
  `NVIDIA_DRIVER_CAPABILITIES=all` but withholds `/dev/dri` — native GL would
  require exposing those render nodes, gated behind `display`.

The kiosk design is forward-compatible: the same `display` entitlement name
gains device-passthrough semantics later, and apps that only want a web UI keep
working unchanged.

## Recommended phasing

1. **Decide image strategy + spike:** pick the display-enabled-variant approach
   (risk 1), pick the browser (cog/WPE vs Chromium, risk 3), and validate the
   cage + Wayland-EGL/DRM-KMS path on real hardware — Pi first (low risk), then
   Jetson with `nvidia-l4t-graphics` enabled (risk 2).
2. **Agent (this repo):** `display` entitlement, target-file publish, headless
   no-op, capability field, tests. Ships safely even before any OS image
   supports it.
3. **OS image — Pi (external):** vc4 KMS + cage + browser + kiosk service behind
   `wendyos-display`. Validate on hardware.
4. **OS image — Jetson (external):** Tegra display stack, pending the spike.
5. **Example app + docs/skill updates.**
6. **(Later) Native GUI roadmap** per section above.
