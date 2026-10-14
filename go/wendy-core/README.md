# Townhall — Wendy Core

Wendy Core is a standalone, SemVer-versioned Go engine with language-consumable distributions. Wendy Swift, Wendy Python, Wendy Kotlin, etc. are separate SDK layers; they are not implemented here yet.

The runnable contract currently exports **one operation**, `WendyHelloWorld()`, returning the integer **42**. Every incarnation reaches the same implementation in `core/core.go`. No device, Cloud, authentication, or platform-capability behavior is implemented.

## Shape

```text
core/                    Public Go incarnation; no C round trip
cmd/wendy-core-native/   Go engine exported through the C bridge
cmd/wendy-core-wasm/     The same Go engine compiled for browsers
include/WendyCore/       Canonical public C contract
proto/                   Reserved structured Core schemas
internal/bridge/         Private native-loader mechanics

distributions/
  javascript/            One ESM npm package: Node native + browser WASM
  python/                Python-version-independent native wheels
  swift/                 Standalone SwiftPM package
  kotlin/                Kotlin/JVM Maven artifact

bindings/                Future language SDKs, not Core incarnations
platform/                Independent platform families and variants
documentation/           Contract, projection, versioning and release guides
examples/                HelloWorld consumers in all six languages
tools/                   Build, generation and release mechanics
.github/workflows/       Core-only CI and gated publication
```

Language and platform are orthogonal. In particular, the Swift incarnation is not the Apple adapter, and the Kotlin/JVM incarnation is not an Android adapter.

## Try it

Prerequisites: Go, a C compiler, Python 3.9+, `protoc`, and Clang. JavaScript tests additionally need Node 22+; Kotlin needs JDK 21. Swift needs Swift 6 and, on macOS, XcodeBuildMCP.

```sh
make check               # real Go/C tests, generated JS interface, metadata
make javascript          # Node + actual browser, including installed npm artifact
make python              # Python calls the native engine
make swift               # SwiftPM calls the packaged native engine
JAVA_HOME=/path/to/jdk21 make kotlin
make documentation       # Doxygen HTML/XML under .build/sdk/site
```

```go
import "github.com/wendylabsinc/townhall/core"
value := core.WendyHelloWorld() // 42; directly in Go
```

```js
import { WendyHelloWorld } from '@wendylabs/core';
console.log(WendyHelloWorld()); // 42; identical import/API in Node and browsers
```

The public JavaScript boundary is ESM with shared TypeScript declarations. Conditional exports select the backend. Node uses Node-API; the browser runs a real Go/WASM build. A CJS shim remains private to Node interop. The shared JS operation surface and declarations are generated from the C manifest; no Promise/iterator SDK sugar is included.

## HelloWorld examples

See [examples/README.md](examples/README.md) for small Go, C, Swift, Python, Kotlin/JVM and JavaScript applications. Each has its own run instructions and uses Wendy Core from disk; JavaScript includes both Node and browser execution.

## Bootstrap target matrix

| Target | C / Python / Node / Kotlin JVM | SwiftPM |
|---|---|---|
| Linux x64 (glibc 2.35+) | CI build/test target | CI build/test target |
| Linux arm64 (glibc 2.39+) | CI build/test target | Not yet supported |
| macOS x64 / arm64 (15+) | CI build/test targets | CI build/test targets |
| Windows x64 | CI build/test target | Not yet supported |
| Browser | Go/WASM, Chromium conformance | — |

This is the configured CI matrix, not a claim that hosted CI has already run. Local validation is recorded in the worker context. Alpine/musl, Windows arm64, Android, iOS, watchOS and other variants are not claimed by this bootstrap. Go callers use the native module directly; the native-binary matrix does not define every potential Go target.

## Release boundary

`VERSION` is the shared Core release version. All incarnations ship that version, not separate implementations or independent versions. Later SDKs depend on compatible Core releases and have independent release trains.

See [releasing](documentation/releasing.md) for packaging, registry configuration, and publication gates. Nothing is published by merely pushing `main`. This local repository has no configured remote yet.

The C distribution is only the bridge foundation—not an ergonomic C SDK. Public headers plus protobuf schemas remain normative. API manifests, JS projections, and reference sites are derived artifacts. Planned object/stream patterns are design notes, not implemented APIs.
