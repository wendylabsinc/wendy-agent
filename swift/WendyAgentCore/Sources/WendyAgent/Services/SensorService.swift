import AVFoundation
import Foundation
import GRPCCore
import WendyAgentGRPC

/// Implements `wendy.agent.services.v2.WendySensorService`: reports the sensors
/// available on this host and streams their frames over one multiplexed RPC.
///
/// The microphone channel reuses `AudioController` (int16-LE PCM). Camera support
/// (channel 2) lands in a follow-up task — this type leaves the seam (an injectable
/// camera producer) but does not implement it.
struct SensorService: Wendy_Agent_Services_V2_WendySensorService.ServiceProtocol {
    static let micChannel: UInt32 = 1

    var audio: any AudioManaging = AudioController()

    func getSensorManifest(
        request: ServerRequest<Wendy_Agent_Services_V2_GetSensorManifestRequest>,
        context: ServerContext
    ) async throws -> ServerResponse<Wendy_Lite_Sensorlink_SensorManifest> {
        var manifest = Wendy_Lite_Sensorlink_SensorManifest()
        if micAuthorized() {
            var descriptor = Wendy_Lite_Sensorlink_SensorDescriptor()
            descriptor.channelID = Self.micChannel
            descriptor.kind = .microphone
            descriptor.name = "mic0"
            let format = Self.micFormat(audio)
            var audioFormat = Wendy_Lite_Sensorlink_AudioFormat()
            audioFormat.codec = .pcmS16Le
            audioFormat.sampleRate = format.sampleRate
            audioFormat.channels = format.channels
            descriptor.audio = audioFormat
            manifest.sensors.append(descriptor)
        }
        return ServerResponse(message: manifest)
    }

    func streamSensors(
        request: ServerRequest<Wendy_Agent_Services_V2_StreamSensorsRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Wendy_Lite_Sensorlink_SensorFrame> {
        let channels = Set(request.message.channelID)
        return StreamingServerResponse { writer in
            if channels.contains(Self.micChannel) {
                var seq: UInt32 = 0
                for try await chunk in audio.audio(deviceID: 0, sampleRate: 48000, channels: 1) {
                    var frame = Wendy_Lite_Sensorlink_SensorFrame()
                    frame.channelID = Self.micChannel
                    frame.seq = seq
                    seq &+= 1
                    frame.payload = chunk.pcm
                    try await writer.write(frame)
                }
            }
            return Metadata()
        }
    }

    /// `AudioController.audio` ignores the requested sample rate/channels and
    /// streams whatever the hardware input format actually is, so the manifest
    /// must report that real format rather than the request we happen to pass.
    /// Probing needs live CoreAudio/AVAudioEngine access, which only the real
    /// `AudioController` has — a fake used in tests gets a fixed, documented
    /// default instead of a live probe.
    private static func micFormat(_ audio: any AudioManaging) -> (
        sampleRate: UInt32, channels: UInt32
    ) {
        guard audio is AudioController else { return (sampleRate: 48000, channels: 1) }
        let format = AVAudioEngine().inputNode.inputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else { return (48000, 1) }
        return (sampleRate: UInt32(format.sampleRate), channels: UInt32(format.channelCount))
    }

    func micAuthorized() -> Bool {
        AVCaptureDevice.authorizationStatus(for: .audio) == .authorized
    }
}
