import Foundation

/// XPC Protocol for communication between edge CLI and edge-network-daemon
@objc public protocol EdgeNetworkDaemonProtocol {
    /// Basic handshake to verify daemon is responding
    func handshake(completion: @escaping (Bool, Error?) -> Void)
    
    /// Execute privileged network configuration with authorization
    func configureNetwork(
        authorizationData: Data,
        interfaceName: String,
        ipAddress: String,
        completion: @escaping (Bool, Error?) -> Void
    )
    
    /// Get daemon version
    func getVersion(completion: @escaping (String?, Error?) -> Void)
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
