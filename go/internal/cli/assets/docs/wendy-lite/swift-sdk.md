# Swift SDK Internals

The `WendyLite` SwiftPM library is an Embedded Swift target compiled to `wasm32-unknown-wasip1`. This document covers how the async runtime, entry point protocol, timers, and callback dispatch work — context needed when extending the SDK or debugging unexpected behaviour.

## Requirements

- Swift 6.3.1 via `swiftly`
- Embedded Swift SDK: `swift-6.3.1-RELEASE_wasm-embedded`
- Target triple: `wasm32-unknown-wasip1`

Build command:

```bash
swiftly run +6.3.1 swift build \
    --swift-sdk swift-6.3.1-RELEASE_wasm-embedded \
    --triple wasm32-unknown-wasip1 \
    -c release
```

## Entry Point: WendyLiteApp

`WendyLiteApp` is a protocol with a static `main()` entry point wired to `@main`. Conform a type to it and annotate with `@main`:

```swift
@main
struct MyApp: WendyLiteApp {
    let clock = WendyClock()

    mutating func setup() async {
        // One-time init (hardware config, WiFi connect, etc.)
    }

    mutating func loop() async {
        // Called repeatedly for the lifetime of the app
    }
}
```

`setup()` has a default no-op implementation so it is optional.

`main()` does the following before entering the loop:

1. Calls `bootstrapAsyncRuntime()` (idempotent, guarded by `RuntimeBootstrapState`).
2. Constructs an instance via `Self()`.
3. Awaits `setup()`.
4. Loops forever awaiting `loop()`.

## Async Runtime Bootstrap

Embedded Swift on WASM runs a cooperative single-threaded executor. `bootstrapAsyncRuntime()` (in `WendyLiteApp.swift`) sets it up:

1. Registers the timer callback handler ID with `CallbackDispatch` (handler ID `1`).
2. Drains any events that arrived before user code starts (`pumpAsyncRuntimeOnce(timeoutMs: 0)`).
3. Launches a background `Task` that calls `pumpAsyncRuntimeOnce(timeoutMs: 250)` in a loop, yielding between iterations so Swift tasks can run.

`pumpAsyncRuntimeOnce` calls `System.waitForEvent(timeoutMs:)` (which maps to `sys_wait_for_event` on the host and blocks until a callback fires or the timeout elapses), then calls `TimerState.shared.drainReady()` to resume any sleeping tasks whose deadlines have passed.

### Why not Task.sleep?

`Task.sleep` requires a conforming `Clock` type, but Embedded Swift cannot satisfy the untyped throwing requirement in the `Clock` protocol. Use `WendyClock` instead.

## WendyClock

`WendyClock` (`WendyClock.swift`) is a concrete async-sleep implementation that drives `await clock.sleep(for:)` and `await clock.sleep(until:)`.

```swift
let clock = WendyClock()
try? await clock.sleep(for: .milliseconds(500))
```

Internally:

- `now` reads `System.uptimeMs()` and wraps it in `WendyClock.Instant`.
- `sleep(until:)` delegates to `TimerState.shared.sleep(until:)` in `TimerHub`.

### TimerHub

`TimerHub` maintains a sorted array of `Waiter` entries (deadline + checked continuation). When a sleep is requested:

1. A `Waiter` is inserted in deadline order.
2. `rescheduleForEarliestDeadline()` calls `Timer.setTimeout` on the host for the earliest pending deadline (cancelling any previously scheduled timer first).
3. When the host fires the timer, `timerFired()` moves all expired waiters to `readyWaiters`.
4. `drainReady()` (called from `pumpAsyncRuntimeOnce`) resumes those continuations.

Cancellation is handled via `withTaskCancellationHandler`: a `SleepRegistration` flag is shared between the operation and the cancel handler so that cancellation arriving before or after the continuation is registered is always handled correctly.

**Edge case**: If `Timer.setTimeout` returns a negative ID, the host could not allocate an ESP timer (no free slots or `esp_timer_create`/`esp_timer_start` failure). Pending sleepers will stall in that case — no recovery is attempted.

## Callback Dispatch

`CallbackDispatch` (`CallbackDispatch.swift`) is the single routing point for all host-to-guest async events (GPIO interrupts, BLE events, timer callbacks, UART receive callbacks).

```swift
// Register a handler for handler_id = 42
CallbackDispatch.register(42) { arg0, arg1, arg2 in
    // handle event
}
```

The exported C function `wendy_handle_callback` (which the host calls to dispatch events into the WASM guest) routes by handler ID through the registered table.

Handler IDs are application-defined integers. Timer callbacks internally use ID `1` (a private constant in `WendyClock.swift`). Your application must use IDs that do not collide with internal use.

## SwiftPM Package Structure

```
Package.swift
  targets:
    CWendyLite   (C target) — wendy.h + shim.c
    WendyLite    (Swift target, depends on CWendyLite)
      swiftSettings: .enableExperimentalFeature("Embedded"), -wmo
```

The `WendyLite` target uses whole-module optimisation (`-wmo`) and the Embedded experimental feature, both of which are required for the WASM Embedded SDK. App packages must replicate these settings in their own executable target (see the README for the complete template).

## Linker Requirements for App Packages

App packages must pass specific linker flags:

| Flag | Purpose |
|------|---------|
| `--allow-undefined` | Host imports are resolved at load time by WAMR |
| `--initial-memory=65536` | Minimum WASM linear memory |
| `--table-base=1` | Avoid table slot 0 (reserved by some runtimes) |
| `--strip-all` | Minimise binary size |
| `--export=malloc`, `--export=free` | Host may call these for buffer allocation |
| `--export=wendy_handle_callback` | Required for host-to-guest callbacks |
| `-z stack-size=8192` | Stack size; increase for deeper call stacks |

## Known Constraints

- **No `Task.sleep`** — use `WendyClock.sleep`.
- **Single-threaded executor** — concurrent tasks interleave cooperatively; there is no parallelism.
- **Timer slots are finite** — the ESP timer subsystem has a limited pool; each outstanding `WendyClock.sleep` occupies one slot for the minimum-deadline timer.
- **nonisolated(unsafe)** — `CallbackDispatch` and `TimerState` use `nonisolated(unsafe)` storage because the cooperative single-threaded WASM executor guarantees no true concurrent access.
