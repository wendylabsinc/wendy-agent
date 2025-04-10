import ArgumentParser
import ContainerBuilder
import EdgeAgentGRPC
import EdgeCLI
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIO
import NIOFileSystem
import Shell

struct RunCommand: AsyncParsableCommand {
    enum Error: Swift.Error, CustomStringConvertible {
        case noExecutableTarget

        var description: String {
            switch self {
            case .noExecutableTarget:
                return "No executable target found in package"
            }
        }
    }

    static let configuration = CommandConfiguration(
        commandName: "run",
        abstract: "Run EdgeOS projects."
    )

    @Flag(name: .long, help: "Attach a debugger to the container")
    var debug: Bool = false

    @Flag(name: .long, help: "Run the container in the background")
    var detach: Bool = false

    @Option(name: .long, help: "The Swift SDK to use.")
    var swiftSDK: String = "aarch64-swift-linux-musl"

    @OptionGroup var agentConnectionOptions: AgentConnectionOptions

    func run() async throws {
        let logger = Logger(label: "apache-edge.cli.run")

        let swiftPM = SwiftPM()
        let package = try await swiftPM.dumpPackage()

        // For now, just use the first executable target.
        guard let executableTarget = package.targets.first(where: { $0.type == "executable" })
        else {
            throw Error.noExecutableTarget
        }

        try await swiftPM.build(
            .product(executableTarget.name),
            .swiftSDK(swiftSDK),
            .scratchPath(".edge-build")
        )

        let binPath = try await swiftPM.build(
            .showBinPath,
            .swiftSDK(swiftSDK),
            .quiet,
            .scratchPath(".edge-build")
        ).trimmingCharacters(in: .whitespacesAndNewlines)
        let executable = URL(fileURLWithPath: binPath).appendingPathComponent(executableTarget.name)

        logger.info("Building container")
        let imageName = executableTarget.name.lowercased()

        var imageSpec = ContainerImageSpec.withExecutable(executable: executable)

        if debug {

            // Include the ds2 executable in the container image.
            guard
                let ds2URL = Bundle.module.url(
                    forResource: "ds2-124963fd-static-linux-arm64",
                    withExtension: nil
                )
            else {
                fatalError("Could not find ds2 executable in bundle resources")
            }

            let ds2Files = [
                ContainerImageSpec.Layer.File(
                    source: ds2URL,
                    destination: "/bin/ds2",
                    permissions: 0o755
                )
            ]
            let ds2Layer = ContainerImageSpec.Layer(files: ds2Files)
            imageSpec.layers.insert(ds2Layer, at: 0)
        }

        let outputPath = "\(executableTarget.name)-container.tar"
        try await buildDockerContainerImage(
            image: imageSpec,
            imageName: imageName,
            outputPath: outputPath
        )

        let target = ResolvableTargets.DNS(
            host: agentConnectionOptions.agentHost,
            port: agentConnectionOptions.agentPort
        )
        #if os(macOS)
            let transport = try HTTP2ClientTransport.TransportServices(
                target: target,
                transportSecurity: .plaintext
            )
        #else
            let transport = try HTTP2ClientTransport.Posix(
                target: target,
                transportSecurity: .plaintext
            )
        #endif

        try await withGRPCClient(transport: transport) { client in
            let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
            try await agent.runContainer { writer in
                // First, send the header.
                try await writer.write(
                    .with {
                        $0.header.imageName = imageName
                    }
                )

                // Send the chunks
                let fileHandle = try await FileSystem.shared.openFile(
                    forReadingAt: FilePath(outputPath)
                )

                do {
                    for try await chunk in fileHandle.readChunks() {
                        try await writer.write(
                            .with {
                                $0.requestType = .chunk(
                                    .with { $0.data = Data(chunk.readableBytesView) }
                                )
                            }
                        )
                    }
                } catch {
                    try await fileHandle.close()
                    throw error
                }
                try await fileHandle.close()

                // Send the control command to start the container.
                try await writer.write(
                    .with {
                        $0.requestType = .control(
                            .with { $0.command = .run(.with { $0.debug = debug }) }
                        )
                    }
                )
            } onResponse: { response in
                for try await message in response.messages {
                    switch message.responseType {
                    case .started(let started):
                        if started.debugPort != 0 {
                            logger.info(
                                "Started container with debug port \(started.debugPort)"
                            )
                        } else {
                            logger.info("Started container")
                        }
                        if detach {
                            return
                        }
                    case nil:
                        logger.warning("Unknown message received from agent")
                    }
                }
            }
        }
    }
}
