#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Configuration for EdgeAgent services
public struct EdgeAgentConfiguration: Sendable {
    /// Network manager preference
    public let networkManagerPreference: NetworkManagerFactory.Preference

    public init(networkManagerPreference: NetworkManagerFactory.Preference = .auto) {
        self.networkManagerPreference = networkManagerPreference
    }

    /// Create configuration from environment variables
    public static func fromEnvironment() -> EdgeAgentConfiguration {
        // Check for EDGE_NETWORK_MANAGER environment variable
        if let envValue = ProcessInfo.processInfo.environment["EDGE_NETWORK_MANAGER"] {
            let preference: NetworkManagerFactory.Preference
            switch envValue.lowercased() {
            case "connman":
                preference = .preferConnMan
            case "networkmanager", "network-manager":
                preference = .preferNetworkManager
            case "force-connman":
                preference = .forceConnMan
            case "force-networkmanager", "force-network-manager":
                preference = .forceNetworkManager
            default:
                preference = .auto
            }
            return EdgeAgentConfiguration(networkManagerPreference: preference)
        }

        // Return default configuration if no environment variable is set
        return EdgeAgentConfiguration()
    }
}
