#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Configuration for EdgeAgent services
public struct WendyAgentConfiguration: Sendable {
    /// Network manager preference
    public let networkManagerPreference: NetworkConnectionManagerFactory.Preference

    public init(networkManagerPreference: NetworkConnectionManagerFactory.Preference = .auto) {
        self.networkManagerPreference = networkManagerPreference
    }

    /// Create configuration from environment variables
    public static func fromEnvironment() -> WendyAgentConfiguration {
        // Check for WENDY_NETWORK_MANAGER environment variable
        if let envValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"] {
            let preference: NetworkConnectionManagerFactory.Preference
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
            return WendyAgentConfiguration(networkManagerPreference: preference)
        }

        // Return default configuration if no environment variable is set
        return WendyAgentConfiguration()
    }
}
