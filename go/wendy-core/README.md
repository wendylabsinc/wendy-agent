# Wendy Core

`wendy-core` is the standalone, strict-SemVer release unit for the portable engine, selected modules, public contracts and included platform/subsystem implementations. Go is the chosen engine implementation. Core ships in language-consumable distributions. Wendy Swift, Wendy Python, Wendy Kotlin, etc. are separate SDK layers; they are not implemented here yet.

The runnable contract currently exports **one operation**, `WendyHelloWorld()`, returning the integer **42**. Every incarnation reaches the same implementation in `core/core.go`. No device, Cloud, authentication, or platform-capability behavior is implemented.

## Shape

```text
core/                    Public Go incarnation; no C round trip
  runtime/               Shared runtime foundation placeholder
  identity/              Proposed domain placeholder
  devices/               Proposed domain placeholder
  applications/          Proposed domain placeholder
  events/                Proposed domain placeholder
cmd/wendy-core-native/   Go engine exported through the C bridge
cmd/wendy-core-wasm/     The same Go engine compiled for browsers
include/WendyCore/       Public C headers: binding API + reserved platform SPI
proto/                   Reserved structured Core schemas
internal/bridge/         Private native-loader mechanics

distributions/
  javascript/            One ESM npm package: Node native + browser WASM
  python/                Python-version-independent native wheels
  swift/                 Standalone SwiftPM package
  kotlin/                Kotlin/JVM Maven artifact

bindings/                Future language SDKs, not Core incarnations
platform/                Included adapter families/variants; Core-versioned
subsystems/              Reusable infrastructure behind explicit contracts
documentation/           Contract, projection, versioning and release guides
examples/                HelloWorld consumers in all six languages
scripts/                 Build, generation and release mechanics
.github/workflows/       Core-only CI and gated publication
```

Language and platform are orthogonal. In particular, the Swift incarnation is not the Apple adapter, and the Kotlin/JVM incarnation is not an Android adapter.

## Agreed architecture

One SDK family serves Companion/CLI, WendyOS/Agent, and apps using Wendy capabilities. Selectable domain modules and their dependencies share one `WendyRuntime` foundation and one composed engine, rather than pulling every domain into each consumer.

Portable Core sits between a stable binding contract above and a platform-capability SPI below. Substantial infrastructure such as container execution has separate subsystem contracts so it can remain reusable even if Core were theoretically implemented in another language. Go remains the chosen implementation; alternative engines are not planned.

See [architecture and boundaries](documentation/architecture.md) for the diagrams and implementation-neutral contracts, and [module composition](documentation/modules.md) for dependency/scaffold rules. These are recorded design decisions, not features already implemented by the HelloWorld bootstrap.

## Try it

Clone the repository and enter the checkout:

```sh
git clone git@github.com:wendylabsinc/wendy-core.git
cd wendy-core
```

Prerequisites: Go, a C compiler, Python 3.9+, `protoc`, and Clang. JavaScript tests additionally need Node 22+; Kotlin needs JDK 21. Swift tests need Swift 6.2+ and, on macOS, XcodeBuildMCP. Documentation generation needs Doxygen and Graphviz (`dot`).

```sh
make check               # real Go/C tests, generated JS interface, metadata
make javascript          # Node + actual browser, including installed npm artifact
make python              # Python calls the native engine
make swift               # SwiftPM calls the packaged native engine
JAVA_HOME=/path/to/jdk21 make kotlin
make documentation       # Doxygen HTML/XML under .build/sdk/site
```

```go
import "github.com/wendylabsinc/wendy-core/core"
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

[Wendy Core CI](https://github.com/wendylabsinc/wendy-core/actions/workflows/ci.yml) builds and tests this matrix. A passing run verifies the HelloWorld bootstrap, not the proposed domains or platform capabilities. Alpine/musl, Windows arm64, Android, iOS, watchOS and other variants are not claimed by this bootstrap. Go callers use the native module directly; the native-binary matrix does not define every potential Go target.

## Release boundary

`VERSION` is the shared Core release version. The runtime, selected modules, binding/platform/subsystem contracts and included adapters/backends belong to that release. All incarnations ship that version, not separate implementations or independent module/platform versions. Later SDKs depend on compatible Core releases and have independent release trains.

See [releasing](documentation/releasing.md) for packaging, registry configuration, and publication gates. Pushing `main` runs Core CI but does not publish package releases. Registry publication remains explicitly gated; see the release guide before creating any version tag.

## License

Licensed under the Apache License, Version 2.0; see `LICENSE` and `NOTICE`. Native/WASM packages retain the Go runtime's separate BSD license notice. Original Gradle Wrapper notices are preserved.

## Contributing

Start with the architecture and module guides above. Keep shared behavior in Core, interop mechanical, and platform/subsystem implementations behind their contracts. Run `make check compare-clean documentation` and the affected language tests before submitting changes. Runtime/domain/provider placeholders are design scaffolding, not implemented APIs or support guarantees.

The C distribution is only the bridge foundation—not an ergonomic C SDK. Public headers plus protobuf schemas remain normative. API manifests, JS projections, and reference sites are derived artifacts. Planned object/stream patterns are design notes, not implemented APIs.
