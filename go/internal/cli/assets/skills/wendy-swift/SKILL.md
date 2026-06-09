---
name: wendy-swift
description: 'Curated Swift package ecosystem for WendyOS and Linux. Use when developers mention: (1) Swift packages for Linux or ARM64/AMD64, (2) choosing a Swift library, (3) Swift Package Index, (4) swiftpackageindex.com, (5) what Swift library to use, (6) Swift on WendyOS dependencies, (7) edge computing Swift libraries.'
---

# Swift Package Ecosystem for WendyOS

This skill maps the Swift package ecosystem for WendyOS (Linux) edge development. It provides curated recommendations organized by domain and guidance for discovering additional packages via Swift Package Index.

## Curated Package Catalog

### Web / HTTP Servers

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| Hummingbird 2 | Lightweight HTTP server, routing, middleware | https://github.com/hummingbird-project/hummingbird | Full support |
| Vapor | Full-featured web framework with ORM, auth, sessions | https://github.com/vapor/vapor | Full support |

### Networking / SwiftNIO

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| SwiftNIO | Event-driven networking framework, TCP/UDP | https://github.com/apple/swift-nio | Full support |
| AsyncHTTPClient | HTTP/1.1 and HTTP/2 client | https://github.com/swift-server/async-http-client | Full support |
| gRPC Swift 2 | gRPC client and server built on SwiftNIO and Swift Concurrency | https://github.com/grpc/grpc-swift-2 | Full support |
| Swift OpenAPI Generator | Generate client/server code from OpenAPI specs | https://github.com/apple/swift-openapi-generator | Full support |

### Databases

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| PostgresNIO | Non-blocking PostgreSQL client built on SwiftNIO | https://github.com/vapor/postgres-nio | Full support |
| GRDB.swift | SQLite toolkit with query builder, migrations, WAL | https://github.com/groue/GRDB.swift | Full support |
| Valkey Swift | Valkey/Redis client with cluster support, pub/sub | https://github.com/valkey-io/valkey-swift | Full support |

### ML / AI Inference

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| llama.cpp | LLM inference with Swift bindings | https://github.com/ggerganov/llama.cpp | Full support (C library with Swift bindings) |
| whisper.cpp | Speech-to-text inference | https://github.com/ggerganov/whisper.cpp | Full support (C library with Swift bindings) |
| MLX Swift | ML framework for Apple Silicon | https://github.com/ml-explore/mlx-swift | macOS/Apple Silicon only — not available on Linux |

For llama.cpp and whisper.cpp, use the C API directly via Swift's C interop or use community Swift wrapper packages. These libraries compile natively on ARM64/AMD64 Linux.

### Hardware / IoT

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| SwiftyGPIO | GPIO, SPI, I2C, PWM, UART on Linux SBCs | https://github.com/uraimo/SwiftyGPIO | Full support (Linux only) |

**Camera/video access patterns:** Use Video4Linux2 (V4L2) through Swift's C interop for camera capture on Linux. For NVIDIA Jetson, use GStreamer pipelines via `Process` or C bindings for hardware-accelerated video.

**Serial communication:** Use termios via Swift's C interop for serial port access on Linux. SwiftyGPIO also provides UART support for common SBCs.

### Observability

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| swift-log | Structured logging API | https://github.com/apple/swift-log | Full support |
| swift-metrics | Metrics API (counters, gauges, timers) | https://github.com/apple/swift-metrics | Full support |
| swift-otel | OpenTelemetry backend for swift-log, swift-metrics, and distributed tracing | https://github.com/swift-otel/swift-otel | Full support |
| swift-distributed-tracing | Distributed tracing API | https://github.com/apple/swift-distributed-tracing | Full support |

### Serialization / Codable

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| swift-protobuf | Protocol Buffers with Codable support | https://github.com/apple/swift-protobuf | Full support |
| msgpack-swift | MessagePack encoder/decoder, Codable-compliant | https://github.com/fumoboy007/msgpack-swift | Full support |
| Yams | YAML parser and emitter | https://github.com/jpsim/Yams | Full support |

### Concurrency / Utilities

| Package | Use Case | URL | Linux |
|---------|----------|-----|-------------|
| swift-collections | Deque, OrderedSet, OrderedDictionary, and more | https://github.com/apple/swift-collections | Full support |
| swift-algorithms | Sequence/collection algorithms (chunked, combinations, etc.) | https://github.com/apple/swift-algorithms | Full support |
| swift-async-algorithms | Async sequence algorithms (merge, combineLatest, debounce, etc.) | https://github.com/apple/swift-async-algorithms | Full support |
| swift-argument-parser | Type-safe command-line argument parsing | https://github.com/apple/swift-argument-parser | Full support |

## Linux Compatibility Checklist

Before adding a Swift package to a WendyOS project, verify:

1. **No Darwin-only imports** — Check for `import Darwin`, `import AppKit`, `import UIKit`, `import CoreFoundation` (some Foundation APIs are fine on Linux, but not all)
2. **Foundation compatibility** — Avoid APIs that are unimplemented on Linux: `FileManager` works, but `NSAppleScript`, `NSUserDefaults`, `Process` (partially available), etc. may not
3. **No Xcode build system dependencies** — The package must build with `swift build` (SPM), not require Xcode-specific settings
4. **ARM64/AMD64 CI/testing** — Check if the package has Linux CI (GitHub Actions with `ubuntu` runners or Swift Docker images)
5. **C dependencies** — If the package wraps a C library, ensure that library is available on ARM64/AMD64 Linux (e.g., via apt)
6. **Swift version** — WendyOS uses recent Swift toolchains; confirm the package supports Swift 5.9+

## Finding Packages on Swift Package Index

[Swift Package Index](https://swiftpackageindex.com) is the community package search engine.

### How to search

1. Go to https://swiftpackageindex.com and enter your search term
2. Use the **Platform** filter to select **Linux** to find Linux-compatible packages
3. Check the **Compatibility Matrix** on each package page — it shows which Swift versions and platforms are tested

### Evaluating package quality

- **Last activity** — Prefer packages updated within the last 6 months
- **Swift version compatibility** — Should support Swift 5.9 or later
- **Platform badges** — Look for the Linux badge on the package page
- **License** — Prefer Apache 2.0 or MIT for commercial use
- **SSWG status** — Packages incubated by the [Swift Server Work Group](https://www.swift.org/sswg/) are production-vetted

### Search URL pattern

Direct search: `https://swiftpackageindex.com/search?query=YOUR_TERM`

## Best Practices

1. **Use the curated list first** — Packages listed above are vetted for Linux and WendyOS compatibility
2. **Prefer SSWG packages** — Swift Server Work Group packages (SwiftNIO, swift-log, async-http-client, Hummingbird, PostgresNIO) have strong Linux support guarantees
3. **Watch for architecture-specific code** — Swift packages compile from source, so ARM64/AMD64 works automatically unless the package contains x86-specific assembly, SIMD intrinsics, or architecture-gated `#if` blocks
4. **Avoid Darwin-only dependencies** — Even transitive dependencies can break Linux builds; use `swift package show-dependencies` to audit the tree
5. **Pin to stable versions** — Use `.upToNextMajor(from:)` or `.upToNextMinor(from:)` in Package.swift rather than branch-based dependencies
6. **Fall back to Swift Package Index** — If the curated list doesn't cover your need, search swiftpackageindex.com with the Linux platform filter
7. **Test on Linux early** — Build and run on a WendyOS device or Linux VM early to catch missing Foundation APIs and Darwin-only dependencies before they compound
