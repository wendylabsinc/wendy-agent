# Wendy Core for Swift

A standalone SwiftPM product `WendyCore`, separate from the future Wendy Swift SDK.

```swift
import WendyCore
let result = try WendyHelloWorld() // Int32(42)
```

This is native-engine interop only. The operation is synchronous; throws report package/library loading failures, not an invented product error. Supported bootstrap targets are macOS 15+ on x64/arm64 and Linux glibc 2.35+ on x64. Swift 6 is required. Other Swift-capable platforms are not automatically supported.

The release distribution includes native libraries and a private C loader in its resources, so downstream consumers use an ordinary SwiftPM dependency with no Go toolchain. Go libraries remain loaded for the process lifetime. Go runtime notices are included alongside them.

In the `wendy-core` source checkout, run `python3 scripts/build.py native` before testing; generated resources and header copies are not checked into the implementation repository. The generated release/mirror includes them.
