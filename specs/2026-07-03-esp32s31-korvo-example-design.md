# ESP32-S31-Korvo-1 wendy-lite example — design

Date: 2026-07-03
Branch: `jo/esp32s31-korvo-example`
Author: Joannis (with Claude)

## Goal

Produce a **wendy-lite example project targeting the ESP32-S31-Korvo-1 devkit** that
demonstrates the board's screen and camera, modelled on the reference
[`gmondada/wlite-native-demo1`](https://github.com/gmondada/wlite-native-demo1)
(a native ESP-IDF app that consumes wendy-lite's `wendy_core` as an IDF component).

Secondary — and equally important — goal: **capture every inconvenience and gap**
hit while doing this, so the wendy team can improve ESP32 support in our tooling.
That log lives in `Examples/ESP32-S31-Korvo-1/inconveniences.md`.

## Context / what already exists

- **wendy-lite** (`wendylabsinc/wendy-lite`) is a WASM-runtime firmware for RISC-V
  ESP32s. CI target matrix: `esp32c5`, `esp32c6`, `esp32p4` (two P4 board variants),
  built with `espressif/idf:v5.5.1`. **No `esp32s31` target. No `esp32s3` target.**
- Host API subsystems exposed to WASM guests: GPIO, I2C, SPI, UART, RMT, NeoPixel,
  Timer, Storage, System, OTel, BLE, WiFi, Sockets, TLS, USB.
  **There is no camera host API and no first-class display/framebuffer host API.**
  The P4 "LCD" board variants are supported for their PSRAM + ESP-Hosted-WiFi
  config, not because wendy-lite drives a panel.
- The reference native demo's `app_main()` is literally `wendy_core_init()`; it pulls
  `wendy_core` via `idf_component.yml` git dep on branch `gab/core`.

## Target hardware — ESP32-S31-Korvo-1

Brand-new chip (announced 2026-05). RISC-V dual-core (HP RV32IMAFCP @ 320 MHz + LP
core), 512 KB SRAM, 16 MB PSRAM, WiFi 6, BT 5.4, 802.15.4. The Korvo-1 kit carries a
**4.3" 800×480 LCD** and a **3 MP OV3660 camera**. Unlike ESP32-P4 (which needs a
companion ESP32-C6 for WiFi/BT over ESP-Hosted), the S31 has **native** WiFi/BT.

**ESP32-S31 is only supported on ESP-IDF `master`** at time of writing.

## Approach (approved)

Two phases, native-first so the visual is de-risked:

### Phase 1 — native camera → LCD viewfinder (the social clip)
A plain ESP-IDF app that brings up the OV3660 camera and streams frames to the 4.3"
panel, using stock Espressif drivers. Design forks to resolve during bring-up (and
document):
- **Camera stack**: legacy DVP `espressif/esp32-camera` vs. the newer
  `esp_video` / `esp_cam_sensor` MIPI-CSI framework (S31 is P4-class, so MIPI-CSI is
  likely). Pick whichever the S31 + OV3660 combo actually supports on IDF master.
- **LCD stack**: `esp_lcd` panel — RGB parallel vs. MIPI-DSI. The 800×480 4.3" panel
  on the Korvo/LCD-EV subboard is historically RGB parallel; confirm for S31.
- **Pin maps**: need the S31-Korvo-1 schematic, which may not be public yet.
  Documented as a gap; use best-known/EV-board defaults as placeholders.

### Phase 2 — layer in `wendy_core`
Add `wendy_core` (git dep, `gab/core`) and call `wendy_core_init()` alongside the
viewfinder, proving wendy-lite coexists on S31. Expected friction: no `esp32s31`
target in wendy-lite; `idf:v5.5.1` pin vs. required `master`; WAMR on S31.

## Project layout

```
Examples/ESP32-S31-Korvo-1/
  CMakeLists.txt                 # top-level; CMAKE_POLICY_VERSION_MINIMUM 3.5 (WAMR)
  sdkconfig.defaults             # universal (WAMR, mbedTLS dyn buffers, console)
  sdkconfig.defaults.esp32s31    # target, 16MB PSRAM, native WiFi/BT, flash, partition ptr
  partitions.csv                 # nvs / factory / wasm_a / wendy_conf / storage
  main/
    CMakeLists.txt               # REQUIRES wendy_core + camera/LCD components
    idf_component.yml            # idf(master), wendy_core (git gab/core), camera + LCD deps
    demo_main.c                  # Phase 1 viewfinder; Phase 2 wendy_core_init()
  README.md
  inconveniences.md              # friction log — the deliverable for the tools team
```

## Toolchain

Clone ESP-IDF `master` + `install.sh esp32s31` into `~/esp/esp-idf-master` (leaves the
existing v5.3/v5.5.1 trees untouched).

## Scope boundary — what is and isn't verified now

No board is connected. Therefore:
- **Delivered now**: worktree + example project that **builds** for `esp32s31`
  (or, if a hard blocker is hit, builds as far as the blocker and documents it),
  plus the `inconveniences.md` friction log and an on-hardware bring-up checklist.
- **Deferred to "when hardware arrives"**: flashing, the live viewfinder, and the
  social-media clip. `README.md` carries the exact flash/monitor commands to run.

## Success criteria

1. `idf.py set-target esp32s31 && idf.py build` succeeds for the Phase-1 native app
   (or fails only at a documented, genuinely-external blocker).
2. Phase-2 `wendy_core` integration attempted; outcome (success or specific blocker)
   documented.
3. `inconveniences.md` captures every friction point with enough detail to file
   tooling issues against.
4. `README.md` gives a copy-paste path from clone → flash → viewfinder for when the
   board is plugged in.

## Outcomes (2026-07-03)

- ✅ ESP-IDF `master` (v6.2) + `esp32s31` preview toolchain installed at
  `~/esp/esp-idf-master`.
- ✅ `esp32s31` confirmed as a real (preview) IDF target; `idf.py --preview
  set-target esp32s31` configures cleanly.
- ✅ **Default build links, flashes, and drives the LCD on hardware** for `esp32s31`
  (`build/esp32s31-korvo-demo.bin`, ~209 KB): RGB LCD via the official BSP +
  `esp_lcd_panel_disp_on_off(true)`.
- 🟥 **Camera blocked upstream**: `esp32-camera` v2.1.7 compiles no driver for
  `esp32s31` (empty archive → link failure). Camera path is behind
  `DEMO_ENABLE_CAMERA` (default off); the blocker and two resolution routes are
  documented in `inconveniences.md` #8.
- ⏸️ **Phase 2 (`wendy_core`) not exercised**: gated behind the commented
  dependency + `DEMO_ENABLE_WENDY_CORE`. Blocked conceptually by items #1/#2
  (no S31 target in wendy-lite, IDF pin mismatch) — deferred.
- 📋 10 findings captured in `inconveniences.md` (4 blockers, 2 friction, 2
  papercuts, 2 positives) with a prioritized TL;DR for the tools team.
- ⏳ On-hardware flashing + the social-media clip deferred until a board is
  connected (per the approved build-only scope).
