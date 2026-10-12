# Wendy SDK

This fresh repository is a small, executable scaffold for the cross-platform SDK architecture. It is intentionally **not a complete or production-ready SDK**.

## What is here

- `wendy/` — the public Go API used directly by Go callers and the Wendy CLI.
- `internal/core/` — the future shared Go implementation.
- `include/Wendy/` — the stable, flat C bridge contract for foreign-language bindings.
- `proto/wendy/sdk/v1/` — structured cross-language request, response, and event values.
- `internal/bridge/` — the future thin Go-to-C adapter, with no independent behavior.
- `bindings/` — responsibility stubs for full C, Swift, Python, and Kotlin projections.
- `platform/` — the boundary for narrow native capability adapters.
- `Documentation/` — inherited C semantics and native binding patterns.
- `cmd/wendy-sdk-docs/` — a small Go tool that joins Clang's C AST with protobuf descriptors into versioned `api.json` snapshots and compares them.
- `gen/` — generated Go protobuf values.

## Try the mechanics

Prerequisites are Go, Clang, and `protoc`. Doxygen is optional unless rendering the HTML reference.

```sh
make check
make documentation-manifest
make compare-clean
```

`make documentation` additionally renders the C and Markdown reference into `.build/sdk/site` when Doxygen is installed.

## Current surface

The draft contract contains only:

- opaque runtime, device, operation, stream, data, and error objects;
- ownership, cancellation, callback, and backpressure primitives;
- a versioned but deliberately empty platform capability table;
- one representative unary operation (`GetInfo`); and
- one representative demand-driven stream (`WatchState`).

These operations demonstrate the shape of the contract; they do not have a product backend yet.

## Design constraints

- Go owns behavior. Bindings adapt types and concurrency but do not reimplement product logic.
- Go consumers do not serialize through C.
- Structured control values use SDK protobuf messages; bulk payloads use immutable raw buffers.
- C inputs use east-const (`WendyDevice const *device`). Returns, callback outputs, and output parameters are not const-qualified.
- Opaque objects never expose Go pointers.
- The C headers, protobuf schemas, and inherited contract pages are normative. Generated manifests and sites are derived.
