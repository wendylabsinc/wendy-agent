---
name: wendy-lite
description: 'Expert guidance on building WASM apps for Wendy Lite MCU firmware on ESP32-C6. Use when developers mention: (1) Wendy Lite or wendy-lite, (2) WASM apps on ESP32 or microcontrollers, (3) WendyLite Swift package or import WendyLite, (4) building C/Rust/Swift/Zig apps for ESP32, (5) WAMR runtime on embedded devices, (6) GPIO/I2C/SPI/UART/NeoPixel from WASM, (7) Embedded Swift on WASM or wasm32-none-none-wasm, (8) BLE provisioning on ESP32-C6, (9) uploading WASM binaries to MCU, (10) TLS/networking on ESP32 from Swift.'
references:
  - wasm-api.md
  - firmware-config.md
  - swift-sdk.md
---

# Wendy Lite

Wendy Lite is a WebAssembly (WASM) runtime firmware for ESP32-C6 microcontrollers. It enables developers to write apps in C, Rust, Swift, Zig, TypeScript, or WAT, compile them to WASM, and run them on the device with full hardware access.

## Key Concepts

- **WAMR Runtime**: Uses WebAssembly Micro Runtime to execute WASM binaries
- **Host Imports**: WASM apps import functions from the `"wendy"` module to access hardware
- **Callback System**: Async events (GPIO interrupts, timers, BLE) dispatched via exported `wendy_handle_callback`
- **Flash Persistence**: WASM binaries stored in flash partition `wasm_a` (2MB), auto-loaded on boot
- **Multi-language**: Apps can be written in C, Rust, Swift 6.0+, Zig, TypeScript, or raw WAT

## Project Structure

```
wendy-lite/
├── main/wendy_main.c              # Firmware entry point, WASM lifecycle manager
├── components/
│   ├── wendy_wasm/                # WAMR integration (load, run, stop modules)
│   ├── wendy_hal/                 # Hardware: GPIO, I2C, RMT, NeoPixel, Timer
│   ├── wendy_hal_export/          # Registers HAL as WASM host imports
│   ├── wendy_usb/                 # USB CDC wire protocol for app upload
│   ├── wendy_wifi/                # WiFi station + HTTP binary download
│   ├── wendy_ble_prov/            # BLE WiFi provisioning ("Wendy-XXXX")
│   ├── wendy_cloud_prov/          # Cloud device certificate provisioning
│   ├── wendy_callback/            # Async ISR-safe event dispatch (max 32 handlers)
│   ├── wendy_storage/             # NVS key-value storage for apps
│   ├── wendy_uart/                # UART serial communication
│   ├── wendy_spi/                 # SPI bus master
│   ├── wendy_sys/                 # System: reboot, uptime, device ID
│   ├── wendy_otel/                # OpenTelemetry: logs, metrics, traces
│   ├── wendy_ble/                 # BLE peripheral/central (optional)
│   ├── wendy_net/                 # TCP/UDP sockets, DNS, TLS (optional)
│   ├── wendy_wasi_shim/           # WASI compatibility layer
│   ├── wendy_safety/              # Memory safety
│   └── wendy_app_usb/             # USB from WASM app perspective (optional)
├── wasm_apps/                     # Example WASM applications
│   ├── include/wendy.h            # App API header (all host imports)
│   ├── Makefile                   # Build system for all app languages
│   ├── blink/                     # C: GPIO LED blink
│   ├── i2c_sensor/                # C: BMP280 I2C sensor reader
│   ├── swift_display/             # Swift 6.0+ embedded WASM app
│   ├── rust_blink/                # Rust WASM app
│   ├── zig_blink/                 # Zig WASM app
│   └── wat_blink/                 # Raw WebAssembly Text
├── partitions.csv                 # Flash layout (nvs, factory, wasm_a, storage)
├── sdkconfig.defaults             # ESP-IDF + WAMR + feature flags
└── diagram.json                   # Wokwi simulator circuit
```

## Writing WASM Apps

### C (Recommended for Getting Started)

```c
#include "wendy.h"

void _start(void) {
    gpio_configure(2, WENDY_GPIO_OUTPUT, WENDY_GPIO_PULL_NONE);
    for (int i = 0; i < 10; i++) {
        gpio_write(2, 1);
        timer_delay_ms(500);
        gpio_write(2, 0);
        timer_delay_ms(500);
    }
}
```

Build:
```bash
cd wasm_apps
make blink
```

Or manually:
```bash
clang --target=wasm32 -O2 -nostdlib -I include \
  -Wl,--no-entry -Wl,--export=_start -Wl,--allow-undefined \
  -o app.wasm app.c
```

### Swift (Recommended for App Development)

Swift apps use the **WendyLite** Swift package from https://github.com/wendylabsinc/wendy-lite, which provides type-safe wrappers around all WASM host imports.

**See `references/swift-sdk.md` for the complete Swift API reference, Package.swift setup, and examples.**

