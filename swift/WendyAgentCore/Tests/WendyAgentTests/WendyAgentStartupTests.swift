import Darwin
import Foundation
import Testing

@testable import WendyAgentCore

@Suite("WendyAgent startup", .serialized)
struct WendyAgentStartupTests {
    @Test("startup is local-only and access changes rebind the running server")
    func localOnlyStartup() async throws {
        let reservation = try OccupiedIPv6TCPPort()
        let port = reservation.port
        reservation.close()
        let agent = WendyAgent(
            configuration: .init(port: port, otelPort: 0),
            reportedVersion: { "test" }
        )
        do {
            try await agent.start()
            // A deliberately absent Bonjour task must not disable monitoring
            // or be mistaken for a failed runtime subsystem.
            try await Task.sleep(for: .milliseconds(100))
            #expect(await agent.status == .running)

            let listeners = try listenerNames(port: port)
            #expect(listeners.contains("n127.0.0.1:\(port)"))
            #expect(!listeners.contains("n*:\(port)"))

            try await agent.setNetworkAccess(allowed: true)
            #expect(await agent.configuration.allowsNetworkAccess)
            #expect(try listenerNames(port: port).contains("n*:\(port)"))

            try await agent.setNetworkAccess(allowed: false)
            #expect(!(await agent.configuration.allowsNetworkAccess))
            let localListeners = try listenerNames(port: port)
            #expect(localListeners.contains("n127.0.0.1:\(port)"))
            #expect(!localListeners.contains("n*:\(port)"))
            #expect(await agent.status == .running)
            await agent.stop()
        } catch {
            await agent.stop()
            throw error
        }
    }

    @Test("startup reports a port-in-use error when the gRPC port is occupied")
    func reportsPortConflict() async throws {
        let occupiedPort = try OccupiedIPv6TCPPort()
        defer { occupiedPort.close() }

        let agent = await WendyAgent(
            configuration: .init(host: "::", port: occupiedPort.port, otelPort: 0)
        )

        do {
            try await agent.start()
            Issue.record("Expected WendyAgent startup to fail on an occupied gRPC port")
            await agent.stop()
        } catch {
            guard case WendyAgentError.portInUse(let serviceName, let port) = error else {
                Issue.record("Expected portInUse error, got: \(error)")
                return
            }

            #expect(serviceName == "Wendy Agent gRPC")
            #expect(port == occupiedPort.port)

            let description = String(describing: error)
            #expect(description.contains("TCP port \(occupiedPort.port) is already in use"))
            #expect(!description.contains("There is no listening address bound"))
        }
    }
}

private func listenerNames(port: Int) throws -> String {
    let process = Process()
    process.executableURL = URL(fileURLWithPath: "/usr/sbin/lsof")
    process.arguments = [
        "-nP", "-a", "-p", String(ProcessInfo.processInfo.processIdentifier), "-iTCP:\(port)",
        "-sTCP:LISTEN", "-F", "n",
    ]
    let output = Pipe()
    process.standardOutput = output
    try process.run()
    let data = output.fileHandleForReading.readDataToEndOfFile()
    process.waitUntilExit()
    #expect(process.terminationStatus == 0)
    return String(decoding: data, as: UTF8.self)
}

private final class OccupiedIPv6TCPPort: @unchecked Sendable {
    let fileDescriptor: Int32
    let port: Int

    init() throws {
        let fileDescriptor = socket(AF_INET6, SOCK_STREAM, IPPROTO_TCP)
        guard fileDescriptor >= 0 else {
            throw currentPOSIXError()
        }

        do {
            var address = sockaddr_in6()
            address.sin6_len = UInt8(MemoryLayout<sockaddr_in6>.size)
            address.sin6_family = sa_family_t(AF_INET6)
            address.sin6_port = in_port_t(0).bigEndian
            address.sin6_addr = in6addr_any

            try withUnsafePointer(to: &address) { pointer in
                try pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { socketAddress in
                    guard
                        bind(
                            fileDescriptor,
                            socketAddress,
                            socklen_t(MemoryLayout<sockaddr_in6>.size)
                        ) == 0
                    else {
                        throw currentPOSIXError()
                    }
                }
            }

            guard listen(fileDescriptor, 1) == 0 else {
                throw currentPOSIXError()
            }

            var boundAddress = sockaddr_in6()
            var boundAddressLength = socklen_t(MemoryLayout<sockaddr_in6>.size)
            try withUnsafeMutablePointer(to: &boundAddress) { pointer in
                try pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { socketAddress in
                    guard getsockname(fileDescriptor, socketAddress, &boundAddressLength) == 0
                    else {
                        throw currentPOSIXError()
                    }
                }
            }

            self.fileDescriptor = fileDescriptor
            self.port = Int(in_port_t(bigEndian: boundAddress.sin6_port))
        } catch {
            Darwin.close(fileDescriptor)
            throw error
        }
    }

    func close() {
        Darwin.close(self.fileDescriptor)
    }
}

private func currentPOSIXError() -> POSIXError {
    POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
}
