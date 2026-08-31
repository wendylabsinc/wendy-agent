import Foundation
import GRPCCore
import Testing

@testable import WendyAgentCore
@testable import WendyAgentGRPC

private struct FakeAudio: AudioManaging {
    func listDevices(typeFilter: AudioKind?) async throws -> [AudioDeviceInfo] { [] }
    func setDefault(deviceID: UInt32) async throws {}
    func levels(
        deviceID: UInt32,
        rateHz: UInt32
    ) -> AsyncThrowingStream<(peakDb: Float, rmsDb: Float), any Error> {
        AsyncThrowingStream { $0.finish() }
    }
    func audio(
        deviceID: UInt32,
        sampleRate: UInt32,
        channels: UInt32
    ) -> AsyncThrowingStream<(pcm: Data, sampleRate: UInt32, channels: UInt32), any Error> {
        AsyncThrowingStream { continuation in
            continuation.yield((pcm: Data([1, 2, 3, 4]), sampleRate: 48000, channels: 1))
            continuation.yield((pcm: Data([5, 6, 7, 8]), sampleRate: 48000, channels: 1))
            continuation.finish()
        }
    }
}

private final class CollectingWriter<Element: Sendable>: RPCWriterProtocol, @unchecked Sendable {
    private let queue = DispatchQueue(label: "wendy.tests.sensor-collecting-writer")
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

private func makeServerContext(method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v2.WendySensorService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}

@Test func manifestReportsMicAndStreamYieldsPCMFrames() async throws {
    let svc = SensorService(audio: FakeAudio())

    let manifestResponse = try await svc.getSensorManifest(
        request: ServerRequest(
            metadata: [:],
            message: Wendy_Agent_Services_V2_GetSensorManifestRequest()
        ),
        context: makeServerContext(method: "GetSensorManifest")
    )
    let manifest = try manifestResponse.message
    #expect(manifest.sensors.contains { $0.kind == .microphone })
    let mic = try #require(manifest.sensors.first { $0.kind == .microphone })
    #expect(mic.channelID == SensorService.micChannel)
    #expect(mic.audio.codec == .pcmS16Le)
    #expect(mic.audio.sampleRate == 48000)
    #expect(mic.audio.channels == 1)

    var request = Wendy_Agent_Services_V2_StreamSensorsRequest()
    request.channelID = [SensorService.micChannel]
    let streamResponse = try await svc.streamSensors(
        request: ServerRequest(metadata: [:], message: request),
        context: makeServerContext(method: "StreamSensors")
    )
    let contents = try streamResponse.accepted.get()
    let writer = CollectingWriter<Wendy_Lite_Sensorlink_SensorFrame>()
    _ = try await contents.producer(RPCWriter(wrapping: writer))

    let frames = writer.snapshot()
    #expect(frames.count == 2)
    #expect(frames.allSatisfy { $0.channelID == SensorService.micChannel })
    #expect(frames.first?.payload == Data([1, 2, 3, 4]))
    #expect(frames.last?.payload == Data([5, 6, 7, 8]))
    #expect(frames.map(\.seq) == [0, 1])
}
