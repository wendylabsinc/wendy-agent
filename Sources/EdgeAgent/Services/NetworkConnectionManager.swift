import Logging

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Common protocol for network connection managers (NetworkManager, ConMan, etc.)
public protocol NetworkConnectionManager: Sendable {
    /// List all available WiFi networks
    func listWiFiNetworks() async throws -> [WiFiNetwork]

    /// Connect to a WiFi network
    func connectToNetwork(ssid: String, password: String) async throws

    /// Get the current active WiFi connection information
    func getCurrentConnection() async throws -> WiFiConnection?

    /// Disconnect from the current WiFi network
    func disconnectFromNetwork() async throws -> Bool

    /// Setup WiFi (alias for connectToNetwork for backward compatibility)
    func setupWiFi(ssid: String, password: String) async throws
}

/// Represents a discovered WiFi network
public struct WiFiNetwork: Sendable {
    public let ssid: String
    public let path: String
    public let signalStrength: Int8?
    public let isSecured: Bool

    public init(ssid: String, path: String, signalStrength: Int8? = nil, isSecured: Bool = true) {
        self.ssid = ssid
        self.path = path
        self.signalStrength = signalStrength
        self.isSecured = isSecured
    }
}

/// Represents an active WiFi connection
public struct WiFiConnection: Sendable {
    public let ssid: String
    public let connectionPath: String
    public let ipAddress: String?
    public let state: ConnectionState

    public enum ConnectionState: String, Sendable {
        case connected = "connected"
        case connecting = "connecting"
        case disconnected = "disconnected"
        case failed = "failed"
    }

    public init(ssid: String, connectionPath: String, ipAddress: String? = nil, state: ConnectionState = .connected) {
        self.ssid = ssid
        self.connectionPath = connectionPath
        self.ipAddress = ipAddress
        self.state = state
    }
}

/// Errors that can occur when using network connection managers
public enum NetworkConnectionError: Error, Sendable {
    case notConnected
    case noReply
    case authenticationFailed
    case noWiFiDevice
    case networkNotFound
    case invalidSSID
    case connectionFailed
    case timeout
    case invalidType
    case noActiveConnection
    case disconnectionFailed
    case managerNotAvailable
    case unsupportedOperation

    public var localizedDescription: String {
        switch self {
        case .notConnected:
            return "Not connected to network"
        case .noReply:
            return "No reply from network manager"
        case .authenticationFailed:
            return "Authentication failed"
        case .noWiFiDevice:
            return "No WiFi device found"
        case .networkNotFound:
            return "Network not found"
        case .invalidSSID:
            return "Invalid SSID"
        case .connectionFailed:
            return "Connection failed"
        case .timeout:
            return "Operation timed out"
        case .invalidType:
            return "Invalid data type"
        case .noActiveConnection:
            return "No active connection"
        case .disconnectionFailed:
            return "Disconnection failed"
        case .managerNotAvailable:
            return "Network manager not available"
        case .unsupportedOperation:
            return "Operation not supported"
        }
    }
}