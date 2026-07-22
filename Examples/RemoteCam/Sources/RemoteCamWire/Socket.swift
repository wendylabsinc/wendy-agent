// Plain POSIX TCP sockets (no SwiftNIO, no Foundation networking types) —
// socket/bind/listen/accept/read/write via Glibc on Linux, Darwin on macOS.
// This split (Glibc vs Darwin, matching the convention already used in
// .github/ci-tests/swift-network/Sources/main.swift) is what lets this file
// actually build and type-check on macOS, unlike the V4L2 half of this app.

import Foundation

#if canImport(Glibc)
    import Glibc
#elseif canImport(Darwin)
    import Darwin
#else
    #error("RemoteCamWire requires Glibc or Darwin for POSIX sockets")
#endif

/// Errors from the underlying POSIX socket calls.
public enum SocketError: Error, CustomStringConvertible {
    case syscallFailed(String, Int32)
    case connectionClosed

    public var description: String {
        switch self {
        case .syscallFailed(let name, let err):
            return "\(name) failed (errno \(err))"
        case .connectionClosed:
            return "connection closed by peer"
        }
    }
}

/// SOCK_STREAM has a different raw type on Glibc (an option-set-like struct)
/// vs. Darwin (already an Int32); normalize it once here.
private func streamSocketType() -> Int32 {
    #if canImport(Glibc)
        return Int32(SOCK_STREAM.rawValue)
    #else
        return SOCK_STREAM
    #endif
}

private func closeFD(_ fd: Int32) {
    #if canImport(Glibc)
        Glibc.close(fd)
    #else
        Darwin.close(fd)
    #endif
}

/// A minimal byte-stream abstraction so the wire-protocol code below doesn't
/// need to know it's talking to a real socket (useful for testing with an
/// in-memory fake, and keeps `readFrame`/`writeFrame` platform-agnostic).
public protocol ByteChannel: AnyObject {
    /// Read exactly `count` bytes, blocking until they arrive. Throws
    /// `SocketError.connectionClosed` if the peer closes before `count`
    /// bytes are available.
    func readExactly(_ count: Int) throws -> [UInt8]

    /// Write all of `bytes`, blocking until the whole buffer is sent.
    func writeAll(_ bytes: [UInt8]) throws

    /// Close the underlying connection. Safe to call more than once.
    func close()
}

/// A connected TCP socket.
///
/// @unchecked Sendable: camserver's main.swift calls `readExactly` only from
/// its command-reader thread and `writeAll` only from its frame-streaming
/// thread for a given connection's lifetime — the two directions never touch
/// shared mutable state (each syscall reads the immutable `fd` directly).
/// `close()` is the one method either side (plus the accept loop) can call
/// concurrently, so it alone is lock-guarded to make that safe; `readExactly`/
/// `writeAll` intentionally stay lock-free since the disjoint-thread
/// invariant above already rules out a data race on them.
public final class SocketChannel: ByteChannel, @unchecked Sendable {
    private let fd: Int32
    private let closeLock = NSLock()
    private var closed = false

    public init(fd: Int32) {
        self.fd = fd
    }

    public func readExactly(_ count: Int) throws -> [UInt8] {
        guard count > 0 else { return [] }
        var buffer = [UInt8](repeating: 0, count: count)
        var total = 0
        while total < count {
            let n = buffer.withUnsafeMutableBytes { raw -> Int in
                read(fd, raw.baseAddress!.advanced(by: total), count - total)
            }
            if n == 0 {
                throw SocketError.connectionClosed
            }
            if n < 0 {
                if errno == EINTR { continue }
                throw SocketError.syscallFailed("read", errno)
            }
            total += n
        }
        return buffer
    }

    public func writeAll(_ bytes: [UInt8]) throws {
        guard !bytes.isEmpty else { return }
        var total = 0
        try bytes.withUnsafeBytes { raw in
            while total < bytes.count {
                let n = write(fd, raw.baseAddress!.advanced(by: total), bytes.count - total)
                if n < 0 {
                    if errno == EINTR { continue }
                    throw SocketError.syscallFailed("write", errno)
                }
                total += n
            }
        }
    }

    public func close() {
        closeLock.lock()
        let alreadyClosed = closed
        closed = true
        closeLock.unlock()
        guard !alreadyClosed else { return }
        closeFD(fd)
    }

    deinit {
        close()
    }
}

/// A listening TCP socket bound to 0.0.0.0:<port>.
public final class TCPListener {
    private let fd: Int32

    public init(port: UInt16) throws {
        let newFD = socket(AF_INET, streamSocketType(), 0)
        guard newFD >= 0 else {
            throw SocketError.syscallFailed("socket", errno)
        }

        var reuse: Int32 = 1
        _ = setsockopt(
            newFD, SOL_SOCKET, SO_REUSEADDR, &reuse, socklen_t(MemoryLayout<Int32>.size))

        var addr = sockaddr_in()
        #if canImport(Darwin)
            addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        #endif
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = port.bigEndian
        addr.sin_addr.s_addr = INADDR_ANY

        let bindResult = withUnsafePointer(to: &addr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                bind(newFD, sa, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bindResult == 0 else {
            closeFD(newFD)
            throw SocketError.syscallFailed("bind", errno)
        }

        guard listen(newFD, 8) == 0 else {
            closeFD(newFD)
            throw SocketError.syscallFailed("listen", errno)
        }

        self.fd = newFD
    }

    /// Block until a client connects, then return the connected channel.
    public func acceptOne() throws -> SocketChannel {
        var clientAddr = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let clientFD = withUnsafeMutablePointer(to: &clientAddr) { ptr -> Int32 in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sa in
                accept(fd, sa, &len)
            }
        }
        guard clientFD >= 0 else {
            throw SocketError.syscallFailed("accept", errno)
        }
        return SocketChannel(fd: clientFD)
    }

    public func close() {
        closeFD(fd)
    }
}
