# Language bindings

Each binding projects the normative C/protobuf contract into native naming, values, concurrency, errors, and resource lifetimes. Bindings may add ergonomic sugar but no independent product behavior.

Language SDK modules select compatible Core domains and share one runtime foundation. They must not load separate Go engines per module or hide a monolithic native payload behind apparently small wrappers. Platform adapters and subsystem backends are Core components, not language-binding implementations.

Core artifacts share the coordinated Core version; ergonomic SDKs may have independent release trains. Only responsibility stubs are present here; current runnable interop is in `distributions/`.
