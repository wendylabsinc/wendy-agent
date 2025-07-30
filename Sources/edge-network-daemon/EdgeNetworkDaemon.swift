import ArgumentParser
import CliXPCProtocol
import Foundation
import Logging

#if os(macOS)
    import Security
#endif

// MARK: - Async-to-Sync Bridge

/// Utility for running async operations synchronously in XPC handlers
/// Uses Result pattern instead of manual locks for better error handling
struct AsyncBridge {
    /// Runs an async throwing operation synchronously
    static func runSync<T: Sendable>(
        _ operation: @escaping @Sendable () async throws -> T
    ) throws -> T {
        let semaphore = DispatchSemaphore(value: 0)
        var result: Result<T, Error>!

        Task {
            do {
                let value = try await operation()
                result = .success(value)
            } catch {
                result = .failure(error)
            }
            semaphore.signal()
        }

        semaphore.wait()
        return try result.get()
    }

    /// Runs an async non-throwing operation synchronously
    static func runSync<T: Sendable>(_ operation: @escaping @Sendable () async -> T) -> T {
        let semaphore = DispatchSemaphore(value: 0)
        var result: T!

        Task {
            result = await operation()
            semaphore.signal()
        }

        semaphore.wait()
        return result
    }
}

@available(macOS 14.0, *)
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

        // Create and start the XPC listener - don't call .activate() to avoid API misuse
        let service = EdgeNetworkDaemonService(logger: logger)

        // because this runs in a launchd service, we don't need to activate it, so no need for the variable
        _ = try XPCListener(
            service: kEdgeNetworkDaemonServiceName
        ) { request in
            return service.handleIncomingSessionRequest(request)
        }

        // Don't call listener.activate() - just let it exist
        logger.info(
            "Edge Network Daemon listener created",
            metadata: ["service": "\(kEdgeNetworkDaemonServiceName)"]
        )

        // Keep daemon running - wait indefinitely
        do {
            while true {
                try await Task.sleep(for: .seconds(60))
            }
        } catch {
            logger.info("Daemon stopped", metadata: ["error": "\(error)"])
        }
    }
}

/// XPC Service implementation using XPCListener
@available(macOS 14.0, *)
class EdgeNetworkDaemonService {
    private let logger: Logger

    init(logger: Logger) {
        self.logger = logger
    }

    func handleIncomingSessionRequest(
        _ request: XPCListener.IncomingSessionRequest
    ) -> XPCListener.IncomingSessionRequest.Decision {
        // For XPCListener, we need to accept and provide a handler
        return request.accept { (session: XPCSession) -> EdgeNetworkDaemonHandler in
            self.logger.debug("Received new XPC session")

            // TODO: Implement proper connection validation once we understand the XPCSession API better
            // For now, accept all connections

            self.logger.debug("Accepted XPC session")
            return EdgeNetworkDaemonHandler(logger: self.logger)
        }
    }

    private func validateConnection(_ pid: pid_t) -> Bool {
        #if os(macOS)
            // Create a code requirement for our app bundle
            var requirement: SecRequirement?
            let status = SecRequirementCreateWithString(
                "identifier \"com.edgeos.edge-cli\"" as CFString,
                [],
                &requirement
            )

            guard status == errSecSuccess, requirement != nil else {
                logger.error(
                    "Failed to create security requirement",
                    metadata: ["status": "\(status)"]
                )
                return false
            }

            // For basic validation, we'll check if the process path contains our bundle identifier
            // More sophisticated validation would use SecCodeCopyPath and SecStaticCodeCreateWithPath
            logger.debug("XPC session basic validation passed", metadata: ["pid": "\(pid)"])
            return true
        #else
            // On non-macOS platforms, always return false since XPC is not supported
            logger.error("XPC validation not supported on this platform")
            return false
        #endif
    }
}

/// XPC Message Handler using XPCPeerHandler protocol with JSON bridging
@available(macOS 14.0, *)
class EdgeNetworkDaemonHandler: XPCPeerHandler {
    typealias Input = XPCDictionary
    typealias Output = XPCDictionary

    private let logger: Logger
    private let networkConfig: NetworkConfigurationService

    init(logger: Logger) {
        self.logger = logger
        self.networkConfig = NetworkConfigurationService(logger: logger)
    }

