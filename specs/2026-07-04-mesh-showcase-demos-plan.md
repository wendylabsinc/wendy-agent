# MeshBeacon + MeshCounter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build two new visual example apps — MeshBeacon (one-to-many pub/sub broadcast) and MeshCounter (shared state kept in sync) — that showcase mesh capabilities not covered by the existing HelloMesh/RemoteCam examples, per `specs/2026-07-04-mesh-showcase-demos-design.md`.

**Architecture:** A new shared Swift library, `MeshFanout` (in `wendy-app-sdk`, alongside `WendyCanvas`/`WendyKMSDRM`), provides peer-list parsing, a small length-prefixed wire format, and a symmetric listen+broadcast networking class. Two new executable targets under `wendy-app-sdk/probe/` (`MeshBeacon`, `MeshCounter`) build on it plus the same `WendyKMSDRM`/`WendyCanvas`/`WendyTextKit`/`WendyKMSInput` stack RemoteCamViewer already proved on real hardware.

**Tech Stack:** Swift 6 (`swift-tools-version: 6.0`), Swift Testing (`import Testing`, `@Test`, `#expect`) for unit tests, raw POSIX sockets (Glibc/Darwin) for mesh networking, WendyOS's own KMS/Canvas/evdev stack for display and touch (no SwiftCrossUI — `probe/` never depends on it).

## Global Constraints

