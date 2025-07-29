import Foundation

/// Network interface information
@objc public class NetworkInterfaceInfo: NSObject, NSSecureCoding, Codable, @unchecked Sendable {
    public static let supportsSecureCoding: Bool = true

    @objc public let name: String
    @objc public let bsdName: String
    @objc public let deviceId: String

    public init(name: String, bsdName: String, deviceId: String) {
        self.name = name
        self.bsdName = bsdName
        self.deviceId = deviceId
    }

    public required init?(coder: NSCoder) {
        guard let name = coder.decodeObject(of: NSString.self, forKey: "name") as String?,
            let bsdName = coder.decodeObject(of: NSString.self, forKey: "bsdName") as String?,
            let deviceId = coder.decodeObject(of: NSString.self, forKey: "deviceId") as String?
        else {
            return nil
        }
        self.name = name
        self.bsdName = bsdName
        self.deviceId = deviceId
    }

    public func encode(with coder: NSCoder) {
        coder.encode(name, forKey: "name")
        coder.encode(bsdName, forKey: "bsdName")
        coder.encode(deviceId, forKey: "deviceId")
    }
}

/// IP configuration information
@objc public class IPConfigurationInfo: NSObject, NSSecureCoding, Codable, @unchecked Sendable {
    public static let supportsSecureCoding: Bool = true

    @objc public let ipAddress: String
    @objc public let subnetMask: String
    @objc public let gateway: String?

    public init(ipAddress: String, subnetMask: String, gateway: String? = nil) {
        self.ipAddress = ipAddress
        self.subnetMask = subnetMask
        self.gateway = gateway
    }

    public required init?(coder: NSCoder) {
        guard let ipAddress = coder.decodeObject(of: NSString.self, forKey: "ipAddress") as String?,
            let subnetMask = coder.decodeObject(of: NSString.self, forKey: "subnetMask") as String?
        else {
            return nil
        }
        self.ipAddress = ipAddress
        self.subnetMask = subnetMask
        self.gateway = coder.decodeObject(of: NSString.self, forKey: "gateway") as String?
    }

    public func encode(with coder: NSCoder) {
        coder.encode(ipAddress, forKey: "ipAddress")
        coder.encode(subnetMask, forKey: "subnetMask")
        coder.encode(gateway, forKey: "gateway")
    }
}

/// XPC Protocol for communication between edge CLI and edge-network-daemon
@objc public protocol EdgeNetworkDaemonProtocol {
    /// Basic handshake to verify daemon is responding
    func handshake(completion: @escaping @Sendable (Bool, Error?) -> Void)

    /// Configure network interface with IP settings
    func configureInterface(
        _ interface: NetworkInterfaceInfo,
        with config: IPConfigurationInfo,
        completion: @escaping @Sendable (Bool, Error?) -> Void
    )

    /// Check if interface is already configured
    func isInterfaceConfigured(
        _ interface: NetworkInterfaceInfo,
        completion: @escaping @Sendable (Bool, Error?) -> Void
    )

    /// Clean up interface configuration (reset to DHCP)
    func cleanupInterface(
        _ interface: NetworkInterfaceInfo,
        completion: @escaping @Sendable (Bool, Error?) -> Void
    )

    /// Get daemon version
    func getVersion(completion: @escaping @Sendable (String?, Error?) -> Void)
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
public let kEdgeNetworkDaemonServiceName = "com.edgeos.edge-network-daemon"

/// Custom authorization right for network configuration
public let kEdgeNetworkConfigurationRight = "com.edgeos.configure.network"

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
