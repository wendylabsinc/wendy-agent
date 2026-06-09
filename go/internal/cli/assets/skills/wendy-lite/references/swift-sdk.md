# WendyLite Swift SDK Reference

The `WendyLite` Swift package provides type-safe wrappers around the Wendy WASM host imports. It targets Embedded Swift (WASM) for ESP32-C6 microcontrollers.

**Repository:** https://github.com/wendylabsinc/wendy-lite

## Adding the Dependency

```swift
// Package.swift
// swift-tools-version:6.0
import PackageDescription

let package = Package(
    name: "MyApp",
    dependencies: [
        .package(url: "https://github.com/wendylabsinc/wendy-lite", branch: "main"),
    ],
    targets: [
        .executableTarget(
            name: "MyApp",
            dependencies: [
                .product(name: "WendyLite", package: "wendy-lite"),
            ],
            swiftSettings: [
                .enableExperimentalFeature("Embedded"),
                .unsafeFlags(["-wmo"]),
            ],
            linkerSettings: [
                .unsafeFlags([
                    "-Xclang-linker", "-nostdlib",
                    "-Xlinker", "--no-entry",
                    "-Xlinker", "--export=_start",
                    "-Xlinker", "--allow-undefined",
                    "-Xlinker", "--initial-memory=65536",
                    "-Xlinker", "-z", "-Xlinker", "stack-size=8192",
                ])
            ]
        ),
    ]
)
```

## Architecture

- `CWendyLite` — C target containing `wendy.h` with all WASM host import declarations
- `WendyLite` — Swift target with `@_exported import CWendyLite`, re-exporting all C symbols
- All Swift wrappers are `public enum` types with `@inline(__always)` static methods
- Parameters use `UnsafePointer<CChar>` / `UnsafeMutablePointer<CChar>` with explicit lengths
- Functions that can fail return `Int32` (0 = success, negative = error)

## Embedded Swift Constraints

Since this runs on WASM with Embedded Swift, these restrictions apply:

- **No heap allocation** — Use stack-allocated tuple buffers instead of `Array`, `String`, or `Data`
- **`StaticString` only** — No runtime string construction; use string literals
- **No stdlib collections** — No `Array`, `Dictionary`, `Set`
- **No `print()`** — Use `Console.print()` with raw pointers
- **`@_cdecl("_start")`** — Required entry point export for the WASM runtime
- **Manual pointer casting** — `StaticString.withUTF8Buffer` gives `UnsafePointer<UInt8>`, but APIs expect `UnsafePointer<CChar>` (Int8). Cast via `UnsafeRawPointer`:
  ```swift
  message.withUTF8Buffer { buf in
      let ptr = UnsafeRawPointer(buf.baseAddress!).assumingMemoryBound(to: CChar.self)
      Console.print(ptr, length: Int32(buf.count))
  }
  ```

## Stack Buffer Pattern

Since heap allocation is unavailable, use tuple types for fixed-size buffers:

```swift
// 128-byte stack buffer
var buf: (
    UInt8, UInt8, UInt8, UInt8, UInt8, UInt8, UInt8, UInt8,
    UInt8, UInt8, UInt8, UInt8, UInt8, UInt8, UInt8, UInt8,
    // ... repeat to desired size ...
) = (0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, ...)

withUnsafeMutablePointer(to: &buf) { tuplePtr in
    let ptr = UnsafeMutableRawPointer(tuplePtr)
        .assumingMemoryBound(to: UInt8.self)
    // Use ptr[0], ptr[1], etc.
}
```

## API Reference

### Console

```swift
public enum Console {
    /// Print a buffer to the Wendy console (UART + USB).
    @discardableResult
    static func print(_ buffer: UnsafePointer<CChar>, length: Int32) -> Int32
}
```

### System

```swift
public enum System {
    static func uptimeMs() -> Int64
    static func reboot() -> Never
    static func firmwareVersion(buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    static func deviceId(buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    static func sleepMs(_ ms: Int32)
    static func yield()  // Dispatches pending callbacks
}
```