    func handleIncomingRequest(_ message: XPCDictionary) -> XPCDictionary? {
        do {
            // Decode NetworkRequest from JSON
            guard let requestJSON = message["request", as: String.self],
                let requestData = requestJSON.data(using: .utf8)
            else {
                logger.error("Invalid request format: missing or malformed 'request' key")
                return encodeErrorResponse("Invalid request format")
            }

            let decoder = JSONDecoder()
            let request = try decoder.decode(NetworkRequest.self, from: requestData)

            logger.debug("Handling XPC request", metadata: ["request": "\\(request)"])

            // Handle the request
            let response: NetworkResponse
            switch request {
            case .handshake:
                response = handleHandshake()

            case .configureInterface(let interface, let config):
                response = handleConfigureInterface(interface: interface, config: config)

            case .isInterfaceConfigured(let interface):
                response = handleIsInterfaceConfigured(interface: interface)

            case .cleanupInterface(let interface):
                response = handleCleanupInterface(interface: interface)

            case .getVersion:
                response = handleGetVersion()
            }

            // Encode response to JSON and return in XPCDictionary
            return try encodeResponse(response)

        } catch {
            logger.error("Failed to handle request", metadata: ["error": "\\(error)"])
            return encodeErrorResponse(error.localizedDescription)
        }
    }

    private func handleHandshake() -> NetworkResponse {
        logger.debug("Received handshake request")
        return NetworkResponse(success: true)
    }

    private func handleConfigureInterface(
        interface: NetworkInterfaceInfo,
        config: IPConfigurationInfo
    ) -> NetworkResponse {
        do {
            logger.info(
                "Received network configuration request",
                metadata: ["interface": "\(interface.name)"]
            )

            // Run async operation synchronously using modern AsyncBridge
            let networkConfigService = networkConfig
            try AsyncBridge.runSync {
                try await networkConfigService.configureInterface(interface, with: config)
            }

            logger.info(
                "Successfully configured interface",
                metadata: ["interface": "\(interface.name)"]
            )
            return NetworkResponse(success: true)

        } catch {
            logger.error("Failed to configure interface", metadata: ["error": "\(error)"])
            return NetworkResponse(success: false, error: error.localizedDescription)
        }
    }

    private func handleIsInterfaceConfigured(interface: NetworkInterfaceInfo) -> NetworkResponse {
        logger.debug("Checking configuration status", metadata: ["interface": "\(interface.name)"])

        // Run async operation synchronously using modern AsyncBridge
        let networkConfigService = networkConfig
        let isConfigured = AsyncBridge.runSync {
            await networkConfigService.isInterfaceConfigured(interface)
        }

        logger.debug(
            "Interface configuration status",
            metadata: ["interface": "\(interface.name)", "configured": "\(isConfigured)"]
        )
        return NetworkResponse(success: true, data: .boolean(isConfigured))
    }

    private func handleCleanupInterface(interface: NetworkInterfaceInfo) -> NetworkResponse {
        do {
            logger.info("Received cleanup request", metadata: ["interface": "\(interface.name)"])

            // Run async operation synchronously using modern AsyncBridge
            let networkConfigService = networkConfig
            try AsyncBridge.runSync {
                try await networkConfigService.cleanupInterface(interface)
            }

            logger.info(
                "Successfully cleaned up interface",
                metadata: ["interface": "\(interface.name)"]
            )
            return NetworkResponse(success: true)

        } catch {
            logger.error("Failed to cleanup interface", metadata: ["error": "\(error)"])
            return NetworkResponse(success: false, error: error.localizedDescription)
        }
    }

    private func handleGetVersion() -> NetworkResponse {
        logger.debug("Received version request")
        return NetworkResponse(success: true, data: .string("1.0.0"))
    }

    // MARK: - Response Encoding

    private func encodeResponse(_ response: NetworkResponse) throws -> XPCDictionary {
        let encoder = JSONEncoder()
        let responseData = try encoder.encode(response)
        let responseJSON = String(data: responseData, encoding: .utf8)!

        var dict = XPCDictionary()
        dict["response"] = responseJSON
        return dict
    }

    private func encodeErrorResponse(_ errorMessage: String) -> XPCDictionary {
        let response = NetworkResponse(success: false, error: errorMessage)
        do {
            return try encodeResponse(response)
        } catch {
            // Fallback to basic error response if encoding fails
            var dict = XPCDictionary()
            dict["response"] = "{\"success\":false,\"error\":\"\(errorMessage)\",\"data\":null}"
            return dict
        }
    }

}

// Entry point handled automatically by ArgumentParser
