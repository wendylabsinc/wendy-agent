import ArgumentParser
import Crypto
import WendyAgentGRPC
import WendyShared
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import WendyAgentGRPC
import WendyShared
import ServiceLifecycle
import X509
import _NIOFileSystem

@main
struct WendyAgent: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "wendy-agent",
        abstract: "Wendy Agent",
        version: Version.current
    )

    @Option(name: .shortAndLong, help: "The port to listen on for incoming connections.")
    var port: Int = 50051

    @Option(name: .shortAndLong, help: "The directory to store configuration files in.")
    var configDir: String = "/etc/wendy-agent"

    func run() async throws {
        LoggingSystem.bootstrap { label in
            StreamLogHandler.standardError(label: label)
        }

        let logger = Logger(label: "sh.wendy.agent")

        logger.info("Starting Wendy Agent version \(Version.current) on port \(port)")

        let (signal, continuation) = AsyncStream<Void>.makeStream()

        let provisioning: WendyProvisioningService
        let config: any AgentConfigService = try await {
            try await FileSystemAgentConfigService(directory: FilePath(configDir))
        }()

        if let certificate = await config.certificate {
            provisioning = await WendyProvisioningService(
                privateKey: config.privateKey,
                deviceId: config.deviceId,
                certificate: certificate
            )
        } else {
            logger.notice("Agent requires provisioning")
            provisioning = await WendyProvisioningService(
                privateKey: config.privateKey,
                deviceId: config.deviceId
            ) { provisionedDevice in
                // TODO: Save to disk and restart server
                try await config.provisionCertificate(
                    provisionedDevice.certificate
                )
                logger.notice("Provisioning complete. Restarting server")
                continuation.yield()
            }
        }

        let services: [any GRPCCore.RegistrableRPCService] = [
            WendyContainerService(),
            WendyAgentService(shouldRestart: {
                print("Shutting down server")
                continuation.yield()
            }),
            provisioning,
        ]

        let serverIPv4 = GRPCServer(
            transport: GRPCNIOTransportHTTP2.HTTP2ServerTransport.Posix(
                address: .ipv4(host: "0.0.0.0", port: port),
                transportSecurity: .plaintext,
                config: .defaults
            ),
            services: services
        )

        let serverIPv6 = GRPCServer(
            transport: GRPCNIOTransportHTTP2.HTTP2ServerTransport.Posix(
                address: .ipv6(host: "::", port: port),
                transportSecurity: .plaintext,
                config: .defaults
            ),
            services: services
        )

        try await withThrowingTaskGroup(of: Void.self) { taskGroup in
            taskGroup.addTask {
                try await serverIPv4.serve()
                continuation.finish()
            }
            taskGroup.addTask {
                try await serverIPv6.serve()
                continuation.finish()
            }

            defer {
                serverIPv4.beginGracefulShutdown()
                serverIPv6.beginGracefulShutdown()
                taskGroup.cancelAll()
            }

            for try await () in signal {
                logger.info("Received signal, restarting")
                try await Task.sleep(for: .seconds(3))
                return
            }
        }
    }
}
