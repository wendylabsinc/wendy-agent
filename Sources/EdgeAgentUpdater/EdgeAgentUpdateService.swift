import EdgeAgentGRPC
import Foundation
import Logging
import NIOCore
import NIOFoundationCompat
import _NIOFileSystem

struct EdgeAgentService: Edge_Agent_Updater_Services_V1_EdgeAgentUpdateService.ServiceProtocol {
    let logger = Logger(label: "EdgeAgentUpdateService")
    let binaryPath: FilePath

    func updateAgent(
        request: StreamingServerRequest<Edge_Agent_Updater_Services_V1_UpdateAgentRequest>,
        context: ServerContext
    ) async throws -> StreamingServerResponse<Edge_Agent_Updater_Services_V1_UpdateAgentResponse> {
        logger.info("Updating agent")
        return StreamingServerResponse { writer in
            let filesystem = FileSystem.shared

            logger.info("Checking current binary at \(binaryPath)")
            guard
                let info = try await filesystem.info(forFileAt: binaryPath),
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

            logger.info("Applying update to \(binaryPath)")
            try await filesystem.removeItem(at: binaryPath)
            try await filesystem.moveItem(at: updateFile, to: binaryPath)

            logger.info("Restarting agent")

            try await writer.write(
                .with {
                    $0.updated = .init()
                }
            )

            return Metadata()
        }
    }
}