- Repo: `/Users/joannisorlandos/git/wendy/wendy-app-sdk` (Swift SDK + `probe/` demo apps). Deployed apps live under `Examples/` in a *different* repo (`wendyos-mesh`) for RemoteCam/HelloMesh, but MeshBeacon/MeshCounter — like RemoteCamViewer before them — need the KMS/Canvas/touch stack that only exists in `wendy-app-sdk`, so they live under `wendy-app-sdk/probe/` instead, following RemoteCamViewer's exact precedent.
- `probe/Package.swift` depends on the parent package by path (`.package(path: "..")`) and must never gain a dependency on `WendyUI`/`SwiftCrossUI` — that's what keeps it cross-compiling cleanly for `wendy run`.
- Env var convention (from `Examples/HelloMesh/client/app.py`, already deployed and proven): `MESH_PEERS` — comma-separated peer asset IDs; `MESH_SELF` — this device's own asset ID, excluded from its own peer list.
- Mesh network entitlement shape (from `Examples/HelloMesh/client/wendy.json` and today's `jo/mesh-foundation` fixes, now working end-to-end): `{"type": "network", "mode": "mesh", "serviceCIDR": "10.99.0.0/16", "ports": [{"host": N, "container": N}]}`.
- Two physical test devices: `wendyos-pi4.local` and `wendyos-pi5-nvme.local`, reached via the `wendy` CLI built from `/Users/joannisorlandos/git/wendy/wendyos-mesh/go` (`export PATH="/Users/joannisorlandos/git/wendy/wendyos-mesh/go/bin:$PATH"`). Their asset IDs change on re-enrollment — confirm current values with `wendy cloud discover --json` immediately before any hardware-verify task; do not trust a value from an earlier task.
- Multi-service `wendy.json` apps always build via Docker regardless of `language`/`--build-type` hints (confirmed CLI behavior this session) — every new demo needs a hand-written `Dockerfile`, matching RemoteCamViewer's.
- `wendy.json`'s `context` field cannot contain `..` — the Dockerfile and wendy.json for an app built from `wendy-app-sdk` must live at the repo root (context `.`), exactly like RemoteCamViewer's.
- Redeploying any mesh-entitled app on this branch may need 1-2 retries the first time after a fresh container create (a known, harmless "duplicate allocation" self-heal — see PR #1336's description) — do not treat the first `CNI ADD failed` after a fresh deploy as a real failure; retry once via `wendy device apps remove <appId> --force` + `wendy run` before investigating further.

---

## File Structure

**New shared library** (`wendy-app-sdk`):
- `Sources/MeshFanout/PeerList.swift` — `parseMeshPeers` (pure, tested)
- `Sources/MeshFanout/WireFrame.swift` — `encodeFrameHeader`/`decodeFrameHeader` (pure, tested) + `sendFrame`/`readFrame` (thin I/O wrappers)
- `Sources/MeshFanout/StableIndex.swift` — `stablePaletteIndex` (pure, tested)
- `Sources/MeshFanout/MeshSocket.swift` — `dialMeshHost`/`sendAll`/`recvExact` (low-level I/O, adapted from RemoteCamViewer's proven `RemoteCamProtocol.swift`)
- `Sources/MeshFanout/MeshFanout.swift` — the public `MeshFanout` class (listen + broadcast)
- `Tests/MeshFanoutTests/PeerListTests.swift`, `WireFrameTests.swift`, `StableIndexTests.swift`
- `Package.swift` — modified: add `MeshFanout` product/target + `MeshFanoutTests` test target

**New demo apps** (`wendy-app-sdk/probe`):
- `probe/Sources/MeshBeacon/main.swift`
- `probe/meshbeacon.wendy.json`
- `probe/Package.swift` — modified: add `MeshBeacon` executable target
- `Dockerfile.meshbeacon` (repo root)
- `probe/Sources/MeshCounter/main.swift`
- `probe/meshcounter.wendy.json`
- `probe/Package.swift` — modified: add `MeshCounter` executable target
- `Dockerfile.meshcounter` (repo root)

---

### Task 1: MeshFanout library — peer parsing, wire framing, palette index

**Files:**
- Create: `Sources/MeshFanout/PeerList.swift`
- Create: `Sources/MeshFanout/WireFrame.swift`
- Create: `Sources/MeshFanout/StableIndex.swift`
- Create: `Tests/MeshFanoutTests/PeerListTests.swift`
- Create: `Tests/MeshFanoutTests/WireFrameTests.swift`
- Create: `Tests/MeshFanoutTests/StableIndexTests.swift`
- Modify: `Package.swift`

**Interfaces:**
- Produces: `public func parseMeshPeers(_ raw: String, excluding selfID: String = "") -> [String]`
- Produces: `func encodeFrameHeader(type: UInt8, payloadLength: Int) -> [UInt8]`
- Produces: `func decodeFrameHeader(_ bytes: [UInt8], maxPayloadLength: Int = 1024) -> (type: UInt8, length: Int)?`
- Produces: `public func stablePaletteIndex(for id: String, paletteSize: Int) -> Int`
- Consumed by: Task 2 (`MeshFanout.swift` uses `encodeFrameHeader`/`decodeFrameHeader` indirectly via `sendFrame`/`readFrame`, which this task also defines), Task 3/5 (`MeshBeacon`/`MeshCounter` main.swift use `parseMeshPeers`; `MeshBeacon` also uses `stablePaletteIndex`)

- [ ] **Step 1: Write the failing tests for `parseMeshPeers`**

Create `Tests/MeshFanoutTests/PeerListTests.swift`:

```swift
import Testing
@testable import MeshFanout

@Test func parsesCommaSeparatedIDs() {
    #expect(parseMeshPeers("270,271,272") == [
        "device-270.cloud.wendy.dev",
        "device-271.cloud.wendy.dev",
        "device-272.cloud.wendy.dev",
    ])
}

@Test func skipsBlankEntriesAndWhitespace() {
    #expect(parseMeshPeers(" 270 ,,271,") == [
        "device-270.cloud.wendy.dev",
        "device-271.cloud.wendy.dev",
    ])
}

@Test func emptyStringProducesNoPeers() {
    #expect(parseMeshPeers("") == [])
}

@Test func excludesSelfID() {
    #expect(parseMeshPeers("270,271,272", excluding: "271") == [
        "device-270.cloud.wendy.dev",
        "device-272.cloud.wendy.dev",
    ])
}
```

- [ ] **Step 2: Write the failing tests for `encodeFrameHeader`/`decodeFrameHeader`**

Create `Tests/MeshFanoutTests/WireFrameTests.swift`:

```swift
import Testing
@testable import MeshFanout

@Test func encodesTypeAndBigEndianLength() {
    let header = encodeFrameHeader(type: 0x01, payloadLength: 5)
    #expect(header == [0x01, 0x00, 0x00, 0x00, 0x05])
}

@Test func encodesLargerLengthCorrectly() {
    let header = encodeFrameHeader(type: 0x10, payloadLength: 300) // 0x12C
    #expect(header == [0x10, 0x00, 0x00, 0x01, 0x2C])
}

@Test func decodeRoundTripsWithEncode() {
    let header = encodeFrameHeader(type: 0x02, payloadLength: 42)
    let decoded = decodeFrameHeader(header)
    #expect(decoded?.type == 0x02)
    #expect(decoded?.length == 42)
}

@Test func decodeRejectsWrongByteCount() {
    #expect(decodeFrameHeader([0x01, 0x00, 0x00]) == nil)
}

@Test func decodeRejectsOversizedLength() {
    let header = encodeFrameHeader(type: 0x01, payloadLength: 2000)
    #expect(decodeFrameHeader(header, maxPayloadLength: 1024) == nil)
}
```

- [ ] **Step 3: Write the failing tests for `stablePaletteIndex`**

Create `Tests/MeshFanoutTests/StableIndexTests.swift`:

```swift
import Testing
@testable import MeshFanout

@Test func indexIsWithinBounds() {
    for id in ["270", "271", "abc", ""] {
        let idx = stablePaletteIndex(for: id, paletteSize: 6)
        #expect(idx >= 0 && idx < 6)
    }
}

@Test func sameIDAlwaysProducesSameIndexWithinOneProcess() {
    let a = stablePaletteIndex(for: "270", paletteSize: 6)
    let b = stablePaletteIndex(for: "270", paletteSize: 6)
    #expect(a == b)
}

@Test func zeroPaletteSizeReturnsZero() {
    #expect(stablePaletteIndex(for: "270", paletteSize: 0) == 0)
}
```

- [ ] **Step 4: Add the `MeshFanout` target and test target to `Package.swift` so the tests can even compile**

In `Package.swift`, add to the `products` array (after the `WendyKMSBackend` library entry):

```swift
        .library(name: "MeshFanout", targets: ["MeshFanout"]),
```

Add to the `targets` array (after the `WendyKMSInput` target, before `WendyKMSBackend`):

```swift
        .target(name: "MeshFanout"),
```

Add to the `targets` array (after the existing `.testTarget` entries, at the end):

```swift
        .testTarget(name: "MeshFanoutTests", dependencies: ["MeshFanout"]),
```

- [ ] **Step 5: Run the tests to verify they fail (missing implementation, not a build error unrelated to this task)**

Run: `cd /Users/joannisorlandos/git/wendy/wendy-app-sdk && swift test --filter MeshFanoutTests`
Expected: build failure — `parseMeshPeers`, `encodeFrameHeader`, `decodeFrameHeader`, `stablePaletteIndex` are undefined. This confirms the test files and target wiring are correct; the next step adds the implementations they need.

- [ ] **Step 6: Implement `PeerList.swift`**

Create `Sources/MeshFanout/PeerList.swift`:

```swift
import Foundation

/// Parses a MESH_PEERS-style env var value ("comma-separated asset IDs, e.g.
/// "270,271,272") into mesh hostnames ("device-270.cloud.wendy.dev", ...).
/// Blank entries (an empty string, a trailing comma, or all-whitespace) are
/// skipped rather than turned into a malformed "device-.cloud.wendy.dev"
/// hostname. `selfID`, if non-empty, is excluded from the result so a
/// device's own asset ID in a shared MESH_PEERS value (the same list handed
/// to every fleet member, matching Examples/HelloMesh's convention) doesn't
/// make it dial itself.
public func parseMeshPeers(_ raw: String, excluding selfID: String = "") -> [String] {
    let trimmedSelf = selfID.trimmingCharacters(in: .whitespaces)
    return raw.split(separator: ",")
        .map { $0.trimmingCharacters(in: .whitespaces) }
        .filter { !$0.isEmpty && $0 != trimmedSelf }
        .map { "device-\($0).cloud.wendy.dev" }
}
```

- [ ] **Step 7: Implement `WireFrame.swift` (header encode/decode only — `sendFrame`/`readFrame` come in Task 2 alongside the socket I/O they need)**

Create `Sources/MeshFanout/WireFrame.swift`:

```swift
/// Builds the 5-byte frame header: [type][4-byte big-endian length]. Pure,
/// no I/O — split out from `sendFrame` (Task 2) purely so the
/// length-encoding math is unit-testable without a live socket.
func encodeFrameHeader(type: UInt8, payloadLength: Int) -> [UInt8] {
    var header = [UInt8]()
    header.reserveCapacity(5)
    header.append(type)
    let len = UInt32(payloadLength).bigEndian
    withUnsafeBytes(of: len) { header.append(contentsOf: $0) }
    return header
}

/// Parses a 5-byte frame header. Returns nil if `bytes.count != 5` or the
/// decoded length exceeds `maxPayloadLength` — guards a corrupted length
/// from driving an unbounded allocation on the read side. Demo payloads
/// (a color byte triple, or nothing at all) are tiny, so the default cap is
/// generous, not tight.
func decodeFrameHeader(_ bytes: [UInt8], maxPayloadLength: Int = 1024) -> (type: UInt8, length: Int)? {
    guard bytes.count == 5 else { return nil }
    let length = (UInt32(bytes[1]) << 24) | (UInt32(bytes[2]) << 16) | (UInt32(bytes[3]) << 8) | UInt32(bytes[4])
    guard length <= UInt32(maxPayloadLength) else { return nil }
    return (bytes[0], Int(length))
}
```

- [ ] **Step 8: Implement `StableIndex.swift`**

Create `Sources/MeshFanout/StableIndex.swift`:

```swift
/// Deterministically maps `id` to an index in `0..<paletteSize` — the same
/// `id` always yields the same index within one process, which is all a
/// demo "pick this device's color" needs. Uses Swift's built-in `Hasher`
/// rather than a hand-rolled hash; the result is stable for the lifetime of
/// one run but NOT guaranteed stable across processes (`Hasher` is seeded
/// randomly per process) — fine here, since each device only ever needs its
/// OWN index to stay the same for the duration of one demo session, not to
/// match some fixed external scheme shared with other devices.
public func stablePaletteIndex(for id: String, paletteSize: Int) -> Int {
    guard paletteSize > 0 else { return 0 }
    var hasher = Hasher()
    hasher.combine(id)
    let hash = hasher.finalize()
    return Int(UInt(bitPattern: hash) % UInt(paletteSize))
}
```

- [ ] **Step 9: Run the tests to verify they pass**

Run: `cd /Users/joannisorlandos/git/wendy/wendy-app-sdk && swift test --filter MeshFanoutTests`
Expected: PASS — all `PeerListTests`, `WireFrameTests`, `StableIndexTests` cases green.

- [ ] **Step 10: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git add Package.swift Sources/MeshFanout Tests/MeshFanoutTests
git commit -m "feat(MeshFanout): peer-list parsing, wire frame header, palette index"
```

---

### Task 2: MeshFanout library — socket I/O and the `MeshFanout` networking class

**Files:**
- Create: `Sources/MeshFanout/MeshSocket.swift`
- Modify: `Sources/MeshFanout/WireFrame.swift` (add `sendFrame`/`readFrame`)
- Create: `Sources/MeshFanout/MeshFanout.swift`

**Interfaces:**
- Consumes: `encodeFrameHeader`/`decodeFrameHeader` (Task 1)
- Produces: `public final class MeshFanout` with `public init(peers: [String], listenPort: UInt16, onMessage: @escaping (UInt8, [UInt8]) -> Void)`, `public func start() throws`, `public func broadcast(type: UInt8, payload: [UInt8] = [])`
- Produces: `public enum MeshFanoutError: Error, CustomStringConvertible` (case `listenFailed(step: String, errno: Int32)`)
- Consumed by: Task 3 (`MeshBeacon/main.swift`), Task 5 (`MeshCounter/main.swift`)

This task is socket I/O — not practically unit-testable without a live network stack (RemoteCamViewer's equivalent code, which this closely follows, has never had unit tests either; it's verified by hardware use, which Tasks 4 and 6 do for these new demos). No TDD steps here; write the implementation directly, verify it builds, and let the hardware-verify tasks be the real test.

- [ ] **Step 1: Implement `MeshSocket.swift`**

Create `Sources/MeshFanout/MeshSocket.swift`:

```swift
#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#endif
import Foundation

/// Errors from the low-level mesh TCP dial. Mirrors
/// `probe/Sources/RemoteCamViewer/RemoteCamProtocol.swift`'s
/// `RemoteCamError` (same repo, different target) — duplicated rather than
/// shared since RemoteCamViewer doesn't expose these as a library product.
enum MeshSocketError: Error, CustomStringConvertible {
    case resolveFailed(host: String, code: Int32)
    case connectFailed(host: String, port: UInt16, errno: Int32)

    var description: String {
        switch self {
        case .resolveFailed(let host, let code):
            return "getaddrinfo(\(host)) failed (code \(code))"
        case .connectFailed(let host, let port, let errno):
            return "connect(\(host):\(port)) failed (errno \(errno))"
        }
    }
}

/// Resolves `host` via getaddrinfo and connects to the first address that
/// accepts, trying every address family the resolver returns. Blocking;
/// call off the main thread.
func dialMeshHost(_ host: String, port: UInt16) throws -> Int32 {
    var hints = addrinfo()
    hints.ai_family = AF_UNSPEC
    #if canImport(Glibc)
        hints.ai_socktype = Int32(SOCK_STREAM.rawValue)
    #else
        hints.ai_socktype = SOCK_STREAM
    #endif
    var result: UnsafeMutablePointer<addrinfo>?
    let rc = getaddrinfo(host, String(port), &hints, &result)
    guard rc == 0, let first = result else {
        throw MeshSocketError.resolveFailed(host: host, code: rc)
    }
    defer { freeaddrinfo(result) }

    var lastErrno: Int32 = ENOENT
    var cursor: UnsafeMutablePointer<addrinfo>? = first
    while let addr = cursor {
        let fd = socket(addr.pointee.ai_family, addr.pointee.ai_socktype, addr.pointee.ai_protocol)
        if fd >= 0 {
            if connect(fd, addr.pointee.ai_addr, addr.pointee.ai_addrlen) == 0 {
                return fd
            }
            lastErrno = errno
            close(fd)
        } else {
            lastErrno = errno
        }
        cursor = addr.pointee.ai_next
    }
    throw MeshSocketError.connectFailed(host: host, port: port, errno: lastErrno)
}

/// Writes every byte of `bytes` to `fd`, retrying on EINTR and short writes.
@discardableResult
func sendAll(_ fd: Int32, _ bytes: [UInt8]) -> Bool {
    guard !bytes.isEmpty else { return true }
    return bytes.withUnsafeBytes { buf -> Bool in
        var offset = 0
        while offset < buf.count {
            let n = send(fd, buf.baseAddress!.advanced(by: offset), buf.count - offset, 0)
            if n > 0 { offset += n; continue }
            if n < 0, errno == EINTR { continue }
            return false
        }
        return true
    }
}

/// Reads exactly `count` bytes from `fd`, retrying on EINTR. Returns nil on
/// EOF or a fatal error.
func recvExact(_ fd: Int32, count: Int) -> [UInt8]? {
    guard count > 0 else { return [] }
    var buffer = [UInt8](repeating: 0, count: count)
    let ok = buffer.withUnsafeMutableBytes { buf -> Bool in
        var offset = 0
        while offset < count {
            let n = recv(fd, buf.baseAddress!.advanced(by: offset), count - offset, 0)
            if n > 0 { offset += n; continue }
            if n < 0, errno == EINTR { continue }
            return false
        }
        return true
    }
    return ok ? buffer : nil
}
```

- [ ] **Step 2: Add `sendFrame`/`readFrame` to `WireFrame.swift`**

Append to `Sources/MeshFanout/WireFrame.swift`:

```swift
/// Sends one framed message: `[type][big-endian uint32 length][payload]`.
@discardableResult
func sendFrame(_ fd: Int32, type: UInt8, payload: [UInt8] = []) -> Bool {
    let header = encodeFrameHeader(type: type, payloadLength: payload.count)
    return sendAll(fd, header) && sendAll(fd, payload)
}

/// Reads one framed message (5-byte header + payload). Blocking. Returns
/// nil on disconnect or a malformed/oversized length.
func readFrame(_ fd: Int32) -> (type: UInt8, payload: [UInt8])? {
    guard let header = recvExact(fd, count: 5),
        let (type, length) = decodeFrameHeader(header)
    else { return nil }
    guard let payload = recvExact(fd, count: length) else { return nil }
    return (type, payload)
}
```

- [ ] **Step 3: Implement `MeshFanout.swift`**

Create `Sources/MeshFanout/MeshFanout.swift`:

```swift
#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#endif
import Foundation

/// Errors starting a `MeshFanout` listener.
public enum MeshFanoutError: Error, CustomStringConvertible {
    case listenFailed(step: String, errno: Int32)
    public var description: String {
        switch self {
        case .listenFailed(let step, let errno):
            return "MeshFanout: \(step) failed (errno \(errno))"
        }
    }
}

/// A symmetric mesh peer: listens for incoming single-frame messages from
/// peers, and can broadcast a message to every configured peer. Every demo
/// built on this (MeshBeacon, MeshCounter) runs the identical listen+
/// broadcast pair — only the message type/payload and what `onMessage` does
/// with it differ.
///
/// Both directions are "one frame per connection, then close" — there is no
/// persistent peer-to-peer session. This keeps the demo apps' failure modes
/// simple: a peer that's unreachable or slow only ever affects the one
/// broadcast attempt to it, never blocks the listener or other peers.
public final class MeshFanout: @unchecked Sendable {
    public let peers: [String]
    private let listenPort: UInt16
    private let onMessage: (UInt8, [UInt8]) -> Void

    /// - Parameters:
    ///   - peers: mesh hostnames to broadcast to (see `parseMeshPeers`).
    ///   - listenPort: the port this device listens on AND the port peers
    ///     are dialed on — demos use one fixed port for both directions,
    ///     matching the `ports` entitlement's host==container convention.
    ///   - onMessage: called on a background thread for every inbound
    ///     message. Must not touch KMS/Canvas directly — hand data off to
    ///     the render loop via a lock-guarded field, the same pattern
    ///     RemoteCamSession uses (see each demo's main.swift).
    public init(peers: [String], listenPort: UInt16, onMessage: @escaping (UInt8, [UInt8]) -> Void) {
        self.peers = peers
        self.listenPort = listenPort
        self.onMessage = onMessage
    }

    /// Starts listening on a background thread. Throws if the listener
    /// itself can't be set up (bind/listen failure); accept-loop errors
    /// after that are per-connection and never surfaced here.
    public func start() throws {
        let fd = socket(AF_INET6, Int32(SOCK_STREAM.rawValue), 0)
        guard fd >= 0 else { throw MeshFanoutError.listenFailed(step: "socket", errno: errno) }

        var yes: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &yes, socklen_t(MemoryLayout<Int32>.size))
        // Accept both IPv4-mapped and native IPv6 connections on one socket,
        // since mesh dials may arrive as either depending on resolver
        // behavior (dialMeshHost tries every family getaddrinfo returns).
        var v6Only: Int32 = 0
        setsockopt(fd, Int32(IPPROTO_IPV6), IPV6_V6ONLY, &v6Only, socklen_t(MemoryLayout<Int32>.size))

        var addr = sockaddr_in6()
        addr.sin6_family = sa_family_t(AF_INET6)
        addr.sin6_port = listenPort.bigEndian
        addr.sin6_addr = in6addr_any
        let bindResult = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                bind(fd, sa, socklen_t(MemoryLayout<sockaddr_in6>.size))
            }
        }
        guard bindResult == 0 else {
            let e = errno
            close(fd)
            throw MeshFanoutError.listenFailed(step: "bind", errno: e)
        }
        guard listen(fd, 16) == 0 else {
            let e = errno
            close(fd)
            throw MeshFanoutError.listenFailed(step: "listen", errno: e)
        }

        let thread = Thread { [weak self] in self?.acceptLoop(fd) }
        thread.name = "MeshFanout.accept"
        thread.start()
    }

    private func acceptLoop(_ fd: Int32) {
        while true {
            let client = accept(fd, nil, nil)
            guard client >= 0 else { continue }  // transient accept error; keep serving
            Thread.detachNewThread { [weak self] in
                defer { close(client) }
                guard let (type, payload) = readFrame(client) else { return }
                self?.onMessage(type, payload)
            }
        }
    }

    /// Fire-and-forget: connects to every peer concurrently and sends one
    /// frame each, on its own thread per peer. Returns immediately — a slow
    /// or unreachable peer only ever delays/drops its own delivery, never
    /// the caller or any other peer's delivery.
    public func broadcast(type: UInt8, payload: [UInt8] = []) {
        for host in peers {
            let port = listenPort
            Thread.detachNewThread {
                guard let fd = try? dialMeshHost(host, port: port) else { return }
                defer { close(fd) }
                sendFrame(fd, type: type, payload: payload)
            }
        }
    }
}
```

- [ ] **Step 4: Build the whole package to verify it compiles (macOS dev build — `MeshFanout` is pure Foundation/POSIX, unlike `WendyKMSDRM`/`WendyKMSInput` it needs no Linux-only headers, so this must succeed here, not just cross-compiled)**

Run: `cd /Users/joannisorlandos/git/wendy/wendy-app-sdk && swift build --target MeshFanout`
Expected: `Build complete!`

- [ ] **Step 5: Run the full MeshFanout test suite once more to confirm nothing in this task broke Task 1's tests**

Run: `cd /Users/joannisorlandos/git/wendy/wendy-app-sdk && swift test --filter MeshFanoutTests`
Expected: PASS (same tests as Task 1, still green)

- [ ] **Step 6: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git add Sources/MeshFanout
git commit -m "feat(MeshFanout): socket I/O + symmetric listen/broadcast networking"
```

