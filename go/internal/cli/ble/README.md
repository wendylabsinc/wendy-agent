# `internal/cli/ble` — BLE central (client) for the Wendy CLI

This package is the **client side** of Wendy's Bluetooth Low Energy transport. It runs on the
developer machine (the BLE *central*) and talks to a WendyOS device or a Wendy Lite (ESP32)
board acting as the BLE *peripheral*.

It provides two things:

1. A **generic, cross-platform BLE central API** — `Connection` — covering GATT and L2CAP.
2. **Wendy-specific protocol clients** built on top of it (`LiteClient`, `AgentClient`).

> The protocol clients themselves — `lite_client.go` and `agent_client.go` — are out of scope for
> this document. They are mentioned only where they illustrate what the transport layer supports,
> or where they are the reason a piece of it is Wendy-specific.

## Files

| File | Role |
| --- | --- |
| `ble_darwin.go` / `.h` / `.m` | macOS backend. cgo bridge to a CoreBluetooth implementation in Objective-C. |
| `ble_linux.go` | Linux backend. Raw `AF_BLUETOOTH` / `BTPROTO_L2CAP` socket via `golang.org/x/sys/unix`. **L2CAP only.** |
| `ble_windows.go` | Windows stub. Every method returns "not implemented". |
| `conn.go` | Adapts an L2CAP channel to `net.Conn` so Go's `crypto/tls` can run over it, plus the Wendy client TLS config. |
| `lite_client.go` | Wendy Lite (ESP32) Wi-Fi provisioning over GATT (not covered here). |
| `lite_info.go` | Wendy Lite GATT info service — `ReadLiteInfo` reads the L2CAP PSM and the identity a device advertises for itself (not covered here). |
| `agent_client.go` | WendyOS agent RPC over mTLS-over-L2CAP (not covered here). |

There are no tests in this package.

## The `Connection` API

Each platform file defines the same `Connection` type and method set, selected by build tag. The
API is blocking, with integer-second timeouts.

```go
conn, err := ble.Connect(address, 10)   // address semantics differ per platform, see below
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

Callers get this string from [`shared/discovery`](../../shared/discovery/), which fills
`models.BluetoothDevice.Address` with the right kind of value per platform. **This package does no
scanning or discovery of its own.**

### L2CAP-over-TLS bridge (`conn.go`)

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

`NewClientTLSConfig` builds a `*tls.Config` for it. It sets `InsecureSkipVerify: true` — there is no
hostname to verify over L2CAP, and ML-DSA chain certs don't parse in Go's built-in verifier — and
performs real validation in a `VerifyConnection` callback from `shared/certs` (Wendy PKI + pin store).
This function is **Wendy-specific**; the `l2capNetConn` adapter underneath it is not.

## Platform implementation notes

### macOS (`ble_darwin.m`)

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
* Requires cgo, `-framework CoreBluetooth`, and the macOS Bluetooth TCC permission. (Sandboxed
  terminals can `SIGABRT` on CoreBluetooth init — hence the separate `__ble-check` subprocess in
  `shared/discovery`.)
* Error codes are a small C enum (`timeout`, `not found`, `connect failed`, `discover failed`,
  `write failed`, `read failed`, `L2CAP failed`, `disconnected`) mapped to Go `error` strings by
  `bleError`. The one exception is `L2CAPRecv`, which returns the `ErrRecvTimeout` sentinel instead
  of routing a timeout through `bleError`, so a caller can tell an idle channel from a dead one and
  retry. Everything else surfaces as an opaque string.

### Linux (`ble_linux.go`)

Pure Go, no cgo, no BlueZ D-Bus. `Connect` only creates the socket; `OpenL2CAP` does the
non-blocking `connect(2)` + `Poll` (so the timeout is respected) + `SO_ERROR` check, then switches
back to blocking mode. Receive is `Poll` + one `read(2)` into a 64 KiB buffer, one SDU per call.

Pairing/bonding is assumed to already exist (BlueZ handles it out of band); this package never
initiates it.

## Known limitations

Worth knowing before building anything else on this:

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

## Can this be reused for generic (non-Wendy) BLE clients?

**Partly — the transport layer is generic, the rest is not.** The package splits in two, almost
cleanly:

**Generic** — nothing about the API shape or the platform backends is Wendy-specific:

* `Connection` and all its methods take plain service/characteristic UUIDs and PSMs. There are no
  hard-coded Wendy UUIDs anywhere in `ble_darwin.*`, `ble_linux.go`, or `ble_windows.go`.
* The CoreBluetooth backend is a genuinely reusable macOS BLE central for CLI programs, and the
  run-loop/threading work it does is the hard part of that problem.
* `l2capNetConn` is a general-purpose L2CAP-to-`net.Conn` adapter, exported as `NewL2CAPStream`.

**Wendy-specific** — everything above the transport:

* `lite_client.go` — hard-coded Wendy Lite service/characteristic UUIDs and command/status bytes.
* `lite_info.go` — likewise, for the Wendy Lite info service.
* `agent_client.go` — protobuf framing, `agentpb` types, its own reply timeout.
* `NewClientTLSConfig` — depends on `shared/certs` (Wendy PKI, pin store).
* `ConnectLite`/`ConnectAgent` take `*models.BluetoothDevice`, a Wendy discovery type.

The split is clean except for one wart: `DefaultL2CAPPSM` (128) lives in `conn.go`, so a Wendy
constant sits in the otherwise-generic transport file. It is shared by both protocol clients, which
is why it ended up there; a generic fork should drop it.

To use it for a generic BLE app you would need to:

1. **Move it out of `internal/`.** As `go/internal/cli/ble` it is unimportable outside this module.
2. **Drop or replace the `models` and `certs` dependencies**, and drop `DefaultL2CAPPSM`.
   (`NewL2CAPStream` is already exported.)
3. **Bring your own discovery** — this package cannot find devices, and the address format it needs
   differs by platform, so callers can't be fully platform-agnostic.
4. **Implement GATT on Linux** (BlueZ D-Bus or raw ATT) — as it stands, any GATT-based app works on
   macOS only. A pure-L2CAP app already works on macOS *and* Linux.
5. **Write a Windows backend** if you need it.
6. **Fix the concurrency and notification-demux limitations** above for anything beyond a single
   sequential request/response flow.

For a general-purpose Go BLE library, an off-the-shelf option (e.g. `tinygo-org/bluetooth`,
`go-ble/ble`) is the pragmatic choice. What this package has that they generally don't is a working
**BLE L2CAP connection-oriented channel** client on both macOS and Linux, with TLS running over
it — that is the piece worth lifting.