Key points:
- Requires Swift 6.0+ with Embedded Swift support
- Uses `@_cdecl("_start")` for the WASM entry point
- No heap allocation — use stack-allocated tuple buffers
- `StaticString` only — no runtime string construction
- Cast `UnsafePointer<UInt8>` to `UnsafePointer<CChar>` via `UnsafeRawPointer` for API calls
- Build and deploy with `wendy run`

Quick start:
```swift
import WendyLite

@_cdecl("_start")
func start() {
    GPIO.configure(pin: 2, mode: .output)
    GPIO.write(pin: 2, level: 1)
    System.sleepMs(1000)
    GPIO.write(pin: 2, level: 0)
}
```

### Other Languages

| Language | Build Command | Requirements |
|----------|---------------|--------------|
| Rust | `make rust_blink` | rustup (not Homebrew Rust) |
| Zig | `make zig_blink` | Zig 0.13+ |
| WAT | `make wat_blink` | WABT (wat2wasm) |

### App Requirements

1. Export a `_start` function as the entry point
2. Import host functions from the `"wendy"` module
3. For async callbacks, export `wendy_handle_callback(int handler_id, int arg0, int arg1, int arg2)`
4. Call `sys_yield()` to allow callback dispatch

## Hardware Access Summary

| Subsystem | Key Functions | Notes |
|-----------|---------------|-------|
| GPIO | `gpio_configure`, `gpio_read`, `gpio_write`, `gpio_set_pwm`, `gpio_analog_read`, `gpio_set_interrupt` | Supports input, output, PWM, ADC, interrupts |
| I2C | `i2c_init`, `i2c_scan`, `i2c_write`, `i2c_read`, `i2c_write_read` | Bus 0 pre-initialized by firmware |
| NeoPixel | `neopixel_init`, `neopixel_set`, `neopixel_clear` | WS2812 RGB LEDs via RMT |
| Timer | `timer_delay_ms`, `timer_millis`, `timer_set_timeout`, `timer_set_interval` | Blocking delay + async callbacks |
| UART | `uart_open`, `uart_write`, `uart_read`, `uart_set_on_receive` | Serial ports with callbacks |
| SPI | `spi_open`, `spi_transfer`, `spi_close` | Full-duplex master |
| Storage | `storage_get`, `storage_set`, `storage_delete`, `storage_exists` | Persistent NVS key-value |
| System | `sys_reboot`, `sys_uptime_ms`, `sys_device_id`, `sys_yield` | Device info and control |
| OTel | `otel_log`, `otel_metric_*`, `otel_span_*` | Structured logging, metrics, tracing |
| BLE | `ble_init`, `ble_advertise_start`, `ble_gatts_*`, `ble_gattc_*` | Optional, disabled by default |
| WiFi | `wifi_connect`, `wifi_status`, `wifi_get_ip`, `wifi_ap_start` | App-facing WiFi control |
| Sockets | `net_socket`, `net_connect`, `net_send`, `net_recv` | TCP/UDP, optional |
| TLS | `tls_connect`, `tls_send`, `tls_recv` | Secure connections |
| Console | `wendy_print` | Output to UART + USB |

## Boot & Lifecycle

1. Firmware initializes NVS, WAMR pool, HAL, USB, BLE provisioning, WiFi
2. WASM manager thread starts, registers all HAL exports
3. Auto-loads WASM from flash partition `wasm_a` (if present)
4. Runs WASM `_start()` in dedicated thread (8KB stack)
5. Event loop handles: upload, run, stop, reset requests

## Uploading WASM Binaries

- **WiFi**: HTTP download via UDP listener after WiFi provisioning
- **USB CDC**: Wire protocol with PING, UPLOAD (START/CHUNK/DONE), RUN, STOP, RESET messages
- **Flash**: Written to `wasm_a` partition with 4-byte size header, persists across reboots

## BLE Provisioning

Device advertises as "Wendy-XXXX" over BLE. A client can:
1. Discover the device via BLE scan
2. Send WiFi SSID/password via GATT characteristics
3. Device connects to WiFi and starts mDNS + UDP listener

## Building the Firmware

```bash
# Requires ESP-IDF (v5.x) with ESP32-C6 support
idf.py set-target esp32c6
idf.py build
idf.py flash monitor
```

## Wokwi Simulator

The project includes `wokwi.toml` and `diagram.json` for hardware simulation without a physical board.

## Memory Constraints

- WAMR pool: 229KB (pre-allocated before WiFi/BLE)
- WASM stack: 8KB, heap: 8KB
- Flash: 4MB total (1.875MB firmware, 2MB WASM, 64KB app storage)
- WiFi/PHY IRAM offloaded to flash to free ~15-25KB DIRAM

## Reference Files

Load these files as needed for specific topics:

- **`references/swift-sdk.md`** - WendyLite Swift package: Package.swift setup, complete Swift API reference, Embedded Swift constraints, stack buffer patterns, code examples
- **`references/wasm-api.md`** - Complete WASM app API: all host imports, constants, callback system, per-subsystem function signatures and usage (C-level)
- **`references/firmware-config.md`** - ESP-IDF sdkconfig options, partition table, feature flags, memory tuning, component enable/disable