### Timer

```swift
public enum Timer {
    static func delayMs(_ ms: Int32)           // Blocking delay
    static func millis() -> Int64              // Monotonic milliseconds
    static func setTimeout(ms: Int32, handlerId: Int32) -> Int32   // One-shot callback
    static func setInterval(ms: Int32, handlerId: Int32) -> Int32  // Repeating callback
    @discardableResult
    static func cancel(timerId: Int32) -> Int32
}
```

### GPIO

```swift
public enum GPIOMode: Int32 { case input = 0; case output = 1; case inputOutput = 2 }
public enum GPIOPull: Int32 { case none = 0; case up = 1; case down = 2 }
public enum GPIOInterruptEdge: Int32 { case rising = 1; case falling = 2; case anyEdge = 3 }

public enum GPIO {
    @discardableResult
    static func configure(pin: Int32, mode: GPIOMode, pull: GPIOPull = .none) -> Int32
    static func read(pin: Int32) -> Int32
    @discardableResult
    static func write(pin: Int32, level: Int32) -> Int32
    @discardableResult
    static func setPWM(pin: Int32, frequencyHz: Int32, dutyPercent: Int32) -> Int32
    static func analogRead(pin: Int32) -> Int32
    @discardableResult
    static func setInterrupt(pin: Int32, edge: GPIOInterruptEdge, handlerId: Int32) -> Int32
    @discardableResult
    static func clearInterrupt(pin: Int32) -> Int32
}
```

### I2C

```swift
public enum I2C {
    @discardableResult
    static func initialize(bus: Int32, sda: Int32, scl: Int32, frequencyHz: Int32) -> Int32
    static func scan(bus: Int32, addresses: UnsafeMutablePointer<UInt8>, maxAddresses: Int32) -> Int32
    @discardableResult
    static func write(bus: Int32, address: Int32, data: UnsafePointer<UInt8>, length: Int32) -> Int32
    static func read(bus: Int32, address: Int32, buffer: UnsafeMutablePointer<UInt8>, length: Int32) -> Int32
    static func writeRead(bus: Int32, address: Int32, writeData: UnsafePointer<UInt8>, writeLength: Int32, readBuffer: UnsafeMutablePointer<UInt8>, readLength: Int32) -> Int32
}
```

### SPI

```swift
public enum SPI {
    static func open(host: Int32, mosi: Int32, miso: Int32, sclk: Int32, cs: Int32, clockHz: Int32) -> Int32
    @discardableResult
    static func close(deviceId: Int32) -> Int32
    @discardableResult
    static func transfer(deviceId: Int32, txBuffer: UnsafeMutablePointer<UInt8>?, rxBuffer: UnsafeMutablePointer<UInt8>?, length: Int32) -> Int32
}
```

### UART

```swift
public enum UART {
    static func open(port: Int32, txPin: Int32, rxPin: Int32, baud: Int32) -> Int32
    @discardableResult
    static func close(port: Int32) -> Int32
    @discardableResult
    static func write(port: Int32, data: UnsafePointer<CChar>, length: Int32) -> Int32
    static func read(port: Int32, buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    static func available(port: Int32) -> Int32
    @discardableResult
    static func flush(port: Int32) -> Int32
    @discardableResult
    static func setOnReceive(port: Int32, handlerId: Int32) -> Int32
}
```

### NeoPixel (WS2812)

```swift
public enum NeoPixel {
    static func initialize(pin: Int32, numLeds: Int32) -> Int32
    @discardableResult
    static func set(index: Int32, r: Int32, g: Int32, b: Int32) -> Int32
    @discardableResult
    static func clear() -> Int32
}
```

### RMT (Timing Buffer)

```swift
public enum RMT {
    static func configure(pin: Int32, resolutionHz: Int32) -> Int32
    @discardableResult
    static func transmit(channelId: Int32, buffer: UnsafePointer<UInt8>, length: Int32) -> Int32
    @discardableResult
    static func release(channelId: Int32) -> Int32
}
```

