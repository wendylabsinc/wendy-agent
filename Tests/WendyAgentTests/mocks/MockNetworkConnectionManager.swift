import Foundation

@testable import wendy_agent

/// Mock implementation of NetworkConnectionManager for testing
public actor MockNetworkConnectionManager: NetworkConnectionManager {
    // Track method calls for verification
    public var listWiFiNetworksCalled = false
    public var connectToNetworkCalled = false
    public var getCurrentConnectionCalled = false
    public var disconnectFromNetworkCalled = false
    public var setupWiFiCalled = false

    // Track parameters passed
    public var lastConnectSSID: String?
    public var lastConnectPassword: String?
    public var lastSetupSSID: String?
    public var lastSetupPassword: String?

    // Configurable return values
    public var networksToReturn: [WiFiNetwork] = []
    public var connectionToReturn: WiFiConnection?
    public var shouldThrowError: NetworkConnectionError?
    public var disconnectSuccess = true

    public init() {}

    public func listWiFiNetworks() async throws -> [WiFiNetwork] {
        listWiFiNetworksCalled = true

        if let error = shouldThrowError {
            throw error
        }

        return networksToReturn
    }

    public func connectToNetwork(ssid: String, password: String) async throws {
        connectToNetworkCalled = true
        lastConnectSSID = ssid
        lastConnectPassword = password

        if let error = shouldThrowError {
            throw error
        }

        // Simulate successful connection by updating connectionToReturn
        connectionToReturn = WiFiConnection(
            ssid: ssid,
            connectionPath: "/mock/connection/\(ssid)",
            ipAddress: "192.168.1.100",
            state: .connected
        )
    }

    public func getCurrentConnection() async throws -> WiFiConnection? {
        getCurrentConnectionCalled = true

        if let error = shouldThrowError {
            throw error
        }

        return connectionToReturn
    }

    public func disconnectFromNetwork() async throws -> Bool {
        disconnectFromNetworkCalled = true

        if let error = shouldThrowError {
            throw error
        }

        // Clear connection on disconnect
        if disconnectSuccess {
            connectionToReturn = nil
        }

        return disconnectSuccess
    }

    public func setupWiFi(ssid: String, password: String) async throws {
        setupWiFiCalled = true
        lastSetupSSID = ssid
        lastSetupPassword = password

        if let error = shouldThrowError {
            throw error
        }

        // Simulate successful setup
        connectionToReturn = WiFiConnection(
            ssid: ssid,
            connectionPath: "/mock/connection/\(ssid)",
            ipAddress: "192.168.1.100",
            state: .connected
        )
    }

    // Helper methods for test setup
    public func reset() {
        listWiFiNetworksCalled = false
        connectToNetworkCalled = false
        getCurrentConnectionCalled = false
        disconnectFromNetworkCalled = false
        setupWiFiCalled = false
        lastConnectSSID = nil
        lastConnectPassword = nil
        lastSetupSSID = nil
        lastSetupPassword = nil
        networksToReturn = []
        connectionToReturn = nil
        shouldThrowError = nil
        disconnectSuccess = true
    }

    public func addMockNetwork(ssid: String, signalStrength: Int8? = -50, isSecured: Bool = true) {
        networksToReturn.append(
            WiFiNetwork(
                ssid: ssid,
                path: "/mock/network/\(ssid)",
                signalStrength: signalStrength,
                isSecured: isSecured
            )
        )
    }

    public func setMockConnection(ssid: String, state: WiFiConnection.ConnectionState = .connected)
    {
        connectionToReturn = WiFiConnection(
            ssid: ssid,
            connectionPath: "/mock/connection/\(ssid)",
            ipAddress: state == .connected ? "192.168.1.100" : nil,
            state: state
        )
    }
}
