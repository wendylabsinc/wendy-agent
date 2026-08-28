# Wendy Lite

Wendy Lite is Wendy's runtime and deployment layer for ESP32 microcontrollers. It supports native ESP-IDF applications as the recommended project model and can also run portable WASM guest applications.

## Role in the Wendy Platform

The broader Wendy platform targets Linux/macOS edge devices (Raspberry Pi, Jetson, Mac) via WendyOS and wendy-agent. Wendy Lite covers bare-metal MCUs where containers and a full OS are not viable. Native apps use the complete ESP-IDF API and normal ESP-IDF project layout; `wendy run` detects, builds, and deploys them. WASM remains available when a smaller portable application boundary is preferable.

> **Recommendation:** Start new applications as regular native ESP-IDF projects. Use the optional WASM runtime when portability or sandboxing matters more than full ESP-IDF access. Camera and display/framebuffer peripherals are not exposed to WASM guests and should be driven by native ESP-IDF drivers.

## Supported Targets

| Target | Status |
|--------|--------|
| ESP32-C5 | CI-built, nightly releases |
| ESP32-C6 | CI-built, nightly releases |
| ESP32-C61 | CI-built, nightly releases |
| ESP32-P4 | Supported boards are listed by `wendy install` |
| ESP32-S3 | CI-built, nightly releases |

Boards without a published Wendy firmware variant are not shown by `wendy install`. Native app capability is firmware-specific; where the installer offers multiple choices, select a board variant labeled **native app support**.

Native applications are built with ESP-IDF 5.5.4 through the ESP-IDF Installation Manager (`eim`).

## Repository Layout

```
wendy-lite/
  Sources/
    CWendyLite/
      include/wendy.h        Host-function declarations (WASM import attributes)
      shim.c                 Thin C shim for Swift interop
    WendyLite/               Swift SDK (SwiftPM library target)
      WendyLite.swift        Re-exports CWendyLite
      WendyLiteApp.swift     @main protocol + async runtime bootstrap
      WendyClock.swift       Embedded-Swift async clock + TimerHub
      CallbackDispatch.swift Handler registry + wendy_handle_callback export
      GPIO.swift             GPIO types and wrapper enum
      I2C.swift              I2C wrapper
      SPI.swift              SPI wrapper
      UART.swift             UART wrapper
      RMT.swift              RMT (remote control) wrapper
      NeoPixel.swift         NeoPixel/WS2812 wrapper
      Timer.swift            Low-level timer wrapper
      Network.swift          WiFi, Net (sockets), DNS, TLS wrappers
      BLE.swift              BLE, GATTS, GATTC wrappers
      OTel.swift             OpenTelemetry log/metrics/tracing wrapper
      Storage.swift          NVS key-value wrapper
      System.swift           System + Console wrappers
      USB.swift              USB CDC + HID wrapper
  src/lib.rs                 Rust crate (no_std FFI + safe wrappers)
  CMakeLists.txt             ESP-IDF component CMake (firmware build)
  Package.swift              SwiftPM package (Swift SDK)
  Cargo.toml                 Cargo package (Rust crate)
  .github/workflows/
    build.yml                Matrix firmware build + nightly/release publishing
```

## CI and Releases

Wendy publishes installable firmware variants through its stable and nightly release channels. `wendy install` reads the current firmware catalog and only presents board variants that have a published binary for the selected channel.

## Native ESP-IDF Apps

Native apps are regular ESP-IDF projects. Add `wendy.json` with `"platform": "wendy-lite"`, then deploy with:

```bash
wendy run --device <name>
```

Wendy selects the connected device's target, runs the standard ESP-IDF build, uploads the application firmware, reboots, and reconnects to its console. See [`deploying.md`](deploying.md) for the complete workflow.

## Optional WASM Guest Languages

| Language | Entry point | Library |
|----------|-------------|---------|
| Swift | `@main` on `WendyLiteApp` | SwiftPM: `WendyLite` (this repo) |
| Rust | `#[no_mangle] pub extern "C" fn _start()` | Cargo: `wendy-lite` (this repo) |
| C / C++ | `void _start(void)` | Include `Sources/CWendyLite/include/wendy.h` |
| AssemblyScript | `export function _start()` | `@external("wendy", ...)` declarations |
| WAT | `(export "_start" ...)` | Direct import from `"wendy"` module |

See [`host-api.md`](host-api.md) for the full function reference, [`swift-sdk.md`](swift-sdk.md) for Swift-specific internals, [`deploying.md`](deploying.md) for the full build-to-device flow including OTA updates, and [`wendy-com.md`](wendy-com.md) for the WendyCom protocol reference.

## Manually Embedding a WASM App

For low-level firmware development, a WASM guest can still be embedded directly:

1. Build your app to `.wasm`.
2. Convert the binary to a C header array: `./wasm_apps/wasm2header.sh my_app.wasm main/demo_wasm.h`
3. Rebuild the firmware: `idf.py build`
4. Flash: `idf.py flash`
