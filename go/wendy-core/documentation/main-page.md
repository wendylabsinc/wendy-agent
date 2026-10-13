# Wendy Core

Wendy Core is one standalone Go engine released in several dependency-manager formats. Townhall is its repository codename.

## Current operation

`WendyHelloWorld()` returns the signed 32-bit integer `42`, synchronously without transferring owned resources or requiring a platform capability. It is implemented once in Go and projected into C, Swift, Python, Kotlin/JVM and JavaScript.

JavaScript has one ESM package, one operation interface, shared TypeScript declarations, and separate Node/native and browser/WASM backends. Both execute the same conformance suite against the actual engine.

## Contract and layering

The canonical contract is in `include/WendyCore` and `proto/wendy/core/v1`. Language-incarnation packages perform interop only. Future Wendy language SDKs add ergonomics above Core; platform adapters independently provide native capabilities.

Object ownership, async unary, demand-driven streams and bulk buffers under `binding-patterns` and `c-contract` are planned conventions. They do not add operations to the current release.

See `architecture.md`, `api-manifest.md`, and `releasing.md` for the design and build workflow.
