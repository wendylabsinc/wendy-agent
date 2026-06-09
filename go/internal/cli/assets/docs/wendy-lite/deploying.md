# Deploying and Updating WASM Apps on Wendy Lite

Wendy Lite does not have a network-accessible container registry or an OTA pull mechanism. App deployment is a firmware rebuild: the WASM binary is baked into the firmware image as a C array and flashed to the device. This page covers the full flow from build to running code, and the CLI commands involved.

## Deployment Flow

```
[App source code]
      │
      ▼  (swift build / cargo build / clang)
 app.wasm
      │
      ▼  wasm2header.sh
 main/demo_wasm.h   (C byte-array header)
      │
      ▼  idf.py build
 wendy_mcu_<target>.bin   (merged firmware)
      │
      ▼  idf.py flash  (USB)  — or —  wendy device run  (OTA via WiFi)
 Device running new app
```

## Step 1: Build the WASM binary

Pick your language and build to `.wasm`:

**Swift:**
```bash
swiftly run +6.3.1 swift build \
    --swift-sdk swift-6.3.1-RELEASE_wasm-embedded \
    --triple wasm32-unknown-wasip1 \
    -c release
# Output: .build/wasm32-unknown-wasip1/release/MyApp.wasm
```

**Rust:**
```bash
cargo build --release
# Output: target/wasm32-unknown-unknown/release/my_app.wasm
```

**C:**
```bash
clang --target=wasm32 -O2 -nostdlib \
    -I Sources/CWendyLite/include \
    -Wl,--no-entry -Wl,--export=_start -Wl,--allow-undefined \
    -o app.wasm app.c
```

## Step 2: Convert the binary to a C header

The `wasm2header.sh` script in the repository converts the `.wasm` file to a C header array that the firmware build embeds:

```bash
./wasm_apps/wasm2header.sh my_app.wasm main/demo_wasm.h
```

For a named build target (with a `build.sh` wrapper):
```bash
./wasm_apps/build.sh swift_blink
```

## Step 3: Rebuild and flash the firmware

```bash
idf.py set-target esp32c6   # first time only
idf.py build
idf.py flash
```

The `idf.py build` step picks up the updated `main/demo_wasm.h` and links the WASM binary into the firmware. `idf.py flash` writes the merged binary over USB.

---

## Updating a running device

### USB flash (always available)

The simplest update path is to re-run `idf.py flash` with the device connected via USB. No WiFi required; no agent required.

### CLI via WiFi (when WiFi is provisioned)

Once a Wendy Lite device is connected to WiFi, the `wendy` CLI can interact with it over LAN using the standard gRPC transport. This works the same as for WendyOS devices — see [connectivity](../wendy-agent/connectivity/ble.md) and [device selection](../clients/wendy-cli/device-selection.md) for the full picture.

```bash
# Discover nearby devices (mDNS + BLE)
wendy discover

# Push a new firmware and flash remotely
wendy device update --binary ./build/wendy_mcu_esp32c6.bin
```

The `wendy device update` command streams the binary to the running agent over gRPC and triggers the replacement. For Wendy Lite devices, this flashes the merged firmware image rather than replacing an agent binary.

### OTA via `wendy run`

`wendy run` in a Wendy Lite app project directory performs the full build-and-deploy cycle:

1. Queries the device for its target (ESP32-C5 or C6) and architecture.
2. Builds the WASM binary for that target.
3. Converts it to a C header and rebuilds the firmware.
4. Uploads and flashes the firmware to the device.
5. Streams logs from the device.

---

## GitHub CI: nightly and release binaries

Every push to `main` triggers a matrix build for `esp32c5` and `esp32c6` in CI (see [`build.yml`](../../wendy-lite/.github/workflows/build.yml)). Builds produce a merged binary (`wendy_mcu_<target>.bin`) that can be flashed directly.

**Nightly builds** are published as a pre-release GitHub release (`nightly`) containing both targets. The tag is recreated on every `main` push.

**Tagged releases** (e.g. `v1.2.0`) attach both binaries to a stable GitHub release.

The CLI's `wendy device update --nightly` flag targets nightly pre-releases; omitting it targets the latest stable release.

---

## App lifecycle in firmware

The firmware loads the embedded WASM binary from the C array, instantiates it with WAMR, and calls the WASM entry point (`_start` or, for Swift apps, the `WendyLiteApp.main()` entrypoint synthesised by the SDK). There is no install/uninstall step — swapping the app is always a full firmware rebuild.

Multiple WASM apps in a single firmware build are not currently supported. One binary per flash.

---

## Provisioning JSON (current and planned)

**Today (shipped):** Wendy Lite provisioning is done by manually editing a JSON file into the C source tree before flashing. The fields cover device identity, the cloud endpoint, and the certificate material. There is no on-device key generation; the certificate is baked into the firmware image.

**Planned (not yet shipped, [WDY-1245](https://linear.app/wendylabsinc/issue/WDY-1245)):** an automated provisioning system that:

- Generates a private key and a certificate signing request **on the device itself** rather than burning a pre-generated certificate into the firmware.
- Adds an explicit `protocol` property to the provisioning JSON (e.g. `grpc`, `mtls`) so the transport choice is recorded rather than inferred from port numbers. WendyOS today supports port overriding, and the standard default ports are widely understood, so this field is informational rather than required for connection routing — until the unified TCP socket protocol ([WDY-1246](https://linear.app/wendylabsinc/issue/WDY-1246)) lands, at which point the field becomes the canonical transport selector.
- Uses mDNS for device discovery, consistent with the rest of the WendyOS networking stack.

Until those changes ship, the manual JSON-edit + flash workflow above remains the only supported provisioning path.
