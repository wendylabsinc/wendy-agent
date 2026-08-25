# ESP32-C6 NeoPixel — Swift WASM

A minimal [Wendy Lite](https://github.com/wendylabsinc/wendy-lite) sample that
runs Swift as WebAssembly on an ESP32-C6 and cycles a WS2812/NeoPixel through
red, green, blue, and off.

The defaults match the onboard addressable LED on common ESP32-C6-DevKitC-1
boards:

- Data pin: GPIO 8
- Pixel count: 1
- Brightness: 32/255 per active color channel
- Step time: 750 ms

## Prerequisites

Install the Wendy CLI:

```sh
curl -fsSL https://install.wendy.dev/cli.sh | bash
```

Install Swift 6.3.1 and the Embedded WebAssembly SDK:

```sh
swiftly install 6.3.1
swiftly use 6.3.1
swift sdk install \
  https://download.swift.org/swift-6.3.1-release/wasm-sdk/swift-6.3.1-RELEASE/swift-6.3.1-RELEASE_wasm.artifactbundle.tar.gz \
  --checksum bd47baa20771f366d8beed7970afaa30742b2210097afd15f85427226d8f4cf2
```

The ESP32-C6 must be running Wendy Lite firmware with WASM app support.

## Build

```sh
swiftly run +6.3.1 swift build \
  --swift-sdk swift-6.3.1-RELEASE_wasm-embedded \
  --triple wasm32-unknown-wasip1 \
  -c release
```

## Deploy

Connect the ESP32-C6 over USB or ensure it is discoverable on the network, then
run:

```sh
wendy run --device <name>
```

The Wendy CLI detects `Package.swift`, compiles the Swift executable to WASM,
uploads it to the Wendy Lite runtime, and starts it.

## Customize

Change `neoPixelPin`, `pixelCount`, `brightness`, or `stepDelay` near the top of
`Sources/ESP32C6NeoPixelWasm/AppMain.swift`. For an external strip, connect its
data input to the selected GPIO and share ground with the ESP32-C6. Use a
suitable external power supply for more than a few pixels.
