import CliXPCProtocol
import Foundation
import Logging

#if os(macOS)
    import ServiceManagement

    /// Modern SMAppService-based daemon management
    public actor SMAppServiceManager {
        private let logger: Logger
        private let daemonIdentifier = "com.edgeos.edge-network-daemon"

        public init(logger: Logger) {
            self.logger = logger
        }

        /// Register and start the privileged daemon using SMAppService
        public func installDaemon() async throws {
            logger.info("Installing edge-network-daemon using SMAppService")

            // Create the daemon service
            let service = SMAppService.daemon(plistName: "\(daemonIdentifier).plist")

            do {
                // Register the daemon - this will prompt for Touch ID/password
                try service.register()
                logger.info("Successfully registered edge-network-daemon")

                // Wait a moment for the service to start
                try await Task.sleep(for: .seconds(2))

                // Verify it's running
                let status = service.status
                logger.info("Daemon status after registration: \(status.rawValue)")

                switch status {
                case .enabled:
                    logger.info("✅ Daemon is enabled and should be running")
                case .requiresApproval:
                    logger.warning("⚠️ Daemon requires user approval in System Settings")
                    throw SMAppServiceError.requiresApproval
                case .notRegistered:
                    throw SMAppServiceError.registrationFailed
                case .notFound:
                    throw SMAppServiceError.daemonNotFound
                @unknown default:
                    logger.warning("Unknown daemon status: \(status.rawValue)")
                }

            } catch {
                logger.error("Failed to register daemon: \(error)")
                throw error
            }
        }

        /// Unregister the daemon
        public func uninstallDaemon() async throws {
            logger.info("Uninstalling edge-network-daemon")

            let service = SMAppService.daemon(plistName: "\(daemonIdentifier).plist")

            do {
                try await service.unregister()
                logger.info("Successfully unregistered edge-network-daemon")
            } catch {
                logger.error("Failed to unregister daemon: \(error)")
                throw error
            }
        }

        /// Get current daemon status
        public func getDaemonStatus() -> SMAppService.Status {
            let service = SMAppService.daemon(plistName: "\(daemonIdentifier).plist")
            return service.status
        }

        /// Test XPC connection to the daemon
        public func testConnection() async throws {
            logger.info("Testing XPC connection to daemon")

            let connection = NSXPCConnection(
                machServiceName: kEdgeNetworkDaemonServiceName,
                options: .privileged
            )
            connection.remoteObjectInterface = NSXPCInterface(with: EdgeNetworkDaemonProtocol.self)
            connection.resume()

            defer { connection.invalidate() }

            let remoteProxy = connection.remoteObjectProxy as? EdgeNetworkDaemonProtocol

            return try await withCheckedThrowingContinuation { continuation in
                remoteProxy?.handshake { success, error in
                    if success {
                        continuation.resume()
                    } else {
                        continuation.resume(throwing: error ?? XPCError.connectionFailed)
                    }
                }
            }
        }

        /// Get detailed daemon status information
        public func getDaemonStatusInfo() async -> DaemonStatusInfo {
            let status = getDaemonStatus()

            var xpcConnected = false
            var xpcError: Error?

            // Test XPC connection if daemon is enabled
            if status == .enabled {
                do {
                    try await testConnection()
                    xpcConnected = true
                } catch {
                    xpcError = error
                }
            }

            return DaemonStatusInfo(
                status: status,
                isXPCConnected: xpcConnected,
                xpcError: xpcError
            )
        }
    }

    /// Detailed daemon status information
    public struct DaemonStatusInfo: Sendable {
        public let status: SMAppService.Status
        public let isXPCConnected: Bool
        public let xpcError: Error?

        public var statusDescription: String {
            switch status {
            case .enabled:
                return isXPCConnected ? "Running" : "Enabled but not responding"
            case .requiresApproval:
                return "Requires Approval"
            case .notRegistered:
                return "Not Installed"
            case .notFound:
                return "Daemon Not Found"
            @unknown default:
                return "Unknown (\(status.rawValue))"
            }
        }
    }

    /// Errors for SMAppService operations
    public enum SMAppServiceError: Error, LocalizedError {
        case registrationFailed
        case requiresApproval
        case daemonNotFound
        case connectionFailed

        public var errorDescription: String? {
            switch self {
            case .registrationFailed:
                return "Failed to register daemon with SMAppService"
            case .requiresApproval:
                return "Daemon requires approval in System Settings > General > Login Items"
            case .daemonNotFound:
                return "Daemon executable not found"
            case .connectionFailed:
                return "Failed to connect to daemon via XPC"
            }
        }
    }

#else
    // Fallback for non-macOS platforms
    public actor SMAppServiceManager {
        private let logger: Logger

        public init(logger: Logger) {
            self.logger = logger
        }

        public func installDaemon() async throws {
            logger.error("SMAppService is only available on macOS")
            throw SMAppServiceError.registrationFailed
        }

        public func uninstallDaemon() async throws {
            logger.error("SMAppService is only available on macOS")
            throw SMAppServiceError.registrationFailed
        }

        public func testConnection() async throws {
            logger.error("XPC is only available on macOS")
            throw SMAppServiceError.connectionFailed
        }

        public func getDaemonStatusInfo() async -> DaemonStatusInfo {
            return DaemonStatusInfo(
                status: .notFound,
                isXPCConnected: false,
                xpcError: SMAppServiceError.registrationFailed
            )
        }
    }

    // Provide minimal types for non-macOS
    public struct DaemonStatusInfo {
        public let status: MockStatus
        public let isXPCConnected: Bool
        public let xpcError: Error?

        public var statusDescription: String { "Unsupported Platform" }
    }

    public enum MockStatus: Int {
        case notFound = 0
    }
#endif
