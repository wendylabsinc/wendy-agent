import ArgumentParser
import ContainerBuilder
import ContainerRegistry
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
        case invalidExecutableTarget(String)
        case multipleExecutableTargets([String])

        var description: String {
            switch self {
            case .noExecutableTarget:
                return "No executable target found in package"
            case .invalidExecutableTarget(let name):
                return "No executable target named '\(name)' found in package"
            case .multipleExecutableTargets(let names):
                return
                    "multiple executable targets available, but none specified: \(names.joined(separator: ", "))"
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
    var swiftSDK: String = "6.1-RELEASE_edgeos_aarch64"

    @Option(name: .long, help: "The base image to use. Defaults to debian:bookworm-slim.")
    var baseImage: String = "debian:bookworm-slim"

    @Argument(
        help: "The executable to run. Required when a package has multiple executable targets."
    )
    var executable: String?

    @OptionGroup var agentConnectionOptions: AgentConnectionOptions

    func run() async throws {
        let logger = Logger(label: "edgeengineer.cli.run")

        let swiftPM = SwiftPM()
        let package = try await swiftPM.dumpPackage(
            .scratchPath(".edge-build")
        )

        // Get all executable targets
        let executableTargets = package.targets.filter { $0.type == "executable" }

        // Use specified executable or handle multiple executable targets
        let executableTarget: SwiftPM.Package.Target
        if let executableName = executable {
            guard let target = executableTargets.first(where: { $0.name == executableName }) else {
                throw Error.invalidExecutableTarget(executableName)
            }
            executableTarget = target
        } else {
            // If no executable specified, ensure there's only one executable target
            if executableTargets.isEmpty {
                throw Error.noExecutableTarget
            } else if executableTargets.count > 1 {
                throw Error.multipleExecutableTargets(executableTargets.map(\.name))
            } else {
                executableTarget = executableTargets[0]
            }
        }

        try await swiftPM.build(
            .product(executableTarget.name),
            .swiftSDK(swiftSDK),
            .scratchPath(".edge-build"),
            .staticSwiftStdlib
        )

        let binPath = try await swiftPM.build(
            .showBinPath,
            .product(executableTarget.name),
            .swiftSDK(swiftSDK),
            .quiet,
            .scratchPath(".edge-build"),
            .staticSwiftStdlib
        ).trimmingCharacters(in: .whitespacesAndNewlines)
        let executable = URL(fileURLWithPath: binPath).appendingPathComponent(executableTarget.name)

        logger.info("Building container with base image \(baseImage)")
        let imageName = executableTarget.name.lowercased()

        // Use the debian:bookworm-slim base image instead of a blank image
        var imageSpec = try await ContainerImageSpec.withBaseImage(
            baseImage: baseImage,
            executable: executable
        )

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
            imageSpec.layers.append(ds2Layer)
        }

        let outputPath = "\(executableTarget.name)-container.tar"
        try await buildDockerContainerImage(
            image: imageSpec,
            imageName: imageName,
            outputPath: outputPath
        )

        try await withGRPCClient(agentConnectionOptions) { client in
            let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
            try await agent.runContainer { writer in
                // First, send the header.
                try await writer.write(
                    .with {
                        $0.header.imageName = imageName
                    }
                )

                // Send the chunks
                logger.info("Sending container image to agent")
                try await FileSystem.shared.withFileHandle(forReadingAt: FilePath(outputPath)) {
                    fileHandle in
                    for try await chunk in fileHandle.readChunks() {
                        try await writer.write(
                            .with {
                                $0.requestType = .chunk(
                                    .with { $0.data = Data(chunk.readableBytesView) }
                                )
                            }
                        )
                    }
                }

                // Send the control command to start the container.
                logger.info("Sending control command to start container")
                try await writer.write(
                    .with {
                        $0.requestType = .control(
                            .with { $0.command = .run(.with { run in
                                run.debug = debug
                                run.entitlements = .with { entitlements in
                                    entitlements.entitlements.append(.with { entitlement in
                                        entitlement.dbus = .init()
                                    })
                                }
                            }) }
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
