# `internal/cli/ble` — BLE central (client) for the Wendy CLI

This package is the **client side** of Wendy's Bluetooth Low Energy transport. It runs on the
developer machine (the BLE *central*) and talks to a WendyOS device or a Wendy Lite (ESP32)
board acting as the BLE *peripheral*.

It is three packages, split along exactly that line — the generic BLE work in two subpackages, the
Wendy protocol on top:

1. [`central`](central/) — a **generic, cross-platform BLE central API** (`Connection`) covering
   GATT and L2CAP, plus the L2CAP-to-`net.Conn` adapter TLS runs over.
2. [`scan`](scan/) — a **generic, cross-platform BLE scanner** that finds peripherals and yields the
   addresses `central.Connect` takes.
3. `ble` (this package) — the **Wendy-specific protocol clients** built on both (`LiteClient`,
   `AgentClient`).

Neither subpackage contains a Wendy identifier, and that is the point: see
[what is generic, and what is Wendy-specific](#what-is-generic-and-what-is-wendy-specific).

> The protocol clients themselves — `lite_client.go` and `agent_client.go` — are out of scope for
> this document. They are mentioned only where they illustrate what the transport layer supports,
> or where they are the reason a piece of it is Wendy-specific.

## Files

**`central/`** — the connection, generic:

| File | Role |
| --- | --- |
| `ble_darwin.go` / `.h` / `.m` | macOS backend. cgo bridge to a CoreBluetooth implementation in Objective-C. |
| `ble_linux.go` | Linux backend. Raw `AF_BLUETOOTH` / `BTPROTO_L2CAP` socket via `golang.org/x/sys/unix`. **L2CAP only.** |
| `ble_windows.go` | Windows stub. Every method returns "not implemented". |
| `conn.go` | Adapts an L2CAP channel to `net.Conn` so Go's `crypto/tls` can run over it (`NewL2CAPStream`), plus `ErrRecvTimeout` and `TimeoutSeconds`. |

**`scan/`** — finding peripherals, generic. Its own cgo bridge on macOS, BlueZ D-Bus on Linux, a
WinRT advertisement watcher on Windows. See [its section](#scanning-the-scan-subpackage).

**`ble/`** — the Wendy protocol, this package:

| File | Role |
| --- | --- |
| `lite_client.go` | Wendy Lite (ESP32) Wi-Fi provisioning over GATT (not covered here). |
| `lite_info.go` | Wendy Lite GATT info service — `ReadLiteInfo` reads the L2CAP PSM and the identity a device advertises for itself (not covered here). |
| `agent_client.go` | WendyOS agent RPC over mTLS-over-L2CAP (not covered here), plus `DefaultL2CAPPSM` — the PSM the agent listens on. |
| `agent_tls_over_ble.go` | `NewClientTLSConfig` — the mTLS config for the agent path, built on the Wendy PKI. |

There are no tests in `ble` or `central`. `scan/` has them — its engine is testable without a radio,
and a live, hardware-driven test is gated behind `WENDY_BLE_LIVE_SCAN` (see below).

## The `Connection` API ([`central`](central/))

Each platform file in `central/` defines the same `Connection` type and method set, selected by build
tag. The API is blocking, with integer-second timeouts.

```go
conn, err := central.Connect(address, 10)   // address semantics differ per platform, see below
defer conn.Close()

// GATT
err  = conn.DiscoverServices(10)
ok  := conn.HasService(serviceUUID)
s   := conn.ListServices()                                  // comma-separated discovered UUIDs
data, err := conn.ReadCharacteristic(svcUUID, chrUUID)
err  = conn.WriteCharacteristic(svcUUID, chrUUID, data)             // write with response
err  = conn.WriteCharacteristicNoResponse(svcUUID, chrUUID, data)   // write without response
err  = conn.Subscribe(svcUUID, chrUUID)                             // enable notifications
data, err = conn.WaitNotification(svcUUID, chrUUID, 5)              // pull one queued notification

// L2CAP (connection-oriented channel)
err  = conn.OpenL2CAP(psm, 10)
err  = conn.L2CAPSend(payload)
data, err = conn.L2CAPRecv(30)
```

### Capability matrix

| Capability | macOS | Linux | Windows |
| --- | :---: | :---: | :---: |
| Connect to peripheral | ✅ CoreBluetooth | ✅ (socket created; actual link comes up in `OpenL2CAP`) | ❌ |
| Service / characteristic discovery | ✅ | ❌ | ❌ |
| Read / write characteristic | ✅ | ❌ | ❌ |
| Write without response | ✅ | ❌ | ❌ |
| Notifications (subscribe + wait) | ✅ | ❌ | ❌ |
| L2CAP channel (open / send / recv) | ✅ | ✅ | ❌ |
| `net.Conn` + TLS over L2CAP | ✅ | ✅ | ❌ |

On Linux the GATT methods exist only to satisfy the shared method set; they return
`"GATT not implemented on Linux"`.

### Addressing is platform-dependent

`Connect` takes a *string*, but its meaning is not the same everywhere:

* **macOS** — a CoreBluetooth peripheral UUID (`"XXXXXXXX-XXXX-…"`). CoreBluetooth never exposes
  the hardware MAC address.
* **Linux** — a Bluetooth MAC address, `"AA:BB:CC:DD:EE:FF"` (parsed to LSB-first `[6]byte`,
  `BDADDR_LE_PUBLIC`).

Either source of addresses hands you the right kind of value per platform: the [`scan`](scan/)
subpackage in `BLEDeviceInfo.Address`, or [`shared/discovery`](../../shared/discovery/) in
`models.BluetoothDevice.Address`. Windows is the gap — `scan` can find peripherals there, but
`central.Connect` cannot reach them.

### L2CAP-over-TLS bridge (`central/conn.go`)

`NewL2CAPStream(conn)` wraps a `*Connection` as a `net.Conn`. Call it after `OpenL2CAP`; closing the
returned conn closes the underlying BLE connection.

* `Read` polls `L2CAPRecv` in `recvChunkSeconds` (2 s) slices and retries `ErrRecvTimeout`, so one
  `Read` may span many `L2CAPRecv` calls. Leftovers from an oversized read are buffered for later
  reads — needed because macOS coalesces incoming bytes into a stream buffer while Linux
  `SOCK_SEQPACKET` preserves SDU boundaries.
* **With no read deadline set, `Read` blocks indefinitely**, as `net.Conn` requires. The 2 s slice is
  polling granularity, not a timeout callers see. With a deadline it returns a `net.Error` whose
  `Timeout()` is true, which is what stops `crypto/tls` treating a deadline as a broken connection.
  Write deadlines are ignored.
* `Close` is idempotent and takes the same lock `Read` holds across `L2CAPRecv`, so it waits out an
  in-flight read (at most 2 s). Both matter: `Connection.Close` is *not* idempotent (on Linux it
  closes a bare fd without clearing it), and on macOS `wendy_ble_disconnect` hands the
  CoreBluetooth wrapper to ARC while a blocked reader still holds a bare pointer to it.
* `LocalAddr`/`RemoteAddr` are placeholders (`"ble-l2cap"` / `"ble"`).

`ble.NewClientTLSConfig` builds a `*tls.Config` for it. It sets `InsecureSkipVerify: true` — there is
no hostname to verify over L2CAP, and ML-DSA chain certs don't parse in Go's built-in verifier — and
performs real validation in a `VerifyConnection` callback from `shared/certs` (Wendy PKI + pin store).
That function is **Wendy-specific**, which is why it lives in `ble/agent_tls_over_ble.go` rather
than in `central/` with the `l2capNetConn` adapter underneath it.

## Scanning (the [`scan`](scan/) subpackage)

`scan` is the other half of the generic story: `central.Connect` needs an address, and this is where
addresses come from. It is deliberately as Wendy-free as `Connection` is — the services to look for are an
argument, and a result carries only what a BLE advertisement can actually supply.

```go
devices, err := scan.DiscoverBluetoothContinuous(ctx, scan.Options{
    Services: []string{"7565E9EB-4C20-4B67-9272-D708B397B631"}, // empty = every device in range
})
if err != nil {
    return err // the scan could not start at all; a mid-stream failure closes the channel instead
}
for set := range devices {
    // set is the COMPLETE list, sorted by RSSI descending — replace, don't merge.
    // set[i].Address feeds straight into central.Connect.
}
```

A stream re-emits the whole array whenever it changes and closes when `ctx` is cancelled. Devices
accumulate and are never dropped: BLE has no "device went away" signal, and a disappearance timeout
would make a peripheral blink out of a picker between advertisements. Emits are coalesced on
`Options.Interval` (1 s by default), which is what stops RSSI churn — it changes on nearly every
advertising packet — from spinning the channel.

Two things it deliberately does *not* report:

* **No L2CAP PSM.** A PSM is not in an advertisement. Get it from `ReadLiteInfo` after connecting,
  or fall back to the relevant client's own `DefaultL2CAPPSM` — `ble`'s for the agent,
  `liteclient`'s for Lite.
* **No stable cross-platform identity.** `Address` is a CoreBluetooth UUID on macOS and a MAC on
  Linux and Windows, exactly as `central.Connect` requires, so it cannot be compared across
  machines.

`Name` is the advertised local name, and it is a display label rather than an identity: macOS falls
back to CoreBluetooth's cached name and Linux to BlueZ's persisted one, so it can outlive the
advertisement it came from. Windows has no cache to fall back on and reports `""` instead.

Service UUIDs are normalized through `scan.CanonicalUUID`, which matters more than it looks:
CoreBluetooth renders a 16-bit UUID as four hex characters (`"180F"`) where BlueZ and WinRT give the
full 128-bit form, so comparing raw strings across platforms silently misses matches.

| Capability | macOS | Linux | Windows |
| --- | :---: | :---: | :---: |
| Continuous scan | ✅ CoreBluetooth | ✅ BlueZ D-Bus | ✅ WinRT advertisement watcher |
| Filter by service UUID | ✅ (in Go) | ✅ (BlueZ + Go) | ✅ (WinRT + Go) |
| RSSI | ✅ | ✅ | ✅ |
| Advertised name | ✅ | ✅ | ✅ (no cache fallback) |
| `RunBLECheck` is meaningful | ✅ | ❌ returns 0 | ❌ returns 0 |

**The Windows column is unverified on hardware.** It compiles and its JSON parsing is covered by
tests, but no one has run it against a real radio, and the WinRT event binding is the part most
likely to need work — `Register-ObjectEvent` binds WinRT `TypedEventHandler` events unevenly, so the
script may need an `Add-Type` inline-C# shim instead. Treat it as a first cut. Windows also has no
`central` backend, so nothing it finds can be connected to yet.

The macOS backend keeps a long-lived `CBCentralManager` and lets Go poll a snapshot, so there are no
cgo→Go callbacks and nothing ever calls into Go from a CoreBluetooth thread. It scans with
`AllowDuplicates: YES` and **no** native service filter — CoreBluetooth's own filter drops
peripherals whose advertisement omits the UUID, which is exactly the case a name-based fallback
exists to catch — and filters in Go instead. The Windows backend runs one long-lived Windows
PowerShell 5.1 process (the WinRT-projecting host) driving a `BluetoothLEAdvertisementWatcher` and
streaming JSON lines back.

Because CI has no radio and no macOS job compiles this cgo at all, the only thing that exercises the
real bridges is the hardware-gated test:

```sh
WENDY_BLE_LIVE_SCAN=1 go test ./internal/cli/ble/scan -run TestLiveScan -v
# WENDY_BLE_LIVE_SERVICES=<uuid>[,<uuid>] to exercise filtering
```

`shared/discovery`'s `bluetooth_{darwin,linux,windows}.go` still carry their own one-shot scan; the
intent is for them to keep only the Wendy policy (agent vs Lite UUIDs, name fallback, PSM 128/0) and
sit on top of this package.

## Platform implementation notes

### macOS (`central/ble_darwin.m`)

The interesting part is that this runs inside a **CLI binary with no main run loop**, which
CoreBluetooth normally assumes exists.

* The `CBCentralManager` gets its own serial dispatch queue; every C entry point blocks on a
  `dispatch_semaphore_t` signalled from the delegate callbacks.
* `Connect` first tries `retrievePeripheralsWithIdentifiers:` (CoreBluetooth shares its peripheral
  cache process-wide, so a peripheral already seen during discovery needs no re-scan) and only
  falls back to scanning. This avoids a 10 s stall when the device stops advertising between
  discovery and connect.
* L2CAP streams get a **dedicated `NSThread` running a real `NSRunLoop`**, because
  `NSStreamDelegate` events are otherwise never delivered. Writes are marshalled onto that thread
  with `performSelector:onThread:waitUntilDone:YES` — a `CBL2CAPChannel` output stream returns `-1`
  when written from a foreign thread (e.g. Go's TLS goroutine), even when the stream reports open.
  Channel setup waits for `NSStreamEventHasSpaceAvailable` (5 s cap) before declaring the channel
  usable.
* Incoming L2CAP bytes accumulate in an `NSMutableData` behind a lock; `L2CAPRecv` drains the whole
  buffer at once. Reads use a 4096-byte chunk.
* Requires cgo, `-framework CoreBluetooth`, and the macOS Bluetooth TCC permission. Sandboxed
  terminals can `SIGABRT` on CoreBluetooth init rather than returning an error, which is why the
  probe runs in a throwaway subprocess: `shared/discovery` re-execs the CLI as `__ble-check`, and
  `scan.RunBLECheck` is the same probe for a caller to wire into `scan.Options.Preflight`.
* This file and `../scan/scan_darwin.m` both link into one binary, so their C symbols and
  Objective-C class names must not collide — ObjC class names are process-global and a duplicate
  makes the runtime pick one arbitrarily. Hence `wendy_ble_*` / `WendyBLEConnection` here versus
  `wendy_blescan_*` / `WendyBLEScanSession` there.
* Error codes are a small C enum (`timeout`, `not found`, `connect failed`, `discover failed`,
  `write failed`, `read failed`, `L2CAP failed`, `disconnected`) mapped to Go `error` strings by
  `bleError`. The one exception is `L2CAPRecv`, which returns the `ErrRecvTimeout` sentinel instead
  of routing a timeout through `bleError`, so a caller can tell an idle channel from a dead one and
  retry. Everything else surfaces as an opaque string.

### Linux (`central/ble_linux.go`)

Pure Go, no cgo, no BlueZ D-Bus. `Connect` only creates the socket; `OpenL2CAP` does the
non-blocking `connect(2)` + `Poll` (so the timeout is respected) + `SO_ERROR` check, then switches
back to blocking mode. Receive is `Poll` + one `read(2)` into a 64 KiB buffer, one SDU per call.

Pairing/bonding is assumed to already exist (BlueZ handles it out of band); this package never
initiates it.

Note the asymmetry with the scanner: `../scan/scan_linux.go` *does* use BlueZ D-Bus, because typed
`org.bluez.Device1` properties are the only sane way to get advertised service UUIDs and RSSI. Only
the connection path avoids D-Bus, by going straight to an L2CAP socket.

## Known limitations

These are all about `central`'s `Connection` API; `scan`'s own caveats are in its section above. Worth
knowing before building anything else on this:

* **GATT is not goroutine-safe.** There is one semaphore per operation class, so at most one
  outstanding GATT operation per connection, and no request/response correlation. Concurrent GATT
  calls will cross-signal. The L2CAP data path is the exception: one concurrent sender plus one
  concurrent receiver is supported and relied on (`liteclient`'s framing writes from command
  goroutines while its read loop blocks in `Read`). On macOS that holds because sends are
  dispatched onto the I/O thread and never touch the recv buffer or its semaphore; on Linux because
  `write(2)` and `read(2)` on one socket are independent. Two concurrent *senders* are still unsafe
  — on macOS they race on the shared write result, and on Linux `L2CAPSend`'s write loop would
  interleave and corrupt the stream — so callers must serialize writes, as `liteclient` does.
* **`WaitNotification` is per-connection, not per-characteristic.** All subscriptions share one
  semaphore; a notification on characteristic *A* wakes a waiter on *B*, which then finds its own
  queue empty and reports a timeout. Fine with one subscription, fragile with several.
* **A read on a subscribed characteristic lands in the notification queue.** The macOS
  `didUpdateValueForCharacteristic:` handler checks the notify queues first, so `ReadCharacteristic`
  on a characteristic you've subscribed to never gets its response and times out after 10 s.
* **No `context.Context`**, no cancellation; timeouts are whole seconds only, and several
  (GATT read/write/subscribe: 10 s) are hard-coded.
* **No MTU or max-SDU negotiation** is exposed.
* **No descriptor access, no indications** (only notifications), no characteristic-property
  inspection.
* **Central role only** — no advertising, no GATT server, no peripheral role.
* **No reconnect or retry logic**; a disconnect surfaces as an error on the next call.
* On macOS, a disconnect wakes blocked waiters but `L2CAPRecv` may report `disconnected` rather
  than a clean EOF.

## What is generic, and what is Wendy-specific

**The split is the directory layout**, so the compiler enforces it rather than a convention:
`central/` and `scan/` know nothing about Wendy, and `ble` is where everything Wendy-specific lives.
Adding a Wendy identifier to either subpackage is the change to push back on — and adding a
`shared/models` or `shared/certs` import to one would be the first sign of it.

### Generic — no Wendy identifiers anywhere

| Piece | Why it is generic |
| --- | --- |
| `central.Connection` and every method on it | Takes plain service/characteristic UUIDs and PSMs. There are no hard-coded Wendy UUIDs in `central/`. |
| The CoreBluetooth backend (`central/ble_darwin.*`) | A genuinely reusable macOS BLE central for CLI programs. The run-loop and threading work it does is the hard part of that problem. |
| `central.NewL2CAPStream` (`l2capNetConn`) | A general-purpose L2CAP-to-`net.Conn` adapter. |
| `central.ErrRecvTimeout`, `central.TimeoutSeconds` | Sentinel and duration conversion the layers above need; `TimeoutSeconds` is exported only because `ble/lite_info.go` must round the same way. |
| The [`scan`](scan/) subpackage | Services to match are an argument; results carry only what an advertisement supplies. No PSM, no name prefixes. |

Neither subpackage imports `shared/models` or `shared/certs`.

### Wendy-specific — the `ble` package

| Piece | What ties it to Wendy |
| --- | --- |
| `lite_client.go` | Hard-coded Wendy Lite service/characteristic UUIDs and command/status bytes. |
| `lite_info.go` | Likewise, for the Wendy Lite info service. |
| `agent_client.go` | Protobuf framing, `agentpb` types, its own reply timeout. |
| `agent_tls_over_ble.go` — `NewClientTLSConfig` | Depends on `shared/certs` — Wendy PKI and pin store. Agent-only; the Lite path builds its own config in `liteclient`. |
| `DefaultL2CAPPSM` — one per protocol | `ble`'s (in `agent_client.go`) is the PSM the WendyOS agent listens on; `liteclient`'s (in `link_ble.go`) is the Lite firmware's. Both are 128 today and both are **independent** — two programs' listening PSMs, one of them firmware from another repo, either free to move. A PSM is a Wendy convention rather than a property of BLE, which is why neither lives in `central/`. |
| `ConnectLite` / `ConnectAgent` | Take `*models.BluetoothDevice`, a `shared/models` type populated by `shared/discovery`. |
