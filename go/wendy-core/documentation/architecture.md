# Architecture and names

- **Townhall**: repository codename.
- **Wendy Core**: shared Go engine, bridge contract, and independently released language incarnations.
- **Wendy Platform**: native capabilities, implemented in the platform's appropriate language.
- **Wendy Swift / Wendy Python / Wendy Kotlin / Wendy JavaScript / Wendy Go / Wendy C**: future language SDKs above Core. Collectively, the **Wendy SDK**.

Spaced names are used in prose; joined names such as `WendyCore` are used where a code/module identifier is needed. Repository organizational directories are lowercase; language conventions such as SwiftPM `Sources` and public C `WendyCore` are retained.

## Independent axes

Language bindings, platform families, and platform variants are separate axes. Swift can run on Linux or Windows; an Apple platform adapter may be written in Swift while serving a Python consumer. Kotlin/JVM consumption is not equivalent to Android platform support.

Each release declares supported combinations, capabilities, and minimum system versions. A language running on a target does not prove that Go Core can run there. An operation with unavailable platform capabilities must not silently acquire different product behavior in another binding.

## Release and implementation boundaries

One Core version has C, Go, SwiftPM, Python, Maven, and npm incarnations. These are packaging and interop, not separate engines. SDKs may independently version their ergonomic layers and declare compatible Core dependencies.

Go consumers import the engine directly. Native foreign-language consumers use the C bridge. Browser JavaScript runs the Go engine in WebAssembly through a backend projecting the same operation contract.

The shared JavaScript interface is generated from C declarations. Native handles must eventually map to runtime-independent opaque handles rather than exposed pointer values; ownership/cancellation/demand semantics must remain equal across backends. `WendyHelloWorld` is intentionally scalar so the first packaging proof adds none of those unimplemented lifecycle APIs.

Platform adapters live outside language SDKs. Apple has shared-family code and variant directories for iOS, macOS and watchOS; other families have independent directories. These are responsibility placeholders, not support claims.