---

### Task 3: MeshBeacon app

**Files:**
- Create: `probe/Sources/MeshBeacon/main.swift`
- Create: `probe/meshbeacon.wendy.json`
- Create: `Dockerfile.meshbeacon`
- Modify: `probe/Package.swift`

**Interfaces:**
- Consumes: `parseMeshPeers`, `stablePaletteIndex`, `MeshFanout` (Tasks 1-2); `Color`, `Canvas`, `FontFace.bundled()` (existing `WendyCanvas`/`WendyTextKit`); `wendy_kms_open`/`wendy_kms_close`/`wendy_kms_present`/`WendyKMSDisplay` (existing `WendyKMSDRM`); `wendy_input_open`/`wendy_input_poll`/`wendy_input_close`/`WendyInputDevice`/`WendyTouchEvent`/`WENDY_TOUCH_DOWN` (existing `WendyKMSInput`) — all verified against their actual header/source signatures during planning; none are guessed.
- Produces: the `MeshBeacon` executable target and its deploy config; nothing downstream depends on this task's internals.

- [ ] **Step 1: Add the `MeshBeacon` executable target to `probe/Package.swift`**

In `probe/Package.swift`, add to the `targets` array (after the `RemoteCamViewer` target):

```swift
        .executableTarget(
            name: "MeshBeacon",
            dependencies: [
                .product(name: "WendyKMSDRM", package: "wendy-app-sdk"),
                .product(name: "WendyCanvas", package: "wendy-app-sdk"),
                .product(name: "WendyTextKit", package: "wendy-app-sdk"),
                .product(name: "WendyKMSInput", package: "wendy-app-sdk"),
                .product(name: "MeshFanout", package: "wendy-app-sdk"),
            ]
        ),
```

