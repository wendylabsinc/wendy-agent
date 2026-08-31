import Dispatch
import Foundation
import Logging
import dnssd

struct BonjourAdvertiser {
    struct Runtime {
        let registration: BonjourRegistration
        let task: Task<Void, any Error>
    }

    let port: Int
    let displayName: String
    let deviceID: String
    var tls: Bool = false
    var assetID: Int32? = nil
    var orgID: Int32? = nil
    var caps: [String] = []

    private let logger = Logger(label: "sh.wendy.agent.bonjour")

    func start() async throws -> Runtime {
        let registration = BonjourRegistration(port: self.port, txtData: self.txtData)
        try await registration.start()

        self.logger.info("Advertising \(self.displayName) as _wendyos._udp on port \(self.port)")

        let task = Task {
            try await registration.waitForShutdown()
        }

        return Runtime(registration: registration, task: task)
    }

    private var txtData: Data {
        Self.encodeTXT(
            displayName: self.displayName,
            deviceID: self.deviceID,
            tls: self.tls,
            assetID: self.assetID,
            orgID: self.orgID,
            caps: self.caps
        )
    }

    /// Encodes DNS-SD TXT records as length-prefixed `key=value` fields. `tls`,
    /// `assetid`, and `orgid` mirror what the wendy CLI reads to decide mTLS vs
    /// plaintext, to label the device, and to enforce same-org pairing (see
    /// discovery_*.go). `caps` advertises optional capabilities (e.g. "sensors")
    /// as a comma-joined list; omitted when empty.
    static func encodeTXT(
        displayName: String, deviceID: String, tls: Bool, assetID: Int32?, orgID: Int32?,
        caps: [String]
    ) -> Data {
        var fields = ["displayname=\(displayName)", "id=\(deviceID)", "tls=\(tls)"]
        if let assetID {
            fields.append("assetid=\(assetID)")
        }
        if let orgID {
            fields.append("orgid=\(orgID)")
        }
        if !caps.isEmpty {
            fields.append("caps=\(caps.joined(separator: ","))")
        }
        return fields.reduce(into: Data()) { data, field in
            data.append(UInt8(field.utf8.count))
            data.append(contentsOf: field.utf8)
        }
    }
}

final class BonjourRegistration: @unchecked Sendable {
    private let port: Int
    private let txtData: Data
    private let queue = DispatchQueue(label: "sh.wendy.agent.bonjour.registration")

    private var serviceRef: DNSServiceRef?
    private var readyContinuation: CheckedContinuation<Void, any Error>?
    private var shutdownContinuation: CheckedContinuation<Void, any Error>?
    private var hasRegistered = false
    private var isFinished = false
    private var completionError: (any Error)?

    init(port: Int, txtData: Data) {
        self.port = port
        self.txtData = txtData
    }

    func start() async throws {
        try await withCheckedThrowingContinuation { continuation in
            self.queue.async {
                self.startOnQueue(continuation: continuation)
            }
        }
    }

    func waitForShutdown() async throws {
        try await withCheckedThrowingContinuation { continuation in
            self.queue.async {
                if self.isFinished {
                    self.resume(continuation: continuation, with: self.completionError)
                } else {
                    precondition(self.shutdownContinuation == nil)
                    self.shutdownContinuation = continuation
                }
            }
        }
    }

    func shutdown() async {
        await withCheckedContinuation { continuation in
            self.queue.async {
                self.finishOnQueue(error: nil)
                continuation.resume()
            }
        }
    }

    /// Returns the `if_nametoindex` of the interface the kernel would use to
    /// reach the public internet (the default-route / primary LAN interface),
    /// or `0` (all interfaces) if it can't be determined. Uses the UDP-connect
    /// trick: connecting a datagram socket sends no packets but makes the kernel
    /// pick a source address, which `getsockname` then reveals; that address is
    /// mapped back to its interface name.
    static func primaryIPv4InterfaceIndex() -> UInt32 {
        let sock = socket(AF_INET, SOCK_DGRAM, 0)
        guard sock >= 0 else { return 0 }
        defer { close(sock) }

        var dst = sockaddr_in()
        dst.sin_family = sa_family_t(AF_INET)
        dst.sin_port = in_port_t(UInt16(53).bigEndian)
        _ = "8.8.8.8".withCString { inet_pton(AF_INET, $0, &dst.sin_addr) }
        let connected = withUnsafePointer(to: &dst) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                connect(sock, $0, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard connected == 0 else { return 0 }

        var local = sockaddr_in()
        var len = socklen_t(MemoryLayout<sockaddr_in>.size)
        let named = withUnsafeMutablePointer(to: &local) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                getsockname(sock, $0, &len)
            }
        }
        guard named == 0 else { return 0 }
        let localAddr = local.sin_addr.s_addr

