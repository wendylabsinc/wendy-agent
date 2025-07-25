import ArgumentParser
import CliXPCProtocol
import Foundation
import Logging

@main
struct EdgeNetworkDaemon: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "edge-network-daemon",
        abstract: "EdgeOS privileged network configuration daemon",
        version: "1.0.0"
    )

    @Flag(name: .shortAndLong, help: "Enable debug logging")
    var debug = false

    func run() async throws {
        // Set up logging
        let logLevel: Logger.Level = debug ? .debug : .info
        LoggingSystem.bootstrap { label in
            var handler = StreamLogHandler.standardError(label: label)
            handler.logLevel = logLevel
            return handler
        }

        let logger = Logger(label: "edge-network-daemon")

        logger.info("Starting Edge Network Daemon")

        // Create and start the XPC listener
        let service = EdgeNetworkDaemonService(logger: logger)
        let listener = NSXPCListener(machServiceName: kEdgeNetworkDaemonServiceName)
        listener.delegate = service
        listener.resume()

        logger.info("Edge Network Daemon listening on: \(kEdgeNetworkDaemonServiceName)")

        // Keep daemon running - wait indefinitely
        do {
            while true {
                try await Task.sleep(for: .seconds(60))
            }
        } catch {
            logger.info("Daemon stopped: \(error)")
        }
    }
}

/// XPC Service implementation
class EdgeNetworkDaemonService: NSObject, NSXPCListenerDelegate {
    private let logger: Logger

    init(logger: Logger) {
        self.logger = logger
        super.init()
    }

    func listener(
        _ listener: NSXPCListener,
        shouldAcceptNewConnection newConnection: NSXPCConnection
    ) -> Bool {
        logger.debug("Received new XPC connection from PID: \(newConnection.processIdentifier)")

        // Set up the connection
        newConnection.exportedInterface = NSXPCInterface(with: EdgeNetworkDaemonProtocol.self)

        let exportedObject = EdgeNetworkDaemonImplementation(logger: logger)
        newConnection.exportedObject = exportedObject

        newConnection.invalidationHandler = { [weak self] in
            self?.logger.debug("XPC connection invalidated")
        }

        newConnection.interruptionHandler = { [weak self] in
            self?.logger.debug("XPC connection interrupted")
        }

        newConnection.resume()
        return true
    }
}

/// Implementation of the XPC protocol
class EdgeNetworkDaemonImplementation: NSObject, EdgeNetworkDaemonProtocol {
    private let logger: Logger

    init(logger: Logger) {
        self.logger = logger
        super.init()
    }

    func handshake(completion: @escaping (Bool, Error?) -> Void) {
        logger.debug("Received handshake request")
        completion(true, nil)
    }

    func configureNetwork(
        authorizationData: Data,
        interfaceName: String,
        ipAddress: String,
        completion: @escaping (Bool, Error?) -> Void
    ) {
        logger.info("Received network configuration request for interface: \(interfaceName)")

        // For now, just log the request - we'll implement the actual logic later
        logger.info("Would configure interface \(interfaceName) with IP \(ipAddress)")
        logger.info("Authorization data size: \(authorizationData.count) bytes")

        // Return success for now
        completion(true, nil)
    }

    func getVersion(completion: @escaping (String?, Error?) -> Void) {
        completion("1.0.0", nil)
    }
}
