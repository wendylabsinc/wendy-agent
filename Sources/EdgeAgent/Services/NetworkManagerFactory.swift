import DBUS
import Logging
import NIO

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Factory for creating appropriate network manager instances based on availability
public actor NetworkManagerFactory {
    private let logger = Logger(label: "NetworkManagerFactory")
    private let uid: String
    private let socketPath: String

    /// Cached detection result
    private var cachedManager: NetworkManagerType?

    public enum NetworkManagerType: String, Sendable {
        case connMan = "ConnMan"
        case networkManager = "NetworkManager"
        case none = "None"
    }

    /// User preference for network manager (if set)
    public enum Preference: Sendable {
        case auto
        case preferConnMan
        case preferNetworkManager
        case forceConnMan
        case forceNetworkManager
    }

    public init(
        uid: String,
        socketPath: String = "/var/run/dbus/system_bus_socket"
    ) {
        self.uid = uid
        self.socketPath = socketPath
    }

    /// Check if a D-Bus service is available
    private func isServiceAvailable(_ serviceName: String) async -> Bool {
        do {
            let address = try SocketAddress(unixDomainSocketPath: socketPath)

            return try await DBusClient.withConnection(
                to: address,
                auth: .external(userID: uid)
            ) { connection in
                let request = DBusRequest.createMethodCall(
                    destination: "org.freedesktop.DBus",
                    path: "/org/freedesktop/DBus",
                    interface: "org.freedesktop.DBus",
                    method: "NameHasOwner",
                    body: [.string(serviceName)]
                )

                guard let reply = try await connection.send(request),
                      case .methodReturn = reply.messageType,
                      let bodyValue = reply.body.first else {
                    return false
                }

                switch bodyValue {
                case .boolean(let hasOwner):
                    return hasOwner
                case .variant(let variant):
                    if case .boolean(let hasOwner) = variant.value {
                        return hasOwner
                    }
                    return false
                default:
                    return false
                }
            }
        } catch {
            logger.debug("Failed to check service availability", metadata: [
                "service": "\(serviceName)",
                "error": "\(error)"
            ])
            return false
        }
    }

    /// Detect which network manager is available
    private func detectNetworkManager() async -> NetworkManagerType {
        if let cached = cachedManager {
            logger.debug("Using cached network manager detection", metadata: [
                "manager": "\(cached.rawValue)"
            ])
            return cached
        }

        logger.info("Detecting available network manager")

        let connManAvailable = await isServiceAvailable("net.connman")
        let networkManagerAvailable = await isServiceAvailable("org.freedesktop.NetworkManager")

        let detected: NetworkManagerType
        if connManAvailable {
            detected = .connMan
            logger.info("ConnMan detected and will be used")
        } else if networkManagerAvailable {
            detected = .networkManager
            logger.info("NetworkManager detected and will be used")
        } else {
            detected = .none
            logger.warning("No network manager detected")
        }

        cachedManager = detected
        return detected
    }

    /// Create a network connection manager instance based on preference and availability
    public func createNetworkManager(
        preference: Preference = .auto
    ) async throws -> NetworkConnectionManager {
        let detected = await detectNetworkManager()

        let managerType: NetworkManagerType
        switch preference {
        case .auto:
            managerType = detected

        case .preferConnMan:
            if detected == .connMan || detected == .networkManager {
                managerType = detected == .connMan ? .connMan : .networkManager
            } else {
                managerType = .none
            }

        case .preferNetworkManager:
            if detected == .networkManager || detected == .connMan {
                managerType = detected == .networkManager ? .networkManager : .connMan
            } else {
                managerType = .none
            }

        case .forceConnMan:
            if detected == .connMan {
                managerType = .connMan
            } else {
                logger.warning("ConnMan forced but not available")
                throw NetworkConnectionError.managerNotAvailable
            }

        case .forceNetworkManager:
            if detected == .networkManager {
                managerType = .networkManager
            } else {
                logger.warning("NetworkManager forced but not available")
                throw NetworkConnectionError.managerNotAvailable
            }
        }

        switch managerType {
        case .connMan:
            logger.info("Creating ConnMan instance")
            return ConnMan(uid: uid, socketPath: socketPath)

        case .networkManager:
            logger.info("Creating NetworkManager instance")
            return NetworkManagerAdapter(
                networkManager: NetworkManager(uid: uid, socketPath: socketPath)
            )

        case .none:
            logger.error("No network manager available")
            throw NetworkConnectionError.managerNotAvailable
        }
    }

    /// Clear the cached detection result (useful for testing or after system changes)
    public func clearCache() {
        cachedManager = nil
        logger.debug("Cleared network manager detection cache")
    }

    /// Get the currently detected network manager type, returns cached manager when available
    public func getDetectedType() async -> NetworkManagerType {
        return await detectNetworkManager()
    }
}