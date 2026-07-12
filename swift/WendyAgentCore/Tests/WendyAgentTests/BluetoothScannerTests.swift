import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

private struct FakeBluetooth: BluetoothManaging {
    var discovered: [DiscoveredPeripheral] = []
    var connectResult: BluetoothActionResult = .ok

    func scan() -> AsyncStream<DiscoveredPeripheral> {
        let peripherals = discovered
        return AsyncStream { continuation in
            for peripheral in peripherals { continuation.yield(peripheral) }
            continuation.finish()
        }
    }
    func connect(address: String) async -> BluetoothActionResult { connectResult }
    func disconnect(address: String) async -> BluetoothActionResult { connectResult }
}

@Suite("AgentService Bluetooth adapters")
struct BluetoothAdapterTests {
    @Test("scanBluetoothPeripherals streams discovered peripherals")
    func scanStreams() async throws {
        let fake = FakeBluetooth(discovered: [
            DiscoveredPeripheral(
                name: "Headphones",
                address: "11111111-2222-3333-4444-555555555555",
                rssi: -42,
                deviceType: "ble",
                paired: false,
                connected: false,
                trusted: false
            )
        ])
        let service = AgentService(bluetooth: fake)

        let response = try await service.scanBluetoothPeripherals(
            request: makeStreamingRequest(
                Wendy_Agent_Services_V1_ScanBluetoothPeripheralsRequest()
            ),
            context: makeBTContext(method: "ScanBluetoothPeripherals")
        )

        let writer = CollectingWriter<Wendy_Agent_Services_V1_ScanBluetoothPeripheralsResponse>()
        _ = try await response.accepted.get().producer(RPCWriter(wrapping: writer))
        let messages = writer.snapshot()
        #expect(messages.count == 1)
        #expect(messages[0].discoveredDevices.first?.name == "Headphones")
        #expect(messages[0].discoveredDevices.first?.rssi == -42)
    }

    @Test("connectBluetoothPeripheral throws on failure")
    func connectFailureThrows() async {
        let fake = FakeBluetooth(connectResult: .failed("not found"))
        let service = AgentService(bluetooth: fake)

        var request = Wendy_Agent_Services_V1_ConnectBluetoothPeripheralRequest()
        request.address = "11111111-2222-3333-4444-555555555555"

        do {
            _ = try await service.connectBluetoothPeripheral(
                request: ServerRequest(metadata: [:], message: request),
                context: makeBTContext(method: "ConnectBluetoothPeripheral")
            )
            Issue.record("expected connect to throw")
        } catch let error as RPCError {
            #expect(error.message.contains("not found"))
        } catch {
            Issue.record("expected RPCError, got \(error)")
        }
    }

    @Test("forgetBluetoothPeripheral reports the macOS BLE limitation")
    func forgetUnsupported() async {
        let service = AgentService()
        do {
            _ = try await service.forgetBluetoothPeripheral(
                request: ServerRequest(
                    metadata: [:],
                    message: Wendy_Agent_Services_V1_ForgetBluetoothPeripheralRequest()
                ),
                context: makeBTContext(method: "ForgetBluetoothPeripheral")
            )
            Issue.record("expected forget to throw")
        } catch let error as RPCError {
            #expect(error.code == .unimplemented)
            #expect(error.message.contains("BLE-only"))
        } catch {
            Issue.record("expected RPCError, got \(error)")
        }
    }
}

private final class CollectingWriter<Element: Sendable>: RPCWriterProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "wendy.tests.bt-collecting-writer")
    private var elements: [Element] = []

    func write(_ element: Element) async throws {
        queue.sync { elements.append(element) }
    }
    func write(contentsOf elements: some Sequence<Element>) async throws {
        queue.sync { self.elements.append(contentsOf: elements) }
    }
    func snapshot() -> [Element] {
        queue.sync { elements }
    }
}

private func makeStreamingRequest<Message: Sendable>(
    _ message: Message
) -> StreamingServerRequest<Message> {
    StreamingServerRequest(
        metadata: [:],
        messages: RPCAsyncSequence(
            wrapping: AsyncThrowingStream<Message, any Error> { continuation in
                continuation.yield(message)
                continuation.finish()
            }
        )
    )
}

private func makeBTContext(method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyAgentService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
