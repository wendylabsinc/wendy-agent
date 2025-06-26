import EdgeAgentGRPC
import EdgeShared
import Foundation
import Logging
import NIOCore
import NIOFoundationCompat
import _NIOFileSystem

struct EdgeAgentService: Edge_Agent_Services_V1_EdgeAgentService.ServiceProtocol {
    let logger = Logger(label: "EdgeAgentService")
    let shouldRestart: @Sendable () async throws -> Void
    let currentUID: String

    init(shouldRestart: @escaping @Sendable () async throws -> Void) {
        self.shouldRestart = shouldRestart
        self.currentUID = String(getuid())
    }

    func runContainer(
        request: StreamingServerRequest<Edge_Agent_Services_V1_RunContainerRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_RunContainerResponse> {
        return StreamingServerResponse {
            (
                writer: RPCWriter<Edge_Agent_Services_V1_RunContainerResponse>
            ) async throws -> Metadata in
            try await withThrowingDiscardingTaskGroup { group in
                var handler = RunContainerRequestHandler()

                // Add a task to write outgoing events to the response.
                group.addTask { [events = handler.events] in
                    for try await event in events {
                        logger.debug("Sending event: \(event)")
                        try await writer.write(event.proto)
                    }
                }

                do {
                    // Iterate over incoming messages, converting each from protobuf before passing it
                    // to the request handler.
                    for try await message in request.messages {
                        switch message.requestType {
                        case .header(let header):
                            let header = try RunContainerRequestHandler.Header(validating: header)
                            try await handler.handle(header)
                        case .chunk(let chunk):
                            let chunk = try RunContainerRequestHandler.Chunk(validating: chunk)
                            try await handler.handle(chunk)
                        case .control(let control):
                            let control = try RunContainerRequestHandler.ControlCommand(
                                validating: control
                            )
                            try await handler.handle(control)
                        case nil:
                            throw RPCError(
                                code: .invalidArgument,
                                message: "Invalid request: Unknown message type"
                            )
                        }
                    }
                    await handler.cleanup()
                } catch {
                    await handler.cleanup()
                    throw error
                }
            }

            return Metadata()
        }
    }

    func updateAgent(
        request: StreamingServerRequest<Edge_Agent_Services_V1_UpdateAgentRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Services_V1_UpdateAgentResponse> {
        logger.info("Updating agent")
        return StreamingServerResponse { writer in
            let currentBinary = FilePath(ProcessInfo.processInfo.arguments[0])
            let filesystem = FileSystem.shared

            logger.info("Checking current binary at \(currentBinary)")
            guard
                let info = try await filesystem.info(forFileAt: currentBinary),
                info.type == .regular
            else {
                logger.error("Current binary is not a regular file")
                throw RPCError(
                    code: .invalidArgument,
                    message: "Invalid request: Current binary is not a regular file"
                )
            }

            logger.info("Creating temporary directory")
            let tempDir = try await filesystem.createTemporaryDirectory(
                template: "edge-agent-update-XXX"
            )
            let updateFile = tempDir.appending("edge-agent")

            logger.info("Writing update to \(updateFile)")
            try await filesystem.withFileHandle(
                forReadingAndWritingAt: updateFile,
                options: .newFile(
                    replaceExisting: true,
                    permissions: [.ownerReadWriteExecute, .groupReadExecute, .otherReadExecute]
                )
            ) { writer in
                var bufferedWriter = writer.bufferedWriter()
                for try await event in request.messages {
                    switch event.requestType {
                    case .chunk(let chunk):
                        try await bufferedWriter.write(contentsOf: ByteBuffer(data: chunk.data))
                    case .control:
                        logger.info("Received control command, binary is written")
                        return
                    case .none:
                        // Unknown, ignore.
                        ()
                    }
                }
                try await bufferedWriter.flush()
            }

            logger.info("Applying update to \(currentBinary)")
            try await filesystem.removeItem(at: currentBinary)
            try await filesystem.moveItem(at: updateFile, to: currentBinary)

            logger.info("Restarting agent")
            try await shouldRestart()

            try await writer.write(
                .with {
                    $0.updated = .init()
                }
            )

            return Metadata()
        }
    }

    func getAgentVersion(
        request: GRPCCore.ServerRequest<Edge_Agent_Services_V1_GetAgentVersionRequest>,
        context: GRPCCore.ServerContext
    ) async throws -> GRPCCore.ServerResponse<Edge_Agent_Services_V1_GetAgentVersionResponse> {
        return ServerResponse(
            message: .with {
                $0.version = Version.current
            }
        )
    }

    func listWiFiNetworks(
        request: GRPCCore.ServerRequest<Edge_Agent_Services_V1_ListWiFiNetworksRequest>,
        context: GRPCCore.ServerContext
    ) async throws -> GRPCCore.ServerResponse<Edge_Agent_Services_V1_ListWiFiNetworksResponse> {
        logger.info("Listing available WiFi networks")

        do {
            let networkManager = NetworkManager(uid: currentUID)
            let wifiNetworks = try await networkManager.listWiFiNetworks()

            logger.info("Found \(wifiNetworks.count) WiFi networks")

            return ServerResponse(
                message: .with { response in
                    response.networks = wifiNetworks.map { network in
                        .with { protoNetwork in
                            protoNetwork.ssid = network.ssid
                            // Include signal strength if available
                            if let signalStrength = network.signalStrength {
                                protoNetwork.signalStrength = Int32(signalStrength)
                            }
                        }
                    }
                }
            )
        } catch {
            logger.error("Failed to list WiFi networks: \(error)")
            throw RPCError(
                code: .internalError,
                message: "Failed to list WiFi networks: \(error.localizedDescription)"
            )
        }
    }

    func connectToWiFi(
        request: GRPCCore.ServerRequest<Edge_Agent_Services_V1_ConnectToWiFiRequest>,
        context: GRPCCore.ServerContext
    ) async throws -> GRPCCore.ServerResponse<Edge_Agent_Services_V1_ConnectToWiFiResponse> {
        let ssid = request.message.ssid
        let password = request.message.password

        logger.info("Connecting to WiFi network", metadata: ["ssid": "\(ssid)"])

        do {
            let networkManager = NetworkManager(uid: currentUID)
            try await networkManager.setupWiFi(ssid: ssid, password: password)

            if let (ssid, _) = try await networkManager.getCurrentConnection() {
                logger.info("Successfully connected to WiFi network", metadata: ["ssid": "\(ssid)"])
                return ServerResponse(
                    message: .with { $0.success = true }
                )
            } else {
                logger.warning("Failed to connect to WiFi network", metadata: ["ssid": "\(ssid)"])
                return ServerResponse(
                    message: .with {
                        $0.success = false
                        $0.errorMessage = "Connection failed"
                    }
                )
            }
        } catch {
            logger.error(
                "Error connecting to WiFi network",
                metadata: [
                    "ssid": "\(ssid)",
                    "error": "\(error)",
                ]
            )

            return ServerResponse(
                message: .with {
                    $0.success = false
                    $0.errorMessage = error.localizedDescription
                }
            )
        }
    }

    func getWiFiStatus(
        request: GRPCCore.ServerRequest<Edge_Agent_Services_V1_GetWiFiStatusRequest>,
        context: GRPCCore.ServerContext
    ) async throws -> GRPCCore.ServerResponse<Edge_Agent_Services_V1_GetWiFiStatusResponse> {
        logger.info("Getting WiFi connection status")

        do {
            let networkManager = NetworkManager(uid: currentUID)
            let connectionInfo = try await networkManager.getCurrentConnection()

            if let connectionInfo = connectionInfo {
                logger.info(
                    "Currently connected to WiFi network",
                    metadata: ["ssid": "\(connectionInfo.ssid)"]
                )
                return ServerResponse(
                    message: .with { response in
                        response.connected = true
                        response.ssid = connectionInfo.ssid
                    }
                )
            } else {
                logger.info("Not connected to any WiFi network")
                return ServerResponse(
                    message: .with { response in
                        response.connected = false
                    }
                )
            }
        } catch {
            logger.error("Error getting WiFi status: \(error)")

            return ServerResponse(
                message: .with { response in
                    response.connected = false
                    response.errorMessage = error.localizedDescription
                }
            )
        }
    }

    func disconnectWiFi(
        request: GRPCCore.ServerRequest<Edge_Agent_Services_V1_DisconnectWiFiRequest>,
        context: GRPCCore.ServerContext
    ) async throws -> GRPCCore.ServerResponse<Edge_Agent_Services_V1_DisconnectWiFiResponse> {
        logger.info("Disconnecting from WiFi network")

        do {
            let networkManager = NetworkManager(uid: currentUID)

            // Check current connection first
            if let connectionInfo = try await networkManager.getCurrentConnection() {
                logger.info(
                    "Disconnecting from WiFi network",
                    metadata: ["ssid": "\(connectionInfo.ssid)"]
                )

                let success = try await networkManager.disconnectFromNetwork()

                if success {
                    logger.info(
                        "Successfully disconnected from WiFi network",
                        metadata: ["ssid": "\(connectionInfo.ssid)"]
                    )
                    return ServerResponse(
                        message: .with { $0.success = true }
                    )
                } else {
                    logger.warning(
                        "Failed to disconnect from WiFi network",
                        metadata: ["ssid": "\(connectionInfo.ssid)"]
                    )
                    return ServerResponse(
                        message: .with {
                            $0.success = false
                            $0.errorMessage = "Disconnection failed"
                        }
                    )
                }
            } else {
                logger.info("Not connected to any WiFi network")
                return ServerResponse(
                    message: .with {
                        $0.success = true
                    }
                )
            }
        } catch NetworkManagerError.noActiveConnection {
            // Not being connected is not an error for disconnection
            logger.info("Not connected to any WiFi network")
            return ServerResponse(
                message: .with {
                    $0.success = true
                }
            )
        } catch {
            logger.error("Error disconnecting from WiFi network: \(error)")

            return ServerResponse(
                message: .with {
                    $0.success = false
                    $0.errorMessage = error.localizedDescription
                }
            )
        }
    }
}
