import Foundation
import Testing

@testable import wendy_agent

@Suite("WendyAgentConfiguration")
struct WendyAgentConfigurationTests {
    @Test("Default configuration uses auto preference")
    func testDefaultConfiguration() {
        let config = WendyAgentConfiguration()
        #expect(config.networkManagerPreference == .auto)
    }

    @Test("Configuration with specific network manager preference")
    func testConfigurationWithPreference() {
        let config = WendyAgentConfiguration(networkManagerPreference: .preferConnMan)
        #expect(config.networkManagerPreference == .preferConnMan)
    }

    @Test("Configuration from environment with ConnMan preference")
    func testConfigurationFromEnvironmentConnMan() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Set environment variable
        setenv("WENDY_NETWORK_MANAGER", "connman", 1)

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .preferConnMan)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        } else {
            unsetenv("WENDY_NETWORK_MANAGER")
        }
    }

    @Test("Configuration from environment with NetworkManager preference")
    func testConfigurationFromEnvironmentNetworkManager() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Set environment variable
        setenv("WENDY_NETWORK_MANAGER", "networkmanager", 1)

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .preferNetworkManager)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        } else {
            unsetenv("WENDY_NETWORK_MANAGER")
        }
    }

    @Test("Configuration from environment with force ConnMan")
    func testConfigurationFromEnvironmentForceConnMan() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Set environment variable
        setenv("WENDY_NETWORK_MANAGER", "force-connman", 1)

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .forceConnMan)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        } else {
            unsetenv("WENDY_NETWORK_MANAGER")
        }
    }

    @Test("Configuration from environment with force NetworkManager")
    func testConfigurationFromEnvironmentForceNetworkManager() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Set environment variable
        setenv("WENDY_NETWORK_MANAGER", "force-networkmanager", 1)

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .forceNetworkManager)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        } else {
            unsetenv("WENDY_NETWORK_MANAGER")
        }
    }

    @Test("Configuration from environment with invalid value defaults to auto")
    func testConfigurationFromEnvironmentInvalid() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Set environment variable with invalid value
        setenv("WENDY_NETWORK_MANAGER", "invalid-manager", 1)

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .auto)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        } else {
            unsetenv("WENDY_NETWORK_MANAGER")
        }
    }

    @Test("Configuration from environment with no variable defaults to auto")
    func testConfigurationFromEnvironmentNoVariable() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Unset environment variable
        unsetenv("WENDY_NETWORK_MANAGER")

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .auto)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        }
    }

    @Test("Configuration handles network-manager with hyphen")
    func testConfigurationFromEnvironmentNetworkManagerWithHyphen() {
        // Save current environment
        let originalValue = ProcessInfo.processInfo.environment["WENDY_NETWORK_MANAGER"]

        // Set environment variable
        setenv("WENDY_NETWORK_MANAGER", "network-manager", 1)

        let config = WendyAgentConfiguration.fromEnvironment()
        #expect(config.networkManagerPreference == .preferNetworkManager)

        // Restore original environment
        if let original = originalValue {
            setenv("WENDY_NETWORK_MANAGER", original, 1)
        } else {
            unsetenv("WENDY_NETWORK_MANAGER")
        }
    }
}