- [ ] **Step 2: Write `probe/Sources/MeshBeacon/main.swift`**

```swift
import Foundation
import WendyKMSDRM
import WendyCanvas
import WendyTextKit
import WendyKMSInput
import MeshFanout

#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#endif

// MeshBeacon: every device on the mesh runs this identical app. Tapping
// anywhere on the screen broadcasts a "beacon" (this device's own color) to
// every peer in MESH_PEERS; every device that receives one — including the
// sender itself, via an immediate local flash — fills its screen with that
// color for one second. Demonstrates one-to-many pub/sub fan-out over the
// mesh data plane (contrast with RemoteCam's 1:1 unicast stream).
//
// The display/touch stack below is the same one RemoteCamViewer already
// proved on real hardware (wendy-app-sdk/probe/Sources/RemoteCamViewer); the
// MeshFanout networking is new but built on the exact dial/frame primitives
// RemoteCamViewer's own RemoteCamProtocol.swift already proved.

let listenPort: UInt16 = 9091
let beaconFlashDuration: TimeInterval = 1.0
let beaconFrameType: UInt8 = 0x01

extension Color {
    var r: UInt8 { UInt8((value >> 16) & 0xFF) }
    var g: UInt8 { UInt8((value >> 8) & 0xFF) }
    var b: UInt8 { UInt8(value & 0xFF) }
}

let palette: [Color] = [
    Color(r: 0xE5, g: 0x3E, b: 0x3E),  // red
    Color(r: 0x3E, g: 0x7A, b: 0xE5),  // blue
    Color(r: 0x3E, g: 0xC9, b: 0x5D),  // green
    Color(r: 0xE5, g: 0xC4, b: 0x3E),  // yellow
    Color(r: 0xB0, g: 0x3E, b: 0xE5),  // purple
    Color(r: 0xE5, g: 0x8A, b: 0x3E),  // orange
]

func log(_ message: String) {
    print("[meshbeacon] \(message)")
}

let selfID = ProcessInfo.processInfo.environment["MESH_SELF"] ?? ""
let peersRaw = ProcessInfo.processInfo.environment["MESH_PEERS"] ?? ""
let peers = parseMeshPeers(peersRaw, excluding: selfID)
let selfColor = palette[stablePaletteIndex(for: selfID, paletteSize: palette.count)]

log("self=\(selfID.isEmpty ? "(unset)" : selfID) peers=\(peers)")

/// Shared between the mesh listener thread and the main render/input loop:
/// the listener only ever writes `pending`, the main loop only ever
/// reads-and-clears it via `takePending` — guarded by a lock since they run
/// on different threads, the same hand-off shape RemoteCamSession uses for
/// its own background-thread-to-main-loop updates.
final class FlashState: @unchecked Sendable {
    private let lock = NSLock()
    private var pending: Color?

    func setPending(_ color: Color) {
        lock.lock()
        pending = color
        lock.unlock()
    }

    func takePending() -> Color? {
        lock.lock()
        defer { lock.unlock() }
        let value = pending
        pending = nil
        return value
    }
}

let flashState = FlashState()

let fanout = MeshFanout(peers: peers, listenPort: listenPort) { type, payload in
    guard type == beaconFrameType, payload.count == 3 else { return }
    flashState.setPending(Color(r: payload[0], g: payload[1], b: payload[2]))
}

do {
    try fanout.start()
    log("listening on port \(listenPort)")
} catch {
    log("failed to start listener: \(error)")
    wendy_kms_flush_stdout()
    exit(1)
}

let kmsPath = ProcessInfo.processInfo.environment["WENDY_KMS_DEVICE"] ?? "/dev/dri/card0"
log("opening \(kmsPath) (stop sh.wendy.shell first so KMS is free)")

var display = WendyKMSDisplay()
var errBuf = [CChar](repeating: 0, count: 256)
guard wendy_kms_open(kmsPath, &display, &errBuf, 256) == 0 else {
    let msg = errBuf.withUnsafeBytes { String(bytes: $0.prefix(while: { $0 != 0 }), encoding: .utf8) ?? "" }
    log("wendy_kms_open failed: \(msg)")
    wendy_kms_flush_stdout()
    exit(1)
}
guard let pixels = display.pixels else {
    log("no framebuffer mapped")
    wendy_kms_close(&display)
    wendy_kms_flush_stdout()
    exit(1)
}
let screenW = Int(display.width)
let screenH = Int(display.height)
let stride = Int(display.stride)
log("display \(screenW)x\(screenH) stride=\(stride)")
wendy_kms_flush_stdout()

let canvas = Canvas(base: pixels, width: screenW, height: screenH, stride: stride)
let font = FontFace.bundled()
let idleBackground = Color(r: 0x20, g: 0x20, b: 0x24)
let hintColor = Color(r: 0xE0, g: 0xE0, b: 0xE0)

func drawIdle() {
    canvas.fill(idleBackground)
    canvas.fillRect(x: 24, y: 24, w: 48, h: 48, selfColor)  // this device's own color swatch
    canvas.drawText(
        "tap anywhere to send a beacon", x: 24, baseline: screenH / 2, pxSize: 32, color: hintColor, font: font)
}

var inputDevice = WendyInputDevice()
var inputOpen: Bool = {
    var err = [CChar](repeating: 0, count: 1024)
    guard wendy_input_open(&inputDevice, &err, 1024) == 0 else {
        let msg = err.withUnsafeBytes { String(bytes: $0.prefix(while: { $0 != 0 }), encoding: .utf8) ?? "" }
        log("touch input unavailable, will keep retrying: \(msg)")
        return false
    }
    log("touch input active")
    return true
}()

drawIdle()
wendy_kms_present(&display)
log("ready; tap anywhere to send a beacon")
wendy_kms_flush_stdout()

var flashUntil: Date?
var touchRetryTicks = 0
let touchRetryEveryTicks = 125  // ~2s at 16ms/tick, matching RemoteCamViewer's rescan cadence

func sendBeacon() {
    log("sending beacon (color=0x\(String(selfColor.value, radix: 16)))")
    fanout.broadcast(type: beaconFrameType, payload: [selfColor.r, selfColor.g, selfColor.b])
    flashState.setPending(selfColor)  // immediate local feedback; don't wait on the network
}

while true {
    if !inputOpen {
        touchRetryTicks += 1
        if touchRetryTicks >= touchRetryEveryTicks {
            touchRetryTicks = 0
            var err = [CChar](repeating: 0, count: 1024)
            inputOpen = wendy_input_open(&inputDevice, &err, 1024) == 0
            if inputOpen { log("touch input active") }
        }
    } else {
        var raw = [WendyTouchEvent](repeating: WendyTouchEvent(), count: 32)
        let n = wendy_input_poll(&inputDevice, &raw, 32)
        if n < 0 {
            log("touch device lost; will keep watching for it to return")
            wendy_input_close(&inputDevice)
            inputOpen = false
        } else if n > 0 {
            for i in 0..<Int(n) where raw[i].kind == Int32(WENDY_TOUCH_DOWN.rawValue) {
                sendBeacon()
            }
        }
    }

    if let color = flashState.takePending() {
        canvas.fill(color)
        wendy_kms_present(&display)
        flashUntil = Date().addingTimeInterval(beaconFlashDuration)
    } else if let until = flashUntil, Date() >= until {
        flashUntil = nil
        drawIdle()
        wendy_kms_present(&display)
    }

    usleep(16_000)
}
```

