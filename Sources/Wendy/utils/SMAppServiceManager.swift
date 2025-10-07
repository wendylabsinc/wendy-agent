import CliXPCProtocol
import Foundation
import Logging

#if os(macOS)
    import ServiceManagement

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

    /// Modern SMAppService-based daemon management
    public actor SMAppServiceManager {
        private let logger: Logger
        private let daemonIdentifier = "sh.wendy.wendy-network-daemon"

        public init(logger: Logger) {
            self.logger = logger
        }

        /// Register and start the privileged daemon using SMAppService
        public func installDaemon() async throws {
            logger.info("Installing wendy-network-daemon using SMAppService")

            // Create the daemon service
            let service = SMAppService.daemon(plistName: "\(daemonIdentifier).plist")

            do {
                // Register the daemon - this will prompt for Touch ID/password
                try service.register()
                logger.info("Successfully registered wendy-network-daemon")

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
            logger.info("Uninstalling wendy-network-daemon")

            let service = SMAppService.daemon(plistName: "\(daemonIdentifier).plist")

            do {
                try await service.unregister()
                logger.info("Successfully unregistered wendy-network-daemon")
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
        /// Note: This method is deprecated - use NetworkDaemonClient for XPC communication
        public func testConnection() async throws {
            logger.info("XPC connection testing moved to NetworkDaemonClient")
            // For now, just check if the daemon is enabled
            let status = getDaemonStatus()
            if status != .enabled {
                throw SMAppServiceError.connectionFailed
            }
        }

        /// Get detailed daemon status information
        public func getDaemonStatusInfo() async -> DaemonStatusInfo {
            let status = getDaemonStatus()

            // XPC connection testing has been moved to NetworkDaemonClient
            // This method now only returns SMAppService status
            return DaemonStatusInfo(
                status: status,
                isXPCConnected: status == .enabled,
                xpcError: nil
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

#else
    // Fallback for non-macOS platforms

    /// Errors for non-macOS platforms
    public enum PlatformError: Error, LocalizedError {
        case unsupportedPlatform

        public var errorDescription: String? {
            return "SMAppService is only available on macOS"
        }
    }

    public actor SMAppServiceManager {
        private let logger: Logger

        public init(logger: Logger) {
            self.logger = logger
        }

        public func installDaemon() async throws {
            logger.error("SMAppService is only available on macOS")
            throw PlatformError.unsupportedPlatform
        }

        public func uninstallDaemon() async throws {
            logger.error("SMAppService is only available on macOS")
            throw PlatformError.unsupportedPlatform
        }

        public func testConnection() async throws {
            logger.error("XPC is only available on macOS")
            throw PlatformError.unsupportedPlatform
        }

        public func getDaemonStatusInfo() async -> DaemonStatusInfo {
            return DaemonStatusInfo(
                status: .notFound,
                isXPCConnected: false,
                xpcError: PlatformError.unsupportedPlatform
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
