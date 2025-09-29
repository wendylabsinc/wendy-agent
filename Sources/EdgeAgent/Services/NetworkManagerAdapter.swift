import Logging

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Adapter to make NetworkManager conform to NetworkConnectionManager protocol
public actor NetworkManagerAdapter: NetworkConnectionManager {
    private let networkManager: NetworkManager
    private let logger = Logger(label: "NetworkManagerAdapter")

    public init(networkManager: NetworkManager) {
        self.networkManager = networkManager
    }

    public func listWiFiNetworks() async throws -> [WiFiNetwork] {
        let nmNetworks = try await networkManager.listWiFiNetworks()
        return nmNetworks.map { network in
            WiFiNetwork(
                ssid: network.ssid,
                path: network.path,
                signalStrength: network.signalStrength,
                isSecured: true
            )
        }
    }

    public func connectToNetwork(ssid: String, password: String) async throws {
        try await networkManager.connectToNetwork(ssid: ssid, password: password)
    }

    public func getCurrentConnection() async throws -> WiFiConnection? {
        guard let connection = try await networkManager.getCurrentConnection() else {
            return nil
        }

        return WiFiConnection(
            ssid: connection.ssid,
            connectionPath: connection.connectionPath,
            ipAddress: nil,
            state: .connected
        )
    }

    public func disconnectFromNetwork() async throws -> Bool {
        return try await networkManager.disconnectFromNetwork()
    }

    public func setupWiFi(ssid: String, password: String) async throws {
        try await networkManager.setupWiFi(ssid: ssid, password: password)
    }
}