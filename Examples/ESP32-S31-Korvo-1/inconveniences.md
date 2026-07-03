# ESP32-S31 support — inconveniences & gaps

Running log of every friction point hit while building an ESP32-S31-Korvo-1
example on top of wendy-lite. Purpose: drive concrete improvements to Wendy's
ESP32 tooling. Newest findings appended at the bottom.

Legend: 🟥 blocker · 🟧 friction · 🟨 papercut · 🟦 note/observation

## TL;DR for the tools team

| # | Sev | Gap | Where to fix |
|---|-----|-----|--------------|
| 1 | 🟥 | No `esp32s31`/`esp32s3` target in wendy-lite | wendy-lite: add target profile + partitions + CI axis |
| 2 | 🟥 | wendy-lite pins `idf:v5.5.1`; S31 needs IDF `master`/6.x | wendy-lite: master build lane for pre-release chips |
| 3 | 🟥 | No camera or display **host API** for WASM guests | wendy-lite: define camera + framebuffer host imports |
| 8 | 🟥 | `esp32-camera` resolves but builds an **empty** archive for S31 | upstream esp32-camera targets, or move to `esp_driver_cam`+`esp_cam_sensor` |
| 4 | 🟦 | `wendy` CLI firmware flow has no S31 chip key / imaging story | CLI: clear "unsupported chip" error listing manifest chips |
| 5 | 🟧 | `esp32s31` is a *preview* target — needs `--preview` on every idf.py | tooling: auto-inject `--preview` for preview chips |
| 7 | 🟧 | IDF v6.2 `esp_lcd` RGB API drift vs. all S3-era examples | docs: pin snippets to master API |
| 9 | 🟨 | Managed-component `REQUIRES` uses namespaced `espressif__esp32-camera` | docs |
| 6 | 🟨 | Shallow IDF clone breaks `idf.py --version` | docs: use tagged clone / capture commit |
| 10 | 🟦 | Positive: `esp_lcd` RGB **does** build/link for S31 | — |