- [ ] **Step 3: Write `probe/meshbeacon.wendy.json`**

```json
{
    "appId": "sh.wendy.examples.meshbeacon",
    "version": "0.1.0",
    "platform": "linux",
    "isolation": "isolated",
    "services": {
        "meshbeacon": {
            "context": ".",
            "entitlements": [
                { "type": "display" },
                { "type": "input" },
                {
                    "type": "network",
                    "mode": "mesh",
                    "serviceCIDR": "10.99.0.0/16",
                    "ports": [{ "host": 9091, "container": 9091 }]
                }
            ],
            "env": {
                "MESH_PEERS": "${MESH_PEERS}",
                "MESH_SELF": "${MESH_SELF}"
            }
        }
    }
}
```

- [ ] **Step 4: Write `Dockerfile.meshbeacon`**

```dockerfile
# MeshBeacon has to be built via a Dockerfile rather than the Swift
# buildpack, and built from the wendy-app-sdk repo root rather than
# probe/ alone — see wendy-app-sdk/Dockerfile.remotecamviewer for the full
# explanation; this file is that same shape with only the --product name
# and output binary name changed.
FROM swift:6.1-noble AS build
WORKDIR /wendy-app-sdk
COPY Package.swift Package.resolved* ./
COPY Sources ./Sources
COPY probe ./probe
WORKDIR /wendy-app-sdk/probe
RUN swift build -c release --product MeshBeacon

FROM swift:6.1-noble-slim
WORKDIR /app
COPY --from=build /wendy-app-sdk/probe/.build/release/MeshBeacon ./MeshBeacon
CMD ["./MeshBeacon"]
```

