import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

private struct FakeAudio: AudioManaging {
    var devices: [AudioDeviceInfo] = []
    var levelSamples: [(peakDb: Float, rmsDb: Float)] = []
    let recordedFilter: FilterBox = .init()

    func listDevices(typeFilter: AudioKind?) async throws -> [AudioDeviceInfo] {
        recordedFilter.value = typeFilter
        return devices
    }
    func setDefault(deviceID: UInt32) async throws {}
    func levels(
        deviceID: UInt32,
        rateHz: UInt32
    ) -> AsyncThrowingStream<
        (peakDb: Float, rmsDb: Float), any Error
    > {
        let samples = levelSamples
        return AsyncThrowingStream { continuation in
            for sample in samples { continuation.yield(sample) }
            continuation.finish()
        }
    }
    func audio(
        deviceID: UInt32,
        sampleRate: UInt32,
        channels: UInt32
    ) -> AsyncThrowingStream<
        (pcm: Data, sampleRate: UInt32, channels: UInt32), any Error
    > {
        AsyncThrowingStream { $0.finish() }
    }

    final class FilterBox: @unchecked Sendable { var value: AudioKind? }
}

@Suite("AudioService adapters")
struct AudioServiceAdapterTests {
    @Test("listAudioDevices maps devices and forwards the type filter")
    func listMapsDevices() async throws {
        let fake = FakeAudio(devices: [
            AudioDeviceInfo(id: 42, name: "Built-in Mic", kind: .input, isDefault: true)
        ])
        let service = AudioService(audio: fake)

        var request = Wendy_Agent_Services_V1_ListAudioDevicesRequest()
        request.typeFilter = .input

        let response = try await service.listAudioDevices(
            request: ServerRequest(metadata: [:], message: request),
            context: makeAudioContext(method: "ListAudioDevices")
        )
        #expect(fake.recordedFilter.value == .input)
        let devices = try response.message.devices
        #expect(devices.count == 1)
        #expect(devices[0].id == 42)
        #expect(devices[0].name == "Built-in Mic")
        #expect(devices[0].type == .input)
        #expect(devices[0].isDefault)
    }

    @Test("streamAudioLevels forwards provider samples as updates")
    func streamsLevels() async throws {
        let fake = FakeAudio(levelSamples: [(peakDb: -6, rmsDb: -12), (peakDb: -3, rmsDb: -9)])
        let service = AudioService(audio: fake)

        let response = try await service.streamAudioLevels(
            request: ServerRequest(
                metadata: [:],
                message: Wendy_Agent_Services_V1_StreamAudioLevelsRequest()
            ),
            context: makeAudioContext(method: "StreamAudioLevels")
        )

        let writer = CollectingWriter<Wendy_Agent_Services_V1_AudioLevelUpdate>()
        _ = try await response.accepted.get().producer(RPCWriter(wrapping: writer))
        let updates = writer.snapshot()
        #expect(updates.count == 2)
        #expect(updates[0].peakDb == -6)
        #expect(updates[1].rmsDb == -9)
    }
}

@Suite("AudioTapSession buffer math")
struct AudioTapSessionMathTests {
    @Test("linearToDb maps amplitudes to decibels")
    func linearToDb() {
        #expect(AudioTapSession.linearToDb(1.0) == 0)
        #expect(AudioTapSession.linearToDb(0) == -160)
        #expect(AudioTapSession.linearToDb(-0.5) == -160)
        #expect(AudioTapSession.linearToDb(0.1).rounded() == -20)
    }
}

private final class CollectingWriter<Element: Sendable>: RPCWriterProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "wendy.tests.audio-collecting-writer")
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

private func makeAudioContext(method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyAudioService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
