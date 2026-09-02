# Deploying Apps on ESP32 with Wendy

Regular native ESP-IDF projects are the recommended way to build ESP32 apps with Wendy. Wendy also supports Swift and other WASM guests when a portable sandboxed runtime is useful.

## Native ESP-IDF Apps (Recommended)

Use the standard ESP-IDF project structure, components, configuration, and APIs. A Wendy project needs only a `wendy.json` manifest alongside the files ESP-IDF already expects:

```text
my-app/
├── CMakeLists.txt
├── sdkconfig.defaults
├── wendy.json
└── main/
    ├── CMakeLists.txt
    └── main.c
```

```json
{
  "$schema": "https://wendy.dev/schemas/wendy.json",
  "appId": "com.example.my-esp32-app",
  "version": "0.1.0",
  "platform": "wendy-lite",
  "entitlements": []
}
```

The selected ESP32 must run a Wendy firmware variant with native app support. Install one with `wendy install`; for generic C5, C6, and S3 boards, choose the board variant labeled **native app support**.

### Deploy with `wendy run`

From the ESP-IDF project directory:

```bash
wendy run --device <name>
```

The CLI performs the native deployment flow:

1. Detects the project from its standard ESP-IDF markers.
2. Reads the chip target from the connected device.
3. Ensures ESP-IDF 5.5.4 is available through `eim`.
4. Runs `idf.py set-target` if the project is configured for another chip.
5. Runs `idf.py build` and finds the application binary named by `project(...)`.
6. Uploads the native binary, reboots the device, reconnects, and streams console output.

Install the ESP-IDF Installation Manager before the first build. On macOS:

```bash
brew install espressif/eim/eim
```

If ESP-IDF 5.5.4 is missing, `wendy run` installs that version through `eim` automatically.

### Use ESP-IDF Directly

The project remains a normal ESP-IDF project. The conventional workflow continues to work:

```bash
idf.py set-target esp32c6
idf.py menuconfig
idf.py build
idf.py -p /dev/cu.usbmodemXXXX flash monitor
```

Use ESP-IDF components, managed components, Kconfig options, and peripheral drivers without a Wendy-specific wrapper. This is especially important for displays, cameras, audio, and other hardware that needs full access to the native ESP-IDF APIs.

## Updating a Device

`wendy run` can deploy over the native USB connection or over the network after Wi-Fi provisioning. A native app is device firmware, so the ESP32 reboots after every deployment and Wendy reconnects before attaching to its console.

You can always fall back to `idf.py flash` over USB when you want to manage the flash operation directly.

## WASM Apps (Optional)

Wendy Lite firmware variants with WASM support can run Swift, Rust, C/C++, AssemblyScript, or WAT guests. This path trades direct access to all of ESP-IDF for a smaller portable application boundary and Wendy's host-imported hardware APIs.

For Swift projects, `wendy run` builds the package for `wasm32-unknown-wasip1`, uploads the `.wasm` application, and attaches to its console. The lower-level manual flow is:

1. Build the application as `.wasm`.
2. Convert it to a C header with `wasm2header.sh` when embedding it in a firmware build.
3. Rebuild and flash the Wendy Lite firmware.

See the [host API](host-api.md), [Swift SDK](swift-sdk.md), and [StdIO](stdio.md) references for WASM-specific development details.

## Provisioning and Discovery

ESP32 Wendy firmware supports native USB discovery and BLE-assisted Wi-Fi provisioning. Run:

```bash
wendy device setup
wendy discover
```

Once the device joins Wi-Fi, it advertises over mDNS and accepts Wendy connections over the LAN. Cloud-enrolled devices can use the same remote selection workflow as other Wendy targets.
