# Wendy Lite Firmware Configuration

## Flash Partition Table (`partitions.csv`)

| Partition | Type | Offset | Size | Purpose |
|-----------|------|--------|------|---------|
| `nvs` | data/nvs | 0x9000 | 24 KB | WiFi/BLE credentials, app NVS storage |
| `phy_init` | data/phy | 0xF000 | 4 KB | PHY calibration data |
| `factory` | app/factory | 0x10000 | 1.875 MB | Main firmware |
| `wasm_a` | data/0x80 | 0x1F0000 | 2 MB | WASM binary storage |
| `storage` | data/spiffs | 0x3F0000 | 64 KB | App data (SPIFFS) |

Total flash: 4MB.

## sdkconfig Feature Flags

### Core (Always Enabled)

| Config | Default | Description |
|--------|---------|-------------|
| `CONFIG_IDF_TARGET` | `esp32c6` | Target MCU |
| `CONFIG_ESPTOOLPY_FLASHSIZE_4MB` | y | 4MB flash |
| `CONFIG_WENDY_WASM_STACK_SIZE` | 8192 | WASM thread stack (bytes) |
| `CONFIG_WENDY_WASM_HEAP_SIZE` | 8192 | WASM app heap (bytes) |
| `CONFIG_WENDY_WASM_POOL_SIZE` | 229376 | WAMR memory pool (bytes, ~229KB) |

### HAL Modules

| Config | Default | Description |
|--------|---------|-------------|
| `CONFIG_WENDY_HAL_GPIO` | y | GPIO digital I/O, PWM, ADC, interrupts |
| `CONFIG_WENDY_HAL_I2C` | y | I2C bus master |
| `CONFIG_WENDY_HAL_TIMER` | y | Delays, monotonic time, async scheduling |
| `CONFIG_WENDY_HAL_NEOPIXEL` | y | WS2812 RGB LED control via RMT |

### System Components

| Config | Default | Description |
|--------|---------|-------------|
| `CONFIG_WENDY_CALLBACK` | y | Async callback dispatch (max 32 handlers) |
| `CONFIG_WENDY_SYS` | y | System functions (reboot, uptime, device ID) |
| `CONFIG_WENDY_STORAGE` | y | NVS key-value storage |
| `CONFIG_WENDY_UART` | y | UART serial communication |
| `CONFIG_WENDY_SPI` | y | SPI bus master |
| `CONFIG_WENDY_OTEL` | y | OpenTelemetry logging, metrics, traces |
| `CONFIG_WENDY_WASI_SHIM` | y | WASI compatibility layer |

### Connectivity

| Config | Default | Description |
|--------|---------|-------------|
| `CONFIG_WENDY_WIFI_ENABLED` | y | WiFi station mode + binary download |
| `CONFIG_WENDY_WIFI_SSID` | `""` | Compile-time WiFi SSID (empty = use NVS/BLE) |
| `CONFIG_WENDY_WIFI_PASSWORD` | `""` | Compile-time WiFi password |
| `CONFIG_BT_ENABLED` | y | Bluetooth stack |
| `CONFIG_BT_NIMBLE_ENABLED` | y | NimBLE BLE stack |
| `CONFIG_BT_NIMBLE_MAX_CONNECTIONS` | 2 | Max BLE connections |
| `CONFIG_WENDY_BLE_PROV` | y | BLE WiFi provisioning service |
| `CONFIG_WENDY_CLOUD_PROV` | y | Cloud device certificate provisioning |

### Optional Features (Disabled by Default)

| Config | Default | Description |
|--------|---------|-------------|
| `CONFIG_WENDY_BLE` | not set | App-facing BLE host functions |
| `CONFIG_WENDY_NET` | not set | App-facing TCP/UDP sockets, DNS, TLS |
| `CONFIG_WENDY_APP_USB` | not set | App-facing USB CDC (no OTG on C6) |
| `CONFIG_WENDY_USB_CDC_ENABLED` | not set | USB CDC transport (no native USB on C6) |

### WAMR Runtime Settings

| Config | Default | Description |
|--------|---------|-------------|
| `CONFIG_WAMR_ENABLE_LIB_PTHREAD` | not set | Disabled: causes FreeRTOS assert |
| `CONFIG_WAMR_ENABLE_SHARED_MEMORY` | not set | Disabled |
| `CONFIG_WAMR_ENABLE_LIBC_WASI` | not set | Disabled: custom shim used instead |

## Memory Optimizations

These settings are applied to maximize available RAM on the ESP32-C6:

1. **Compiler**: Size-optimized (`-Os`), silent assertions
2. **FreeRTOS**: Stack overflow canary disabled (saves RAM per task)
3. **WiFi/PHY IRAM offload**: Hot paths moved from IRAM to flash, freeing ~15-25KB DIRAM
   - `CONFIG_ESP_WIFI_IRAM_OPT=n`
   - `CONFIG_ESP_WIFI_EXTRA_IRAM_OPT=n`
   - `CONFIG_ESP_WIFI_RX_IRAM_OPT=n`
   - `CONFIG_ESP_WIFI_SLP_IRAM_OPT=n`
   - `CONFIG_ESP_PHY_IRAM_OPT=n`
4. **NimBLE buffer tuning**: Sized for max 2 connections
   - `CONFIG_BT_NIMBLE_MSYS_1_BLOCK_COUNT=12`
   - `CONFIG_BT_NIMBLE_MSYS_2_BLOCK_COUNT=12`

## Building the Firmware

```bash
# Prerequisites: ESP-IDF v5.x installed and sourced
# https://docs.espressif.com/projects/esp-idf/en/latest/esp32c6/get-started/

idf.py set-target esp32c6
idf.py build
idf.py flash monitor
```

## Wokwi Simulator

Run without hardware using Wokwi VS Code extension or CLI:
- `wokwi.toml` configures the simulation
- `diagram.json` defines the virtual circuit (ESP32-C6 + LED + resistor)

## Adding a New Component

1. Create `components/<name>/` with `CMakeLists.txt`, source, and header files
2. Register host imports in `wendy_hal_export` if WASM apps need access
3. Add `CONFIG_WENDY_<NAME>` flag in `Kconfig.projbuild`
4. Guard initialization in `wendy_main.c` with `#ifdef CONFIG_WENDY_<NAME>`
5. Add function declarations to `wasm_apps/include/wendy.h`

## USB Wire Protocol

Message format: `[MAGIC(2)][TYPE(1)][SEQ(1)][LENGTH(4)][PAYLOAD][CRC32(4)]`

| Message Type | Direction | Description |
|-------------|-----------|-------------|
| PING | CLI -> Device | Discovery, device responds with DEVICE_INFO |
| UPLOAD_START | CLI -> Device | Begin binary upload, allocates buffer |
| UPLOAD_CHUNK | CLI -> Device | Binary data chunk |
| UPLOAD_DONE | CLI -> Device | Finalize upload, validate CRC, write to flash |
| RUN | CLI -> Device | Start WASM execution |
| STOP | CLI -> Device | Stop running WASM app |
| RESET | CLI -> Device | Unload module, reinitialize runtime |
| ACK/NACK | Device -> CLI | Confirmation/error response |
