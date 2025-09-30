import DBUS
import Logging
import NIO

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// ConnMan provides an interface to interact with ConnMan over DBus
public actor ConnMan: NetworkConnectionManager {
    private let logger = Logger(label: "ConnMan")

    // Connection configuration
    private let socketPath: String
    private let uid: String

    // ConnMan DBus constants
    private let connManDestination = "net.connman"
    private let connManManagerInterface = "net.connman.Manager"
    private let connManTechnologyInterface = "net.connman.Technology"
    private let connManServiceInterface = "net.connman.Service"

    public init(
        uid: String,
        socketPath: String = "/var/run/dbus/system_bus_socket"
    ) {
        self.uid = uid
        self.socketPath = socketPath
    }

    /// Execute a DBus request and handle the response
    private func executeDBusRequest<T: DBusDecodable & Sendable>(
        _ request: DBusRequest
    ) async throws -> T {
        let address = try SocketAddress(unixDomainSocketPath: socketPath)

        return try await DBusClient.withConnection(
            to: address,
            auth: .external(userID: uid)
        ) { [self] connection in
            do {
                guard let reply = try await connection.send(request) else {
                    throw NetworkConnectionError.noReply
                }
                guard case .methodReturn = reply.messageType else {
                    var errorDetails = "No details available"
                    if let bodyValue = reply.body.first, case .string(let message) = bodyValue {
                        errorDetails = message
                    }

                    self.logger.error(
                        "DBus connection error",
                        metadata: [
                            "error": "\(errorDetails)"
                        ]
                    )
                    throw NetworkConnectionError.connectionFailed
                }
                return try T.decode(from: reply)
            } catch {
                self.logger.error(
                    "Failed to execute DBus request",
                    metadata: [
                        "error": "\(error)"
                    ]
                )
                throw NetworkConnectionError.connectionFailed
            }
        }
    }

    /// Get WiFi technology path
    private func getWiFiTechnology() async throws -> String? {
        let message: DBusMessage = try await executeDBusRequest(
            .createMethodCall(
                destination: connManDestination,
                path: "/",
                interface: connManManagerInterface,
                method: "GetTechnologies"
            )
        )

        guard let bodyValue = message.body.first,
            let array = bodyValue.array
        else {
            throw NetworkConnectionError.invalidType
        }

        for item in array {
            guard case .structure(let structValues) = item,
                structValues.count >= 2,
                let path = structValues[0].objectPath,
                case .dictionary(let properties) = structValues[1]
            else {
                continue
            }

            if let typeValue = properties[DBusValue.string("Type")],
                case .variant(let variant) = typeValue,
                case .string(let type) = variant.value,
                type == "wifi"
            {
                return path
            }
        }

        return nil
    }

    /// Scan for WiFi networks
    private func scanWiFiNetworks() async throws {
        guard let wifiTechPath = try await getWiFiTechnology() else {
            throw NetworkConnectionError.noWiFiDevice
        }

        let _: DBusMessage = try await executeDBusRequest(
            .createMethodCall(
                  destination: connManDestination,
                  path: wifiTechPath,
                  interface: connManTechnologyInterface,
                  method: "Scan"
               )
        )

        try await Task.sleep(for: .seconds(2))
    }

    /// Get all services (networks)
    private func getServices() async throws -> [(path: String, properties: [DBusValue: DBusValue])]
    {
        let message: DBusMessage = try await executeDBusRequest(
            .createMethodCall(
                destination: connManDestination,
                path: "/",
                interface: connManManagerInterface,
                method: "GetServices"
            )
        )

        guard let bodyValue = message.body.first,
            let array = bodyValue.array
        else {
            throw NetworkConnectionError.invalidType
        }

        var services: [(path: String, properties: [DBusValue: DBusValue])] = []

        for item in array {
            guard case .structure(let structValues) = item,
                structValues.count >= 2,
                let path = structValues[0].objectPath,
                case .dictionary(let properties) = structValues[1]
            else {
                continue
            }

            services.append((path: path, properties: properties))
        }

        return services
    }

    /// Extract SSID from service properties
    private func extractSSID(from properties: [DBusValue: DBusValue]) -> String? {
        guard let nameValue = properties[DBusValue.string("Name")],
            case .variant(let variant) = nameValue,
            case .string(let name) = variant.value
        else {
            return nil
        }
        return name
    }

    /// Extract signal strength from service properties
    private func extractSignalStrength(from properties: [DBusValue: DBusValue]) -> Int8? {
        guard let strengthValue = properties[DBusValue.string("Strength")],
            case .variant(let variant) = strengthValue,
            case .byte(let strength) = variant.value
        else {
            return nil
        }
        return Int8(strength)
    }

    /// Extract security type from service properties
    private func extractSecurity(from properties: [DBusValue: DBusValue]) -> Bool {
        guard let securityValue = properties[DBusValue.string("Security")],
            case .variant(let variant) = securityValue,
            let array = variant.value.array,
            !array.isEmpty
        else {
            return false
        }

        for item in array {
            if case .string(let security) = item,
                security != "none"
            {
                return true
            }
        }

        return false
    }

    /// Extract service type from properties
    private func extractServiceType(from properties: [DBusValue: DBusValue]) -> String? {
        guard let typeValue = properties[DBusValue.string("Type")],
            case .variant(let variant) = typeValue,
            case .string(let type) = variant.value
        else {
            return nil
        }
        return type
    }

    /// Extract service state from properties
    private func extractServiceState(from properties: [DBusValue: DBusValue]) -> String? {
        guard let stateValue = properties[DBusValue.string("State")],
            case .variant(let variant) = stateValue,
            case .string(let state) = variant.value
        else {
            return nil
        }
        return state
    }

    /// Extract IP address from service properties
    private func extractIPAddress(from properties: [DBusValue: DBusValue]) -> String? {
        guard let ipv4Value = properties[DBusValue.string("IPv4")],
            case .variant(let ipv4Variant) = ipv4Value,
            case .dictionary(let ipv4Dict) = ipv4Variant.value,
            let addressValue = ipv4Dict[DBusValue.string("Address")],
            case .variant(let addressVariant) = addressValue,
            case .string(let address) = addressVariant.value
        else {
            return nil
        }
        return address
    }

    /// List all available WiFi networks
    public func listWiFiNetworks() async throws -> [WiFiNetwork] {
        logger.debug("Scanning for WiFi networks")

        do {
            try await scanWiFiNetworks()
        } catch {
            logger.warning(
                "WiFi scan failed, continuing with cached results",
                metadata: [
                    "error": "\(error)"
                ]
            )
        }

        let services = try await getServices()

        var networks: [WiFiNetwork] = []

        for (path, properties) in services {
            guard let type = extractServiceType(from: properties),
                type == "wifi",
                let ssid = extractSSID(from: properties),
                !ssid.isEmpty
            else {
                continue
            }

            let signalStrength = extractSignalStrength(from: properties)
            let isSecured = extractSecurity(from: properties)

            networks.append(
                WiFiNetwork(
                    ssid: ssid,
                    path: path,
                    signalStrength: signalStrength,
                    isSecured: isSecured
                )
            )

            logger.debug(
                "Found network",
                metadata: [
                    "ssid": "\(ssid)",
                    "signal": "\(signalStrength?.description ?? "unknown")",
                    "secured": "\(isSecured)",
                ]
            )
        }

        return networks
    }

    /// Connect to a WiFi network
    public func connectToNetwork(ssid: String, password: String) async throws {
        logger.debug("Connecting to WiFi network", metadata: ["ssid": "\(ssid)"])

        let networks = try await listWiFiNetworks()

        guard let network = networks.first(where: { $0.ssid == ssid }) else {
            logger.error("Network not found", metadata: ["ssid": "\(ssid)"])
            throw NetworkConnectionError.networkNotFound
        }

        let address = try SocketAddress(unixDomainSocketPath: socketPath)

        try await DBusClient.withConnection(
            to: address,
            auth: .external(userID: uid)
        ) { [self] connection in
            if network.isSecured && !password.isEmpty {
                let setPropertyMessage = DBusRequest.createMethodCall(
                    destination: connManDestination,
                    path: network.path,
                    interface: connManServiceInterface,
                    method: "SetProperty",
                    body: [
                        .string("Passphrase"),
                        .variant(DBusVariant(.string(password))),
                    ]
                )

                guard let setReply = try await connection.send(setPropertyMessage) else {
                    throw NetworkConnectionError.noReply
                }

                guard case .methodReturn = setReply.messageType else {
                    self.logger.error("Failed to set passphrase")
                    throw NetworkConnectionError.authenticationFailed
                }
            }

            let connectMessage = DBusRequest.createMethodCall(
                destination: connManDestination,
                path: network.path,
                interface: connManServiceInterface,
                method: "Connect"
            )

            guard let connectReply = try await connection.send(connectMessage) else {
                throw NetworkConnectionError.noReply
            }

            guard case .methodReturn = connectReply.messageType else {
                var errorDetails = "No details available"
                if let bodyValue = connectReply.body.first,
                    case .string(let message) = bodyValue
                {
                    errorDetails = message
                }

                if errorDetails.contains("Invalid key") || errorDetails.contains("invalid-key") {
                    self.logger.error("Invalid WiFi password")
                    throw NetworkConnectionError.authenticationFailed
                }

                self.logger.error(
                    "Connection failed",
                    metadata: [
                        "error": "\(errorDetails)"
                    ]
                )
                throw NetworkConnectionError.connectionFailed
            }

            self.logger.info(
                "Successfully connected to WiFi network",
                metadata: [
                    "ssid": "\(ssid)"
                ]
            )
        }
    }

    /// Get the current active WiFi connection information
    public func getCurrentConnection() async throws -> WiFiConnection? {
        let services = try await getServices()

        for (path, properties) in services {
            guard let type = extractServiceType(from: properties),
                type == "wifi",
                let state = extractServiceState(from: properties),
                state == "ready" || state == "online",
                let ssid = extractSSID(from: properties)
            else {
                continue
            }

            let ipAddress = extractIPAddress(from: properties)

            let connectionState: WiFiConnection.ConnectionState
            switch state {
            case "ready", "online":
                connectionState = .connected
            case "association", "configuration":
                connectionState = .connecting
            case "failure":
                connectionState = .failed
            default:
                connectionState = .disconnected
            }

            return WiFiConnection(
                ssid: ssid,
                connectionPath: path,
                ipAddress: ipAddress,
                state: connectionState
            )
        }

        return nil
    }

    /// Disconnect from the current WiFi network
    public func disconnectFromNetwork() async throws -> Bool {
        guard let connection = try await getCurrentConnection() else {
            throw NetworkConnectionError.noActiveConnection
        }

        let _: DBusMessage = try await executeDBusRequest(
            .createMethodCall(
                destination: connManDestination,
                path: connection.connectionPath,
                interface: connManServiceInterface,
                method: "Disconnect"
            )
        )

        logger.info(
            "Disconnected from WiFi network",
            metadata: [
                "ssid": "\(connection.ssid)"
            ]
        )

        return true
    }

    /// Connect to network (helper function for conformance)
    public func setupWiFi(ssid: String, password: String) async throws {
        try await connectToNetwork(ssid: ssid, password: password)
    }
}
