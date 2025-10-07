import Foundation

/// Network interface information
public struct NetworkInterfaceInfo: Codable, Sendable {
    public let name: String
    public let bsdName: String
    public let deviceId: String

    public init(name: String, bsdName: String, deviceId: String) {
        self.name = name
        self.bsdName = bsdName
        self.deviceId = deviceId
    }
}

/// IP configuration information
public struct IPConfigurationInfo: Codable, Sendable {
    public let ipAddress: String
    public let subnetMask: String
    public let gateway: String?

    public init(ipAddress: String, subnetMask: String, gateway: String? = nil) {
        self.ipAddress = ipAddress
        self.subnetMask = subnetMask
        self.gateway = gateway
    }
}

/// Error types for XPC communication
public enum XPCError: Error, LocalizedError {
    case connectionFailed
    case authorizationInvalid
    case networkConfigurationFailed(String)
    case daemonNotRunning

    public var errorDescription: String? {
        switch self {
        case .connectionFailed:
            return "Failed to connect to network daemon"
        case .authorizationInvalid:
            return "Invalid authorization provided"
        case .networkConfigurationFailed(let message):
            return "Network configuration failed: \(message)"
        case .daemonNotRunning:
            return "Network daemon is not running"
        }
    }
}

/// XPC Service identifier
public let kWendyNetworkDaemonServiceName = "com.wendyos.wendy-network-daemon"

/// Custom authorization right for network configuration
public let kWendyNetworkConfigurationRight = "com.wendyos.configure.network"

// MARK: - Modern XPC Message Types (macOS 14+)

/// Request types for network daemon XPC communication
public enum NetworkRequest: Codable, Sendable {
    case handshake
    case configureInterface(interface: NetworkInterfaceInfo, config: IPConfigurationInfo)
    case isInterfaceConfigured(interface: NetworkInterfaceInfo)
    case cleanupInterface(interface: NetworkInterfaceInfo)
    case getVersion
}

/// Response type for network daemon XPC communication
public struct NetworkResponse: Codable, Sendable {
    public let success: Bool
    public let error: String?
    public let data: ResponseData?

    public init(success: Bool, error: String? = nil, data: ResponseData? = nil) {
        self.success = success
        self.error = error
        self.data = data
    }
}

/// Response data payload for different request types
public enum ResponseData: Codable, Sendable {
    case boolean(Bool)
    case string(String)
    case empty
}