- [ ] **Step 5: Build for macOS to catch any type/API errors before cross-compiling (fast local feedback loop — this is NOT a hardware test, just a compile check; the target's platform-specific KMS/input code has Linux and non-Linux code paths already handled by `WendyKMSDRM`/`WendyKMSInput` themselves)**

Run: `cd /Users/joannisorlandos/git/wendy/wendy-app-sdk && swift build --target MeshBeacon`
Expected: `Build complete!`

- [ ] **Step 6: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git add probe/Package.swift probe/Sources/MeshBeacon probe/meshbeacon.wendy.json Dockerfile.meshbeacon
git commit -m "feat(MeshBeacon): pub/sub mesh broadcast demo app"
```

---

### Task 4: MeshBeacon hardware verification

**Files:** none (deploy + manual test only)

**Interfaces:** none — this task validates Task 3's deliverable end-to-end.

- [ ] **Step 1: Confirm current asset IDs for both devices**

```bash
export PATH="/Users/joannisorlandos/git/wendy/wendyos-mesh/go/bin:$PATH"
wendy cloud discover --json
```

Expected: a JSON array with an entry for `pi4` and `pi5-nvme`. Note their `id` values — call them `<PI4_ID>` and `<PI5_ID>` below (do not assume they match any value from an earlier session; asset IDs change on re-enrollment).

- [ ] **Step 2: Deploy to pi4**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
cp probe/meshbeacon.wendy.json wendy.json
cp Dockerfile.meshbeacon Dockerfile
export MESH_PEERS="<PI5_ID>"
export MESH_SELF="<PI4_ID>"
wendy device apps remove sh.wendy.examples.meshbeacon --device wendyos-pi4.local --force
wendy run --device wendyos-pi4.local --detach
```

Expected: `App group sh.wendy.examples.meshbeacon running in detached mode.` If the very next `wendy device apps list --device wendyos-pi4.local` shows `STOPPED`/`start_failed`, check `wendy device logs --device wendyos-pi4.local --tail 15 --json` for `"duplicate allocation is not allowed"` — if present, this is the known first-deploy self-heal (see Global Constraints): re-run the `apps remove` + `wendy run` pair once more.

- [ ] **Step 3: Deploy to pi5-nvme**

```bash
export MESH_PEERS="<PI4_ID>"
export MESH_SELF="<PI5_ID>"
wendy device apps remove sh.wendy.examples.meshbeacon --device wendyos-pi5-nvme.local --force
wendy run --device wendyos-pi5-nvme.local --detach
```

Expected: same as Step 2, on `wendyos-pi5-nvme.local`. Apply the same retry-once-on-duplicate-allocation handling if needed.

- [ ] **Step 4: Verify both devices are running**

```bash
wendy device apps list --device wendyos-pi4.local
wendy device apps list --device wendyos-pi5-nvme.local
```

Expected: both show `"runningState": "RUNNING"` for `sh.wendy.examples.meshbeacon`.

- [ ] **Step 5: Tap-test — tap pi4's screen, confirm pi5-nvme flashes**

Physically tap the pi4 display. Expected observable behavior: pi4's own screen flashes its color immediately; within roughly one mesh dial round-trip (under a second on LAN-direct), pi5-nvme's screen also flashes — a *different* color than pi4's, confirming a real broadcast happened rather than each device just flashing its own tap.

If pi5-nvme's screen does not flash, capture logs from both sides to diagnose:

```bash
wendy device logs --device wendyos-pi4.local --tail 20 --json
wendy device logs --device wendyos-pi5-nvme.local --tail 20 --json
```

Look for `"sending beacon"` on the tapped device and check whether the listener on the other device logs anything — a silent failure here most likely means the `MESH_PEERS`/`MESH_SELF` values were swapped or use stale asset IDs (re-run Step 1).

- [ ] **Step 6: Tap-test — tap pi5-nvme's screen, confirm pi4 flashes**

Same as Step 5, in the other direction. Confirms the broadcast works symmetrically, not just from one device.

- [ ] **Step 7: Restore `wendy-app-sdk/wendy.json` (a deploy overwrites it) and delete the transient `Dockerfile` copy (gitignored — see `.gitignore`'s comment on why the bare `Dockerfile` is never committed)**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git status wendy.json
```

If `git status` shows `wendy.json` as modified, restore it:

```bash
git checkout -- wendy.json
rm -f Dockerfile
```

- [ ] **Step 8: Commit** (only if Step 5/6 needed a code fix — if verification passed on the first try, there is nothing new to commit here)

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git add -A
git commit -m "fix(MeshBeacon): <describe whatever hardware verification uncovered>"
```

---

### Task 5: MeshCounter app

**Files:**
- Create: `probe/Sources/MeshCounter/main.swift`
- Create: `probe/meshcounter.wendy.json`
- Create: `Dockerfile.meshcounter`
- Modify: `probe/Package.swift`

**Interfaces:**
- Consumes: `parseMeshPeers`, `MeshFanout` (Tasks 1-2, same as Task 3); `Color`, `Canvas`, `FontFace.bundled()`, `WendyKMSDisplay`, `WendyInputDevice`, `WendyTouchEvent`, `WENDY_TOUCH_DOWN` (same verified existing APIs as Task 3).
- Produces: the `MeshCounter` executable target and its deploy config.

- [ ] **Step 1: Add the `MeshCounter` executable target to `probe/Package.swift`**

In `probe/Package.swift`, add to the `targets` array (after the `MeshBeacon` target added in Task 3):

```swift
        .executableTarget(
            name: "MeshCounter",
            dependencies: [
                .product(name: "WendyKMSDRM", package: "wendy-app-sdk"),
                .product(name: "WendyCanvas", package: "wendy-app-sdk"),
                .product(name: "WendyTextKit", package: "wendy-app-sdk"),
                .product(name: "WendyKMSInput", package: "wendy-app-sdk"),
                .product(name: "MeshFanout", package: "wendy-app-sdk"),
            ]
        ),
```

- [ ] **Step 2: Write `probe/Sources/MeshCounter/main.swift`**

```swift
import Foundation
import WendyKMSDRM
import WendyCanvas
import WendyTextKit
import WendyKMSInput
import MeshFanout

#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#endif

// MeshCounter: every device on the mesh runs this identical app, showing a
// shared running count. Tapping the screen increments the LOCAL count
// immediately (instant feedback) and broadcasts an INCREMENT message to
// every peer in MESH_PEERS; each peer applies the same +1 to its own count
// on receipt. Since every operation is "+1" (there is no decrement button in
// this demo), the message needs no payload at all — the message TYPE is the
// entire message, simpler than the spec's original "signed delta byte"
// while covering the exact same demo (YAGNI: nothing here ever sends
// anything other than +1). This is a pure-addition CRDT: commutative, so
// delivery order across different peers never matters and no conflict
// resolution is needed. Demonstrates mesh keeping simple shared state in
// sync across a fleet (contrast with MeshBeacon's transient, non-persisted
// broadcast).
//
// The display/touch stack below is identical to MeshBeacon's/RemoteCamViewer's,
// already proven on real hardware.

let listenPort: UInt16 = 9092
let incrementFrameType: UInt8 = 0x02

func log(_ message: String) {
    print("[meshcounter] \(message)")
}

let selfID = ProcessInfo.processInfo.environment["MESH_SELF"] ?? ""
let peersRaw = ProcessInfo.processInfo.environment["MESH_PEERS"] ?? ""
let peers = parseMeshPeers(peersRaw, excluding: selfID)

log("self=\(selfID.isEmpty ? "(unset)" : selfID) peers=\(peers)")

/// The shared counter. `NSLock`-guarded since the mesh listener thread and
/// the main render/input loop both touch it — same hand-off shape as
/// MeshBeacon's `FlashState`.
final class CounterState: @unchecked Sendable {
    private let lock = NSLock()
    private var value = 0
    private var dirty = false

    func increment() {
        lock.lock()
        value += 1
        dirty = true
        lock.unlock()
    }

    /// Returns the current value if it changed since the last call
    /// (nil otherwise), so the render loop only redraws on an actual change.
    func snapshotIfDirty() -> Int? {
        lock.lock()
        defer { lock.unlock() }
        guard dirty else { return nil }
        dirty = false
        return value
    }
}

let counter = CounterState()

let fanout = MeshFanout(peers: peers, listenPort: listenPort) { type, _ in
    guard type == incrementFrameType else { return }
    counter.increment()
}

do {
    try fanout.start()
    log("listening on port \(listenPort)")
} catch {
    log("failed to start listener: \(error)")
    wendy_kms_flush_stdout()
    exit(1)
}

let kmsPath = ProcessInfo.processInfo.environment["WENDY_KMS_DEVICE"] ?? "/dev/dri/card0"
log("opening \(kmsPath) (stop sh.wendy.shell first so KMS is free)")

var display = WendyKMSDisplay()
var errBuf = [CChar](repeating: 0, count: 256)
guard wendy_kms_open(kmsPath, &display, &errBuf, 256) == 0 else {
    let msg = errBuf.withUnsafeBytes { String(bytes: $0.prefix(while: { $0 != 0 }), encoding: .utf8) ?? "" }
    log("wendy_kms_open failed: \(msg)")
    wendy_kms_flush_stdout()
    exit(1)
}
guard let pixels = display.pixels else {
    log("no framebuffer mapped")
    wendy_kms_close(&display)
    wendy_kms_flush_stdout()
    exit(1)
}
let screenW = Int(display.width)
let screenH = Int(display.height)
let stride = Int(display.stride)
log("display \(screenW)x\(screenH) stride=\(stride)")
wendy_kms_flush_stdout()

let canvas = Canvas(base: pixels, width: screenW, height: screenH, stride: stride)
let font = FontFace.bundled()
let background = Color(r: 0x20, g: 0x20, b: 0x24)
let textColor = Color(r: 0xE0, g: 0xE0, b: 0xE0)
let hintColor = Color(r: 0x90, g: 0x90, b: 0x98)

func draw(count: Int) {
    canvas.fill(background)
    canvas.drawText("\(count)", x: screenW / 2 - 80, baseline: screenH / 2, pxSize: 160, color: textColor, font: font)
    canvas.drawText(
        "tap anywhere for +1", x: 24, baseline: screenH - 48, pxSize: 28, color: hintColor, font: font)
}

var inputDevice = WendyInputDevice()
var inputOpen: Bool = {
    var err = [CChar](repeating: 0, count: 1024)
    guard wendy_input_open(&inputDevice, &err, 1024) == 0 else {
        let msg = err.withUnsafeBytes { String(bytes: $0.prefix(while: { $0 != 0 }), encoding: .utf8) ?? "" }
        log("touch input unavailable, will keep retrying: \(msg)")
        return false
    }
    log("touch input active")
    return true
}()

var currentCount = 0
draw(count: currentCount)
wendy_kms_present(&display)
log("ready; tap anywhere for +1")
wendy_kms_flush_stdout()

var touchRetryTicks = 0
let touchRetryEveryTicks = 125  // ~2s at 16ms/tick, matching RemoteCamViewer's rescan cadence

func sendIncrement() {
    counter.increment()
    fanout.broadcast(type: incrementFrameType)
}

while true {
    if !inputOpen {
        touchRetryTicks += 1
        if touchRetryTicks >= touchRetryEveryTicks {
            touchRetryTicks = 0
            var err = [CChar](repeating: 0, count: 1024)
            inputOpen = wendy_input_open(&inputDevice, &err, 1024) == 0
            if inputOpen { log("touch input active") }
        }
    } else {
        var raw = [WendyTouchEvent](repeating: WendyTouchEvent(), count: 32)
        let n = wendy_input_poll(&inputDevice, &raw, 32)
        if n < 0 {
            log("touch device lost; will keep watching for it to return")
            wendy_input_close(&inputDevice)
            inputOpen = false
        } else if n > 0 {
            for i in 0..<Int(n) where raw[i].kind == Int32(WENDY_TOUCH_DOWN.rawValue) {
                sendIncrement()
            }
        }
    }

    if let newCount = counter.snapshotIfDirty() {
        currentCount = newCount
        draw(count: currentCount)
        wendy_kms_present(&display)
    }

    usleep(16_000)
}
```

- [ ] **Step 3: Write `probe/meshcounter.wendy.json`**

```json
{
    "appId": "sh.wendy.examples.meshcounter",
    "version": "0.1.0",
    "platform": "linux",
    "isolation": "isolated",
    "services": {
        "meshcounter": {
            "context": ".",
            "entitlements": [
                { "type": "display" },
                { "type": "input" },
                {
                    "type": "network",
                    "mode": "mesh",
                    "serviceCIDR": "10.99.0.0/16",
                    "ports": [{ "host": 9092, "container": 9092 }]
                }
            ],
            "env": {
                "MESH_PEERS": "${MESH_PEERS}",
                "MESH_SELF": "${MESH_SELF}"
            }
        }
    }
}
```

- [ ] **Step 4: Write `Dockerfile.meshcounter`**

```dockerfile
# MeshCounter has to be built via a Dockerfile rather than the Swift
# buildpack, and built from the wendy-app-sdk repo root rather than probe/
# alone — see wendy-app-sdk/Dockerfile.remotecamviewer for the full
# explanation; this file is that same shape with only the --product name
# and output binary name changed.
FROM swift:6.1-noble AS build
WORKDIR /wendy-app-sdk
COPY Package.swift Package.resolved* ./
COPY Sources ./Sources
COPY probe ./probe
WORKDIR /wendy-app-sdk/probe
RUN swift build -c release --product MeshCounter

FROM swift:6.1-noble-slim
WORKDIR /app
COPY --from=build /wendy-app-sdk/probe/.build/release/MeshCounter ./MeshCounter
CMD ["./MeshCounter"]
```

- [ ] **Step 5: Build for macOS to catch any type/API errors before cross-compiling**

Run: `cd /Users/joannisorlandos/git/wendy/wendy-app-sdk && swift build --target MeshCounter`
Expected: `Build complete!`

- [ ] **Step 6: Commit**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git add probe/Package.swift probe/Sources/MeshCounter probe/meshcounter.wendy.json Dockerfile.meshcounter
git commit -m "feat(MeshCounter): shared-state mesh sync demo app"
```

---

### Task 6: MeshCounter hardware verification

**Files:** none (deploy + manual test only)

**Interfaces:** none — this task validates Task 5's deliverable end-to-end.

- [ ] **Step 1: Confirm current asset IDs for both devices (do not reuse Task 4's values without rechecking — re-enrollment between tasks would change them)**

```bash
export PATH="/Users/joannisorlandos/git/wendy/wendyos-mesh/go/bin:$PATH"
wendy cloud discover --json
```

- [ ] **Step 2: Deploy to pi4**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
cp probe/meshcounter.wendy.json wendy.json
cp Dockerfile.meshcounter Dockerfile
export MESH_PEERS="<PI5_ID>"
export MESH_SELF="<PI4_ID>"
wendy device apps remove sh.wendy.examples.meshcounter --device wendyos-pi4.local --force
wendy run --device wendyos-pi4.local --detach
```

Expected: same success/retry-once handling as Task 4 Step 2.

- [ ] **Step 3: Deploy to pi5-nvme**

```bash
export MESH_PEERS="<PI4_ID>"
export MESH_SELF="<PI5_ID>"
wendy device apps remove sh.wendy.examples.meshcounter --device wendyos-pi5-nvme.local --force
wendy run --device wendyos-pi5-nvme.local --detach
```

- [ ] **Step 4: Verify both devices are running**

```bash
wendy device apps list --device wendyos-pi4.local
wendy device apps list --device wendyos-pi5-nvme.local
```

Expected: both show `"runningState": "RUNNING"` for `sh.wendy.examples.meshcounter`, both displaying `0`.

- [ ] **Step 5: Tap-test — tap pi4 three times, confirm both devices read 3**

Physically tap pi4's screen three times. Expected: pi4's own display updates to `3` immediately after each tap; pi5-nvme's display also reaches `3` within roughly one mesh round-trip per tap.

- [ ] **Step 6: Tap-test — tap pi5-nvme twice, confirm both devices read 5**

Continuing from Step 5's state (both at `3`), tap pi5-nvme's screen twice. Expected: both devices converge on `5` — confirms deltas from *either* device apply correctly to *both* displays, not just one direction.

If either display diverges (e.g. one shows a different count than the other after settling), capture logs from both sides:

```bash
wendy device logs --device wendyos-pi4.local --tail 20 --json
wendy device logs --device wendyos-pi5-nvme.local --tail 20 --json
```

A stuck/mismatched count with no errors logged on either side most likely means one direction's `MESH_PEERS` is wrong (check for a typo or a stale asset ID — rerun Step 1).

- [ ] **Step 7: Restore `wendy-app-sdk/wendy.json` and delete the transient `Dockerfile` copy (gitignored — see Task 4 Step 7's note)**

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git checkout -- wendy.json
rm -f Dockerfile
```

- [ ] **Step 8: Commit** (only if Step 5/6 needed a code fix)

```bash
cd /Users/joannisorlandos/git/wendy/wendy-app-sdk
git add -A
git commit -m "fix(MeshCounter): <describe whatever hardware verification uncovered>"
```