### Storage (NVS)

```swift
public enum Storage {
    static func get(key: UnsafePointer<CChar>, keyLength: Int32, value: UnsafeMutablePointer<CChar>, valueLength: Int32) -> Int32
    @discardableResult
    static func set(key: UnsafePointer<CChar>, keyLength: Int32, value: UnsafePointer<CChar>, valueLength: Int32) -> Int32
    @discardableResult
    static func delete(key: UnsafePointer<CChar>, keyLength: Int32) -> Int32
    static func exists(key: UnsafePointer<CChar>, keyLength: Int32) -> Int32
}
```

### WiFi

```swift
public enum WiFi {
    @discardableResult
    static func connect(ssid: UnsafePointer<CChar>, ssidLength: Int32, password: UnsafePointer<CChar>, passwordLength: Int32) -> Int32
    @discardableResult
    static func disconnect() -> Int32
    static func status() -> Int32
    static func getIP(buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    static func rssi() -> Int32
    @discardableResult
    static func startAP(ssid: UnsafePointer<CChar>, ssidLength: Int32, password: UnsafePointer<CChar>, passwordLength: Int32, channel: Int32) -> Int32
    @discardableResult
    static func stopAP() -> Int32
}
```

### Sockets (Net)

```swift
public enum SocketDomain: Int32 { case inet = 2 }
public enum SocketType: Int32 { case stream = 1; case dgram = 2 }

public enum Net {
    static func socket(domain: SocketDomain = .inet, type: SocketType, protocol proto: Int32 = 0) -> Int32
    @discardableResult
    static func connect(fd: Int32, ip: UnsafePointer<CChar>, ipLength: Int32, port: Int32) -> Int32
    @discardableResult
    static func bind(fd: Int32, port: Int32) -> Int32
    @discardableResult
    static func listen(fd: Int32, backlog: Int32) -> Int32
    static func accept(fd: Int32) -> Int32
    static func send(fd: Int32, data: UnsafePointer<CChar>, length: Int32) -> Int32
    static func recv(fd: Int32, buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    @discardableResult
    static func close(fd: Int32) -> Int32
}
```

### DNS

```swift
public enum DNS {
    static func resolve(hostname: UnsafePointer<CChar>, hostnameLength: Int32, resultBuffer: UnsafeMutablePointer<CChar>, resultLength: Int32) -> Int32
}
```

### TLS

```swift
public enum TLS {
    static func connect(host: UnsafePointer<CChar>, hostLength: Int32, port: Int32) -> Int32
    static func send(fd: Int32, data: UnsafePointer<CChar>, length: Int32) -> Int32
    static func recv(fd: Int32, buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    @discardableResult
    static func close(fd: Int32) -> Int32
}
```

### OpenTelemetry

```swift
public enum OTelLogLevel: Int32 { case error = 1; case warn = 2; case info = 3; case debug = 4 }

public enum OTel {
    @discardableResult
    static func log(level: OTelLogLevel, message: UnsafePointer<CChar>, messageLength: Int32) -> Int32
    @discardableResult
    static func counterAdd(name: UnsafePointer<CChar>, nameLength: Int32, value: Int64) -> Int32
    @discardableResult
    static func gaugeSet(name: UnsafePointer<CChar>, nameLength: Int32, value: Double) -> Int32
    @discardableResult
    static func histogramRecord(name: UnsafePointer<CChar>, nameLength: Int32, value: Double) -> Int32
    static func spanStart(name: UnsafePointer<CChar>, nameLength: Int32) -> Int32
    @discardableResult
    static func spanSetAttribute(spanId: Int32, key: UnsafePointer<CChar>, keyLength: Int32, value: UnsafePointer<CChar>, valueLength: Int32) -> Int32
    @discardableResult
    static func spanSetStatus(spanId: Int32, status: Int32) -> Int32
    @discardableResult
    static func spanEnd(spanId: Int32) -> Int32
}
```

