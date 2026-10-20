# Wendy Core for Swift

The SwiftPM product `WendyCore`: a thin Swift layer over the C interface, separate from the Wendy Swift SDK ([wendy-swift](https://github.com/wendylabsinc/wendy-swift)), which builds on it. Its manifest is the repository root's `Package.swift`, because SwiftPM resolves packages from repository roots.

```swift
import WendyCore
let result = WendyHelloWorld() // Int32(42)
```

The Go engine is linked statically: Go `c-archive` libraries in an XCFramework for macOS 15+ (arm64/x64), iOS 18+ and the iOS Simulator (arm64/x64), and in a SwiftPM artifact bundle for Linux x64 (glibc 2.35+) and arm64 (glibc 2.39+). The C header is exposed to Swift as the `CWendyCore` module. Consumers need no Go toolchain. Swift 6.2+ is required. Other Swift-capable platforms are not automatically supported.

Each process should link at most one Go-based library: a second embedded Go runtime causes duplicate symbols or undefined behavior.

## Versions and binaries

Swift release tags are bare `x.y.z` (or an exact preview such as `x.y.z-nightly.<run>.<attempt>`); Go uses `vx.y.z`. SwiftPM prefers the bare tag, which names a release-only commit whose `Package.swift` downloads the checksummed binaries from the matching GitHub release:

```swift
.package(url: "https://github.com/wendylabsinc/wendy-core.git", from: "0.1.0")
```

On branches, including `main`, `Package.swift` instead uses binaries built locally. In this checkout, run `make swift` (or `python3 scripts/build.py swift`), which writes them to the ignored `distributions/swift/artifacts/`: on macOS the full Apple XCFramework, on Linux the host's artifact bundle. To develop a dependent package against this checkout, depend on it by path.
