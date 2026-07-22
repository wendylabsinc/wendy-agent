import GRPCCore
import WendyAgentGRPC

struct AudioService: Wendy_Agent_Services_V1_WendyAudioService.ServiceProtocol {
    var audio: any AudioManaging = AudioController()

    func listAudioDevices(
        request: ServerRequest<Wendy_Agent_Services_V1_ListAudioDevicesRequest>,
        context: ServerContext
    ) async throws -> ServerResponse<Wendy_Agent_Services_V1_ListAudioDevicesResponse> {
        let filter = Self.modelKind(request.message.typeFilter)
        let devices: [AudioDeviceInfo]
        do {
            devices = try await audio.listDevices(typeFilter: filter)
        } catch {
            throw RPCError(code: .internalError, message: "\(error)")
        }

        var response = Wendy_Agent_Services_V1_ListAudioDevicesResponse()
        response.devices = devices.map { device in
            var proto = Wendy_Agent_Services_V1_AudioDevice()
            proto.id = device.id
            proto.name = device.name
            proto.type = Self.protoKind(device.kind)
            proto.isDefault = device.isDefault
            return proto
        }
        return ServerResponse(message: response)
    }

    func setDefaultAudioDevice(
        request: ServerRequest<Wendy_Agent_Services_V1_SetDefaultAudioDeviceRequest>,
        context: ServerContext
    ) async throws -> ServerResponse<Wendy_Agent_Services_V1_SetDefaultAudioDeviceResponse> {
        do {
            try await audio.setDefault(deviceID: request.message.deviceID)
        } catch {
            throw RPCError(code: .internalError, message: "\(error)")
        }
        return ServerResponse(message: Wendy_Agent_Services_V1_SetDefaultAudioDeviceResponse())
    }

    func streamAudioLevels(
        request: ServerRequest<Wendy_Agent_Services_V1_StreamAudioLevelsRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Wendy_Agent_Services_V1_AudioLevelUpdate> {
        let deviceID = request.message.deviceID
        let rateHz = request.message.updateRateHz
        let stream = audio.levels(deviceID: deviceID, rateHz: rateHz)
        return StreamingServerResponse { writer in
            for try await sample in stream {
                var update = Wendy_Agent_Services_V1_AudioLevelUpdate()
                update.peakDb = sample.peakDb
                update.rmsDb = sample.rmsDb
                try await writer.write(update)
            }
            return Metadata()
        }
    }

    func streamAudio(
        request: ServerRequest<Wendy_Agent_Services_V1_StreamAudioRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Wendy_Agent_Services_V1_AudioChunk> {
        let message = request.message
        let stream = audio.audio(
            deviceID: message.deviceID,
            sampleRate: message.sampleRate,
            channels: message.channels
        )
        return StreamingServerResponse { writer in
            for try await chunk in stream {
                var proto = Wendy_Agent_Services_V1_AudioChunk()
                proto.pcmData = chunk.pcm
                proto.sampleRate = chunk.sampleRate
                proto.channels = chunk.channels
                try await writer.write(proto)
            }
            return Metadata()
        }
    }

    // MARK: - Type mapping

    static func modelKind(_ type: Wendy_Agent_Services_V1_AudioDeviceType) -> AudioKind? {
        switch type {
        case .input: return .input
        case .output: return .output
        case .unspecified, .UNRECOGNIZED: return nil
        }
    }

    static func protoKind(_ kind: AudioKind) -> Wendy_Agent_Services_V1_AudioDeviceType {
        switch kind {
        case .input: return .input
        case .output: return .output
        }
    }
}
