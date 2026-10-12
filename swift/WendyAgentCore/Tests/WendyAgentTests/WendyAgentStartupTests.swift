import Darwin
import Foundation
import Testing

@testable import WendyAgentCore

@Suite("WendyAgent startup", .serialized)
struct WendyAgentStartupTests {
    @Test("startup listens and advertises on the preferred port when it is available")
    func usesPreferredPort() async throws {
        let availablePort = try OccupiedIPv6TCPPort()
        let preferredPort = availablePort.port
        availablePort.close()

        let agent = WendyAgent(
            configuration: .init(port: preferredPort, otelPort: 0),
            probesLinuxContainerRuntime: false
        )

        try await agent.start()
        #expect(await agent.listeningPort == preferredPort)
        #expect(await agent.advertisedPort == preferredPort)
        await agent.stop()
    }

    @Test("startup selects and advertises a free port when the preferred port is occupied")
    func recoversFromPortConflict() async throws {
        let occupiedPort = try OccupiedIPv6TCPPort()
        defer { occupiedPort.close() }

        let agent = WendyAgent(
            configuration: .init(port: occupiedPort.port, otelPort: 0),
            probesLinuxContainerRuntime: false
        )

        try await agent.start()
        do {
            let listeningPort = try #require(await agent.listeningPort)
            let advertisedPort = try #require(await agent.advertisedPort)

            #expect(listeningPort != occupiedPort.port)
            #expect(advertisedPort == listeningPort)
        } catch {
            await agent.stop()
            throw error
        }
        await agent.stop()
    }
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
