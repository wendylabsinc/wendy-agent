import CliXPCProtocol
import Foundation
import Logging

/// Client for communicating with the privileged network daemon via XPC
/// Uses modern XPCSession with proper async/await patterns (macOS 14+)
@available(macOS 14.0, *)
actor NetworkDaemonClient: NetworkDaemonClientProtocol {
    private let logger: Logger
    private var session: XPCSession?

    init(logger: Logger) {
        self.logger = logger
    }

    private func getSession() throws -> XPCSession {
        if let session = session {
            return session
        }

        let newSession = try XPCSession(
            machService: kEdgeNetworkDaemonServiceName,
            cancellationHandler: { [weak self] error in
                self?.logger.error("XPC session cancelled", metadata: ["error": "\(error)"])
            }
        )

        self.session = newSession
        return newSession
    }

    private func clearSession() async {
        if let session = session {
            session.cancel(reason: "Client shutting down")
            self.session = nil
        }
    }

    func configureInterface(
        name: String,
        bsdName: String,
        deviceId: String,
        ipAddress: String,
        subnetMask: String,
        gateway: String? = nil
    ) async throws {
        logger.info("Requesting network configuration", metadata: ["interface": "\(name)"])

        let interface = NetworkInterfaceInfo(name: name, bsdName: bsdName, deviceId: deviceId)
        let config = IPConfigurationInfo(
            ipAddress: ipAddress,
            subnetMask: subnetMask,
            gateway: gateway
        )

        let request = NetworkRequest.configureInterface(interface: interface, config: config)
        let response = try await sendRequest(request)

        if !response.success {
            throw XPCError.networkConfigurationFailed(response.error ?? "Configuration failed")
        }
    }

    func isInterfaceConfigured(
        name: String,
        bsdName: String,
        deviceId: String
    ) async throws -> Bool {
        logger.debug("Checking configuration status", metadata: ["interface": "\(name)"])

        let interface = NetworkInterfaceInfo(name: name, bsdName: bsdName, deviceId: deviceId)

        let request = NetworkRequest.isInterfaceConfigured(interface: interface)
        let response = try await sendRequest(request)

        if !response.success {
            throw XPCError.networkConfigurationFailed(
                response.error ?? "Failed to check interface status"
            )
        }

        guard case .boolean(let isConfigured) = response.data else {
            throw XPCError.networkConfigurationFailed("Invalid response data type")
        }

        return isConfigured
    }

    func cleanupInterface(
        name: String,
        bsdName: String,
        deviceId: String
    ) async throws {
        logger.info("Requesting cleanup", metadata: ["interface": "\(name)"])

        let interface = NetworkInterfaceInfo(name: name, bsdName: bsdName, deviceId: deviceId)
        let request = NetworkRequest.cleanupInterface(interface: interface)
        let response = try await sendRequest(request)

        if !response.success {
            throw XPCError.networkConfigurationFailed(response.error ?? "Cleanup failed")
        }
    }

    func testConnection() async throws {
        logger.debug("Testing connection to network daemon")

        let request = NetworkRequest.handshake
        let response = try await sendRequest(request)

        if !response.success {
            throw XPCError.connectionFailed
        }
    }

    // MARK: - Helper Methods

    private func sendRequest(_ request: NetworkRequest) async throws -> NetworkResponse {
        let session = try getSession()

        // Encode the request as JSON string for XPCDictionary transport
        let encoder = JSONEncoder()
        let requestData = try encoder.encode(request)
        let requestJSON = String(data: requestData, encoding: .utf8)!

        var message = XPCDictionary()
        message["request"] = requestJSON

        return try await withCheckedThrowingContinuation { continuation in
            session.send(message: message) { result in
                do {
                    let response = try NetworkDaemonClient.handleResponse(result)
                    continuation.resume(returning: response)
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    private static func handleResponse(
        _ result: Result<XPCDictionary, XPCRichError>
    ) throws -> NetworkResponse {
        switch result {
        case .success(let reply):
            guard let responseJSON = reply["response", as: String.self],
                let responseData = responseJSON.data(using: .utf8)
            else {
                throw XPCError.networkConfigurationFailed("Invalid response format")
            }

            let decoder = JSONDecoder()
            return try decoder.decode(NetworkResponse.self, from: responseData)

        case .failure(let xpcError):
            throw XPCError.networkConfigurationFailed(xpcError.localizedDescription)
        }
    }

    deinit {
        // XPCSession cleanup will happen automatically when deallocated
        // We cannot access actor-isolated properties from deinit
    }
}
