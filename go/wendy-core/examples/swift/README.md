# Swift HelloWorld

Requires Go, a C compiler, Python 3.9+, and Swift 6+. Current targets are macOS 15+ (arm64/x64) and Linux x64; this is not an iOS/watchOS example.

From the repository root, stage the local Core package's native resources:

```sh
python3 tools/build.py native
```

On macOS, install XcodeBuildMCP if needed (`npm install -g xcodebuildmcp@2.7.0`), then run:

```sh
xcodebuildmcp swift-package run --package-path "$PWD/examples/swift/hello-world" --executable-name HelloWorld
```

On Linux:

```sh
cd examples/swift/hello-world
swift run HelloWorld
```

Expected output: `WendyHelloWorld() = 42`.

`hello-world/Package.swift` depends on `../../../distributions/swift` on disk. That package loads the staged Go shared library; no remote SwiftPM mirror or release tag is needed.
