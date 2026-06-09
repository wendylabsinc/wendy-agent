# Wendy Lite

Wendy Lite is the WebAssembly runtime layer for ESP32 microcontrollers. It runs WASM guest applications on device and exposes the hardware via host-imported functions from the `"wendy"` module.

## Role in the Wendy Platform

The broader Wendy platform targets Linux/macOS edge devices (Raspberry Pi, Jetson, Mac) via WendyOS and wendy-agent. Wendy Lite covers the other end: bare-metal MCUs where containers and a full OS are not viable. The same hardware APIs (BLE, WiFi, sockets, TLS, OTel) are available from WASM guests as are exposed by wendy-agent to containerised apps, making the programming model portable across device classes.

## Supported Targets

| Target | Status |
|--------|--------|
| ESP32-C6 | CI-built, nightly releases |
| ESP32-C5 | CI-built, nightly releases |

Firmware is built with [ESP-IDF v5.5.1](https://docs.espressif.com/projects/esp-idf/en/v5.5.1/esp32c6/index.html) via the official Espressif Docker image.

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

Every push to `main` and every pull request against `main` runs a matrix build for `esp32c5` and `esp32c6` using `espressif/idf:v5.5.1`. On merge to `main`, a `nightly` pre-release is created (or replaced) on GitHub containing both merged firmware `.bin` files. On a `v*` tag push, the firmware is attached to the corresponding GitHub release.

## Guest Languages

| Language | Entry point | Library |
|----------|-------------|---------|
| Swift | `@main` on `WendyLiteApp` | SwiftPM: `WendyLite` (this repo) |
| Rust | `#[no_mangle] pub extern "C" fn _start()` | Cargo: `wendy-lite` (this repo) |
| C / C++ | `void _start(void)` | Include `Sources/CWendyLite/include/wendy.h` |
| AssemblyScript | `export function _start()` | `@external("wendy", ...)` declarations |
| WAT | `(export "_start" ...)` | Direct import from `"wendy"` module |

See [`host-api.md`](host-api.md) for the full function reference, [`swift-sdk.md`](swift-sdk.md) for Swift-specific internals, and [`deploying.md`](deploying.md) for the full build-to-device flow including OTA updates.

## Deploying a WASM App to Device

1. Build your app to `.wasm` (see the README in the repository root for per-language build commands).
2. Convert the binary to a C header array: `./wasm_apps/wasm2header.sh my_app.wasm main/demo_wasm.h`
3. Rebuild the firmware: `idf.py build`
4. Flash: `idf.py flash`
