# ESP32-S31-Korvo-1 — camera → LCD viewfinder (wendy-lite example)

A wendy-lite example for the **ESP32-S31-Korvo-1** devkit, modelled on the
reference [`gmondada/wlite-native-demo1`](https://github.com/gmondada/wlite-native-demo1)
(a native ESP-IDF app that consumes wendy-lite's `wendy_core` as an IDF component).

Goal: stream the on-board **OV3660 camera** to the **4.3" 800×480 LCD** — the
"screen and camera work" demo — and (Phase 2) boot the wendy-lite WASM runtime
alongside it.

> **Status:**
> - ✅ **Screen demo runs on hardware** (via the official BSP) and the default build links/flashes for `esp32s31`. The default build shows the **animated color-bar test pattern** on the LCD.
> - 🟥 **Camera path does NOT build for `esp32s31` yet** — `esp32-camera` compiles no driver for this target (see `inconveniences.md` #8). It's behind the `DEMO_ENABLE_CAMERA` flag (default off).
> - ⚠️ Flashing requires manual BOOT→RST (see `inconveniences.md` #12).

## Build modes

| Build | Result |
|-------|--------|
| `idf.py --preview build` | LCD **test pattern** (default). Links, flashes. |
| `idf.py --preview build -DDEMO_ENABLE_CAMERA=1` | OV3660 → LCD viewfinder. **Does not link** until esp32-camera gains `esp32s31` support. |
| `idf.py --preview build -DDEMO_ENABLE_WENDY_CORE=1` | Also boots `wendy_core` (Phase 2; enable dep first). |

## Why native drivers (and not a WASM guest)

wendy-lite exposes GPIO/I2C/SPI/UART/BLE/WiFi/etc. to WASM guests, but **no
camera and no display/framebuffer host API**. So the camera and LCD here are
driven by stock ESP-IDF drivers (`esp32-camera` + `esp_lcd` RGB). wendy-lite
runs *alongside* in Phase 2, it does not drive the panel. See `inconveniences.md`
item #3.

## Board / chip notes

- **ESP32-S31**: RISC-V dual-core @ 320 MHz, 16 MB PSRAM, WiFi 6 / BT 5.4,
  **native** radio (no companion C6 like the P4).
- Imaging path is the **LCD_CAM** peripheral (parallel DVP camera in + RGB LCD
  out) plus JPEG codec + PPA — an ESP32-S3-class pipeline, **not** MIPI-CSI/DSI.
- **ESP32-S31 requires ESP-IDF `master`** (installed here as 6.2). The
  `espressif/idf:v5.5.1` image wendy-lite pins cannot build it.

## Prerequisites

```bash
# ESP-IDF master with the esp32s31 toolchain:
git clone --recurse-submodules https://github.com/espressif/esp-idf.git ~/esp/esp-idf-master
cd ~/esp/esp-idf-master && ./install.sh esp32s31
```

## Build

```bash
. ~/esp/esp-idf-master/export.sh
cd Examples/ESP32-S31-Korvo-1
idf.py --preview set-target esp32s31   # NOTE: --preview is required (S31 is a preview target)
idf.py --preview build
```

## Flash & watch (once the board is connected)

```bash
idf.py --preview -p /dev/cu.usbserial-XXXX flash monitor
```
The 4.3" panel should show the animated color-bar test pattern — that already
proves the display path. Once the camera blocker (`inconveniences.md` #8) is
resolved and `DEMO_ENABLE_CAMERA=1`, the panel shows the live camera feed:
that's the clip.

## ⚠️ Pin map is unverified

The ESP32-S31-Korvo-1 schematic was not public at authoring time. The LCD and
camera GPIOs in `main/demo_main.c` are **placeholders** from the
ESP32-S3-LCD-EV-Board / Korvo-2 conventions and will almost certainly need
correcting against the real board schematic before anything shows on screen.
Every such pin is marked `TODO(schematic)`.

## Phase 2 — add the wendy-lite runtime

1. Uncomment the `wendy_core` dependency in `main/idf_component.yml`.
2. Add `wendy_core` to `REQUIRES` in `main/CMakeLists.txt`.
3. Build with `idf.py build -DDEMO_ENABLE_WENDY_CORE=1`.

`app_main()` will call `wendy_core_init()` before starting the viewfinder.
Coexistence on esp32s31 is unverified — expect this to be where the real
wendy-lite porting work surfaces (see `inconveniences.md`).

## Files

| File | Purpose |
|------|---------|
| `CMakeLists.txt` | Top-level project (WAMR CMake-policy shim + `project()`) |
| `sdkconfig.defaults` | Universal knobs (partition, console, PSRAM malloc, mbedTLS) |
| `sdkconfig.defaults.esp32s31` | S31 target: flash, PSRAM, WAMR interp |
| `partitions.csv` | 16 MB layout (nvs / factory / wasm_a / wendy_conf / storage) |
| `main/demo_main.c` | Camera→LCD viewfinder; optional `wendy_core_init()` |
| `main/idf_component.yml` | IDF master + esp32-camera (+ optional wendy_core) |
| `inconveniences.md` | Friction log for improving Wendy's ESP32 tooling |