        var ifaddrPtr: UnsafeMutablePointer<ifaddrs>?
        guard getifaddrs(&ifaddrPtr) == 0 else { return 0 }
        defer { freeifaddrs(ifaddrPtr) }
        var cur = ifaddrPtr
        while let ifa = cur {
            defer { cur = ifa.pointee.ifa_next }
            guard let sa = ifa.pointee.ifa_addr, sa.pointee.sa_family == UInt8(AF_INET) else {
                continue
            }
            let addr = sa.withMemoryRebound(to: sockaddr_in.self, capacity: 1) {
                $0.pointee.sin_addr.s_addr
            }
            if addr == localAddr {
                return if_nametoindex(ifa.pointee.ifa_name)
            }
        }
        return 0
    }

    private func startOnQueue(continuation: CheckedContinuation<Void, any Error>) {
        precondition(self.readyContinuation == nil)
        self.readyContinuation = continuation

        var serviceRef: DNSServiceRef?
        // Advertise only on the primary LAN interface, not on every interface
        // (index 0). A dev Mac often has junk utun/tunnel interfaces (Docker,
        // VPNs, container runtimes) carrying unroutable network-base (.0)
        // addresses; advertising on those makes a consumer's discovery pick an
        // address it can't reach. 0 is used as a fallback when detection fails.
        let interfaceIndex = Self.primaryIPv4InterfaceIndex()
        let error = self.txtData.withUnsafeBytes { buffer in
            DNSServiceRegister(
                &serviceRef,
                0,
                interfaceIndex,
                nil,
                "_wendyos._udp.",
                nil,
                nil,
                UInt16(self.port).bigEndian,
                UInt16(buffer.count),
                buffer.baseAddress,
                Self.handleRegistrationCallback,
                Unmanaged.passUnretained(self).toOpaque()
            )
        }

        guard error == kDNSServiceErr_NoError, let serviceRef else {
            self.readyContinuation = nil
            continuation.resume(throwing: BonjourError.registrationFailed(error))
            return
        }

        let queueError = DNSServiceSetDispatchQueue(serviceRef, self.queue)
        guard queueError == kDNSServiceErr_NoError else {
            DNSServiceRefDeallocate(serviceRef)
            self.readyContinuation = nil
            continuation.resume(throwing: BonjourError.registrationFailed(queueError))
            return
        }

        self.serviceRef = serviceRef
    }

    private func handleRegistrationCallback(
        flags: DNSServiceFlags,
        errorCode: DNSServiceErrorType
    ) {
        if errorCode != kDNSServiceErr_NoError {
            self.finishOnQueue(error: BonjourError.registrationFailed(errorCode))
            return
        }

        let hasAddFlag = (flags & DNSServiceFlags(kDNSServiceFlagsAdd)) != 0
        guard hasAddFlag else {
            self.finishOnQueue(error: BonjourError.registrationLost)
            return
        }

        guard !self.hasRegistered else { return }
        self.hasRegistered = true

        let continuation = self.readyContinuation
        self.readyContinuation = nil
        continuation?.resume(returning: ())
    }

    private func finishOnQueue(error: (any Error)?) {
        guard !self.isFinished else { return }

        self.isFinished = true
        self.completionError = error

        if let serviceRef = self.serviceRef {
            DNSServiceRefDeallocate(serviceRef)
            self.serviceRef = nil
        }

        if let continuation = self.readyContinuation {
            self.readyContinuation = nil
            self.resume(continuation: continuation, with: error)
        }

        if let continuation = self.shutdownContinuation {
            self.shutdownContinuation = nil
            self.resume(continuation: continuation, with: error)
        }
    }

    private func resume(
        continuation: CheckedContinuation<Void, any Error>,
        with error: (any Error)?
    ) {
        if let error {
            continuation.resume(throwing: error)
        } else {
            continuation.resume(returning: ())
        }
    }

    private static let handleRegistrationCallback: DNSServiceRegisterReply = {
        serviceRef,
        flags,
        errorCode,
        _,
        _,
        _,
        context in
        guard let context else { return }

        let registration = Unmanaged<BonjourRegistration>
            .fromOpaque(context)
            .takeUnretainedValue()
        registration.handleRegistrationCallback(flags: flags, errorCode: errorCode)
    }
}

enum BonjourError: Error {
    case registrationFailed(DNSServiceErrorType)
    case registrationLost
}

extension BonjourError: LocalizedError {
    var errorDescription: String? {
        switch self {
        case .registrationFailed(let code):
            return "Bonjour registration failed (DNS-SD error \(code))."
        case .registrationLost:
            return "Bonjour registration stopped unexpectedly."
        }
    }
}