### BLE (Optional)

```swift
public enum BLE {
    @discardableResult static func initialize() -> Int32
    @discardableResult static func startAdvertising(name: UnsafePointer<CChar>, nameLength: Int32) -> Int32
    @discardableResult static func stopAdvertising() -> Int32
    @discardableResult static func startScan(durationMs: Int32, handlerId: Int32) -> Int32
    @discardableResult static func stopScan() -> Int32
    static func connect(addressType: Int32, address: UnsafePointer<CChar>, addressLength: Int32, handlerId: Int32) -> Int32
    @discardableResult static func disconnect(connHandle: Int32) -> Int32
}

public enum GATTS {
    static func addService(uuid: UnsafePointer<CChar>, uuidLength: Int32) -> Int32
    static func addCharacteristic(serviceId: Int32, uuid: UnsafePointer<CChar>, uuidLength: Int32, flags: Int32) -> Int32
    @discardableResult static func setValue(characteristicId: Int32, data: UnsafePointer<CChar>, dataLength: Int32) -> Int32
    @discardableResult static func notify(characteristicId: Int32, connHandle: Int32) -> Int32
    @discardableResult static func onWrite(characteristicId: Int32, handlerId: Int32) -> Int32
}

public enum GATTC {
    @discardableResult static func discover(connHandle: Int32, handlerId: Int32) -> Int32
    @discardableResult static func read(connHandle: Int32, attrHandle: Int32, handlerId: Int32) -> Int32
    @discardableResult static func write(connHandle: Int32, attrHandle: Int32, data: UnsafePointer<CChar>, dataLength: Int32) -> Int32
}
```

### USB (Optional)

```swift
public enum USB {
    static func cdcWrite(data: UnsafePointer<CChar>, length: Int32) -> Int32
    static func cdcRead(buffer: UnsafeMutablePointer<CChar>, length: Int32) -> Int32
    @discardableResult
    static func hidSendReport(reportId: Int32, data: UnsafePointer<CChar>, length: Int32) -> Int32
}
```

## Complete Example: HTTPS GET Request

```swift
import WendyLite

func print(_ message: StaticString) {
    message.withUTF8Buffer { buf in
        let ptr = UnsafeRawPointer(buf.baseAddress!).assumingMemoryBound(to: CChar.self)
        Console.print(ptr, length: Int32(buf.count))
    }
}

@_cdecl("_start")
func start() {
    print("Making HTTPS request...")

    let host: StaticString = "httpbin.org"
    let fd = host.withUTF8Buffer { buf in
        let ptr = UnsafeRawPointer(buf.baseAddress!).assumingMemoryBound(to: CChar.self)
        return TLS.connect(host: ptr, hostLength: Int32(buf.count), port: 443)
    }
    guard fd >= 0 else { print("Connect failed"); return }

    // Build request in stack buffer, send via TLS.send(fd:data:length:)
    // Read response via TLS.recv(fd:buffer:length:) in a loop
    // Close with TLS.close(fd:)
}
```

## Complete Example: LED Blink

```swift
import WendyLite

@_cdecl("_start")
func start() {
    GPIO.configure(pin: 2, mode: .output)

    while true {
        GPIO.write(pin: 2, level: 1)
        System.sleepMs(500)
        GPIO.write(pin: 2, level: 0)
        System.sleepMs(500)
    }
}
```

## Complete Example: NeoPixel Color Cycle

```swift
import WendyLite

@_cdecl("_start")
func start() {
    _ = NeoPixel.initialize(pin: 8, numLeds: 1)

    while true {
        NeoPixel.set(index: 0, r: 255, g: 0, b: 0)
        System.sleepMs(500)
        NeoPixel.set(index: 0, r: 0, g: 255, b: 0)
        System.sleepMs(500)
        NeoPixel.set(index: 0, r: 0, g: 0, b: 255)
        System.sleepMs(500)
    }
}
```
