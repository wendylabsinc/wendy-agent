# Core bridge mechanics

`cmd/wendy-core-native` exports the canonical C function from the shared Go engine. `cmd/wendy-core-wasm` projects that same function for browser execution. `native_loader.h` is private shared packaging glue for Node, Swift and JNI.

These adapters own no product behavior. Future operation glue resolves opaque handles, decodes protobuf inputs, derives cancellation contexts, invokes the Go engine, and transfers outputs according to the normative contract. No Go pointers may cross the C ABI.

Go shared libraries remain loaded for the process lifetime; unloading a wrapper must not tear down the Go runtime. This does not imply future operation/device resources may leak—they will have explicit lifetimes.