**Net:** the screen half is achievable today (builds + flashes). The camera half
is blocked upstream (#8), and using wendy-lite *itself* to drive either is
blocked by the missing host APIs (#3) and the missing S31 target (#1, #2).

---

## 1. wendy-lite has no ESP32-S31 target 🟥
- **What:** wendy-lite's CI matrix and `sdkconfig.defaults.*` cover only
  `esp32c5`, `esp32c6`, `esp32p4`. There is no `esp32s31` (nor `esp32s3`)
  target profile, board cfg, or partition table.
- **Impact:** "make an S31 example based on wendy-lite" is a from-scratch board
  bring-up, not a config tweak. Everything in this folder had to be authored.
- **Fix idea:** add an `esp32s31` profile to wendy-lite (sdkconfig.defaults +
  partitions) and a CI matrix entry once the chip is in a pinned IDF release.

## 2. wendy-lite pins ESP-IDF v5.5.1; S31 needs IDF master (6.x) 🟥
- **What:** wendy-lite builds with `espressif/idf:v5.5.1`. ESP32-S31 support
  only exists on ESP-IDF `master` (installed here as **6.2**).
- **Impact:** the wendy-lite Docker build image cannot build S31 at all. Any
  S31 firmware must be built against a different, unpinned toolchain — a
  reproducibility problem.
- **Fix idea:** track an `idf:latest`/master build lane for pre-release chips,
  or gate S31 behind an IDF-version matrix axis.

## 3. No camera or display host API in wendy-lite 🟥
- **What:** wendy-lite's WASM host imports cover GPIO/I2C/SPI/UART/RMT/NeoPixel/
  Timer/Storage/System/OTel/BLE/WiFi/Sockets/TLS/USB — but **no camera and no
  framebuffer/display** surface. The P4 "LCD" board variants are supported only
  for their PSRAM + ESP-Hosted-WiFi config, not because wendy-lite drives a panel.
- **Impact:** the headline ask ("show the screen and camera work") **cannot be
  done through wendy-lite / a WASM guest today**. This demo drives the camera
  and LCD with native ESP-IDF drivers instead; wendy-lite can at best run
  *alongside* (Phase 2).
- **Fix idea:** define camera + display host imports (or a shared-framebuffer
  handoff) so imaging boards are actually usable from Wendy apps.

## 4. `wendy device` / `wendy` CLI has no MCU-imaging story 🟦
- **What:** the CLI's `firmware` command fetches prebuilt `wendy-lite-<chip>.bin`
  from a manifest keyed by chip. There is no `s31` chip key, and no path for a
  camera/LCD demo — the tooling assumes headless sensor/GPIO MCUs.
- **Fix idea:** at minimum, surface a clear "unsupported chip" error listing the
  chips that *are* in the manifest.

---

## 5. `esp32s31` is a *preview* target — plain `set-target` silently bails 🟧
- **What:** `idf.py set-target esp32s31` prints `esp32s31 is still in preview.
  You have to append '--preview'…`, runs `fullclean`, and stops — it does **not**
  configure the target. You must use `idf.py --preview set-target esp32s31`
  (and `--preview` on every subsequent `build`/`flash`).
- **Impact:** easy to think set-target "worked" (exit is quiet); every wendy
  build/flow that shells out to `idf.py` for S31 must thread `--preview` through.
- **Fix idea:** when Wendy's tooling targets a preview chip, inject `--preview`
  automatically and log that it did.

## 6. Shallow IDF clone breaks `idf.py --version` git-describe 🟨
- **What:** on a `--depth 1` master clone, `idf.py --version` emits
  `fatal: No names found, cannot describe anything / Git version unavailable,
  reading from source` before falling back to `v6.2.0`.
- **Impact:** cosmetic, but noisy in logs and defeats exact-version pinning for
  reproducible S31 builds (compounds item #2).
- **Fix idea:** document a tagged/full clone for S31, or capture the IDF commit
  hash explicitly.

## 7. IDF v6.2 `esp_lcd` RGB API drift vs. every public S3 camera example 🟧
- **What:** all the copy-paste S3-LCD-EV-Board / Korvo camera-viewfinder examples
  online use `esp_lcd_rgb_panel_config_t` fields that **no longer exist** in IDF
  v6.2: `bits_per_pixel` → `in_color_format`/`out_color_format`
  (`lcd_color_format_t`, e.g. `LCD_COLOR_FMT_RGB565`), and `psram_trans_align`
  → `dma_burst_size`. With `-Werror` the build hard-fails.
- **Impact:** because S31 forces you onto IDF master (item #2), you also inherit
  master's API churn — reference code from the S3 era won't compile unmodified.
  This is a tax specific to bringing a brand-new chip up on bleeding-edge IDF.
- **Fix idea:** when Wendy documents/scaffolds S31 imaging, pin snippets to the
  IDF-master API and note the version, rather than linking S3-era examples.

## 8. `esp32-camera` builds an EMPTY archive for esp32s31 → cryptic link errors 🟥
- **What:** `espressif/esp32-camera` v2.1.7 has no `targets:` restriction in its
  manifest, so the component manager happily *resolves* it for esp32s31 — but its
  `CMakeLists.txt` gates all driver sources behind
  `if(IDF_TARGET STREQUAL "esp32" OR "esp32s2" OR "esp32s3")`. For esp32s31 it
  compiles **nothing**, producing an empty `libespressif__esp32-camera.a`.
- **Symptom:** not a clean "unsupported target" error — instead the link fails
  late with `undefined reference to esp_camera_init / esp_camera_fb_get / …`,
  which looks like a REQUIRES/link-order bug and sends you down the wrong path.
- **Impact:** the OV3660 camera **cannot be brought up on esp32s31 with the
  released esp32-camera**. This is the single biggest blocker to the "camera
  works" half of the demo. Resolution paths, both real work:
    1. Patch/fork esp32-camera to add `esp32s31` (its LCD_CAM DVP path mirrors
       esp32s3, so `target/esp32s3/ll_cam.c` may be a starting point — register
       layouts must be verified against S31 silicon).
    2. Rewrite onto IDF's built-in `esp_driver_cam` (DVP controller) plus the
       `espressif/esp_cam_sensor` OV3660 driver — the modern stack for new chips.
- **Left in the example as:** the camera path is behind `DEMO_ENABLE_CAMERA`
  (default 0). The default build uses an animated LCD test pattern so the screen
  pipeline is provably buildable/flashable today.
- **Fix idea (tooling):** flag components that resolve-but-build-empty for a
  target; a manifest `targets:` list would turn this into an upfront rejection.

## 9. Managed-component `REQUIRES` name is the namespaced form 🟨
- **What:** to link esp32-camera you must add `espressif__esp32-camera` (double
  underscore, namespaced) to `REQUIRES`, not `esp32_camera` / `esp32-camera`.
  Non-obvious, and only surfaces as an undefined-reference link failure.
- **Fix idea:** document the exact REQUIRES token in any Wendy S31 camera guide.

## 10. Positive: `esp_lcd` RGB panel builds & links cleanly for esp32s31 🟦
- **What:** the 800x480 RGB panel via IDF built-in `esp_lcd` compiles and links
  for esp32s31 with no target gymnastics. The screen half of the demo is sound;
  only the pin map / panel timings need on-hardware confirmation.

---
<!-- build-phase findings appended below as they occur -->
