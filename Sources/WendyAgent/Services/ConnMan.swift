import DBUS
import Logging
import NIO
import _NIOFileSystem

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
                            "address": "\(address)",
                            "uid": "\(uid)",
                            "error": "\(errorDetails)",
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

    /// Get WiFi technology path and its properties
    private func getWiFiTechnologyInfo() async throws -> (path: String, properties: [DBusValue: DBusValue])? {
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

        logger.info("Found \(array.count) technologies")

        for item in array {
            guard case .structure(let structValues) = item,
                structValues.count >= 2,
                let path = structValues[0].objectPath,
                case .array(let propertiesArray) = structValues[1]
            else {
                continue
            }

            // Merge array of dictionaries into a single dictionary
            var properties: [DBusValue: DBusValue] = [:]
            for propItem in propertiesArray {
                if case .dictionary(let dict) = propItem {
                    for (key, value) in dict {
                        properties[key] = value
                    }
                }
            }

            if let typeValue = properties[DBusValue.string("Type")],
                case .variant(let variant) = typeValue,
                case .string(let type) = variant.value,
                type == "wifi"
            {
                return (path: path, properties: properties)
            }
        }

        return nil
    }

    /// Check if WiFi technology is powered on
    private func isWiFiPowered(properties: [DBusValue: DBusValue]) -> Bool {
        guard let poweredValue = properties[DBusValue.string("Powered")],
              case .variant(let variant) = poweredValue,
              case .boolean(let powered) = variant.value
        else {
            return false
        }
        return powered
    }

    /// Enable WiFi technology if it's not powered on
    private func ensureWiFiEnabled() async throws {
        guard let wifiInfo = try await getWiFiTechnologyInfo() else {
            throw NetworkConnectionError.noWiFiDevice
        }

        let path = wifiInfo.path
        let properties = wifiInfo.properties

        if !isWiFiPowered(properties: properties) {
            logger.info("WiFi technology is powered off, enabling it...")

            let _: DBusMessage = try await executeDBusRequest(
                .createMethodCall(
                    destination: connManDestination,
                    path: path,
                    interface: connManTechnologyInterface,
                    method: "SetProperty",
                    body: [
                        .string("Powered"),
                        .variant(DBusVariant(.boolean(true)))
                    ]
                )
            )

            logger.info("WiFi technology enabled successfully")

            // Wait a moment for the technology to power up
            try await Task.sleep(for: .seconds(2))
        } else {
            logger.debug("WiFi technology is already powered on")
        }
    }

    /// Scan for WiFi networks
    private func scanWiFiNetworks() async throws {
        guard let wifiInfo = try await getWiFiTechnologyInfo() else {
            throw NetworkConnectionError.noWiFiDevice
        }

        let _: DBusMessage = try await executeDBusRequest(
            .createMethodCall(
                destination: connManDestination,
                path: wifiInfo.path,
                interface: connManTechnologyInterface,
                method: "Scan"
            )
        )

        try await Task.sleep(for: .seconds(3))
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

        logger.info("Found service count: ", metadata: ["count": "\(array.count)"])

        var services: [(path: String, properties: [DBusValue: DBusValue])] = []

        for item in array {
            guard case .structure(let structValues) = item,
                structValues.count >= 2,
                let path = structValues[0].objectPath,
                case .array(let propertiesArray) = structValues[1]
            else {
                continue
            }

            // Merge array of dictionaries into a single dictionary
            var properties: [DBusValue: DBusValue] = [:]
            for propItem in propertiesArray {
                if case .dictionary(let dict) = propItem {
                    for (key, value) in dict {
                        properties[key] = value
                    }
                }
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

    /// Extract IPv4 address from service properties
    private func extractIPAddress(from properties: [DBusValue: DBusValue]) -> String? {
        guard let ipv4Value = properties[DBusValue.string("IPv4")],
            // Find the IPv4 root in the data
            case .variant(let ipv4Variant) = ipv4Value,
            // Get the root as a dictionary
            case .dictionary(let ipv4Dict) = ipv4Variant.value,
            // Find the Address value in the dictionary
            let addressValue = ipv4Dict[DBusValue.string("Address")],
            case .variant(let addressVariant) = addressValue,
            // Get the Address as a string
            case .string(let address) = addressVariant.value
        else {
            return nil
        }
        return address
    }

    /// List all available WiFi networks
    public func listWiFiNetworks() async throws -> [WiFiNetwork] {
        logger.debug("Scanning for WiFi networks")

        // Ensure WiFi is enabled before scanning
        try await ensureWiFiEnabled()

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

    /// Generate a safe filename from SSID
    private func sanitizeSSIDForFilename(_ ssid: String) -> String {
        // Replace problematic characters with underscores
        let sanitized = ssid.replacingOccurrences(of: "/", with: "_")
            .replacingOccurrences(of: "\\", with: "_")
            .replacingOccurrences(of: " ", with: "_")
            .replacingOccurrences(of: ":", with: "_")
            .replacingOccurrences(of: "*", with: "_")
            .replacingOccurrences(of: "?", with: "_")
            .replacingOccurrences(of: "\"", with: "_")
            .replacingOccurrences(of: "<", with: "_")
            .replacingOccurrences(of: ">", with: "_")
            .replacingOccurrences(of: "|", with: "_")

        // Limit length to avoid filesystem issues
        let maxLength = 50
        if sanitized.count > maxLength {
            return String(sanitized.prefix(maxLength))
        }
        return sanitized
    }

    /// Create ConnMan configuration file content
    private func createConnManConfigContent(ssid: String, password: String, isSecured: Bool = true) -> String {
        // ConnMan configuration file format
        let configContent = """
        [service_wifi_\(sanitizeSSIDForFilename(ssid))]
        Type=wifi
        Name=\(ssid)
        Security=\(isSecured && !password.isEmpty ? "psk" : "none")
        Passphrase=\(isSecured && !password.isEmpty ? password : "")
        IPv4=dhcp
        """

        return configContent
    }

    /// Write ConnMan configuration file
    private func writeConnManConfigFile(ssid: String, password: String, isSecured: Bool = true) async throws -> FilePath {
        let configDir = FilePath("/var/lib/connman")
        let fileName = "wifi_\(sanitizeSSIDForFilename(ssid)).config"
        let configPath = configDir.appending(fileName)

        logger.info("Writing ConnMan config file", metadata: [
            "path": "\(configPath)"
        ])

        let configContent = createConnManConfigContent(ssid: ssid, password: password, isSecured: isSecured)

        // Write the configuration file
        let fileSystem = FileSystem.shared
        do {
            // Ensure the directory exists
            do {
                let dirInfo = try await fileSystem.info(forFileAt: configDir)
                if dirInfo == nil {
                    logger.warning("ConnMan config directory doesn't exist, attempting to create it")
                    try await fileSystem.createDirectory(at: configDir, withIntermediateDirectories: true)
                }
            } catch {
                // Directory might not exist, try to create it
                logger.info("Creating ConnMan config directory")
                try await fileSystem.createDirectory(at: configDir, withIntermediateDirectories: true)
            }

            // Write the configuration file
            try await fileSystem.withFileHandle(
                forWritingAt: configPath,
                options: .newFile(replaceExisting: true)
            ) { handle in
                let buffer = ByteBuffer(string: configContent)
                _ = try await handle.write(contentsOf: buffer.readableBytesView, toAbsoluteOffset: 0)
            }

            logger.info("Successfully wrote ConnMan config file", metadata: [
                "path": "\(configPath)"
            ])

            return configPath
        } catch {
            logger.error("Failed to write ConnMan config file", metadata: [
                "path": "\(configPath)",
                "error": "\(error)"
            ])
            throw error
        }
    }

    /// Remove ConnMan configuration file
    private func removeConnManConfigFile(ssid: String) async {
        let configDir = FilePath("/var/lib/connman")
        let fileName = "wifi_\(sanitizeSSIDForFilename(ssid)).config"
        let configPath = configDir.appending(fileName)

        let fileSystem = FileSystem.shared
        do {
            try await fileSystem.removeItem(at: configPath)
            logger.debug("Removed ConnMan config file", metadata: [
                "path": "\(configPath)"
            ])
        } catch {
            logger.debug("Could not remove ConnMan config file", metadata: [
                "path": "\(configPath)",
                "error": "\(error)"
            ])
        }
    }

    /// Connect to a WiFi network
    public func connectToNetwork(ssid: String, password: String) async throws {
        logger.debug("Connecting to WiFi network", metadata: ["ssid": "\(ssid)"])

        // Ensure WiFi is enabled before connecting
        try await ensureWiFiEnabled()

        let networks = try await listWiFiNetworks()

        guard let network = networks.first(where: { $0.ssid == ssid }) else {
            logger.error("Network not found", metadata: ["ssid": "\(ssid)"])
            throw NetworkConnectionError.networkNotFound
        }

        // Create a configuration file for both secured and open networks
        do {
            // Write the configuration file
            let configPath = try await writeConnManConfigFile(ssid: ssid, password: password, isSecured: network.isSecured)
            logger.info("Created ConnMan configuration file", metadata: [
                "path": "\(configPath)",
                "secured": "\(network.isSecured)"
            ])

            // Give ConnMan a moment to process the new configuration
            try await Task.sleep(for: .milliseconds(500))

            // Trigger a rescan to pick up the new configuration
            do {
                try await scanWiFiNetworks()
            } catch {
                logger.warning("Failed to trigger rescan after config creation", metadata: [
                    "error": "\(error)"
                ])
            }

        } catch {
            logger.error("Failed to create ConnMan configuration file", metadata: [
                "error": "\(error)"
            ])
        }

        // Now attempt to connect
        let address = try SocketAddress(unixDomainSocketPath: socketPath)

        try await DBusClient.withConnection(
            to: address,
            auth: .external(userID: uid)
        ) { [self] connection in
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
                } else if case .error = connectReply.messageType {
                    // Extract error information from the reply
                    errorDetails = "DBus error occurred"
                }

                if errorDetails.contains("Invalid key") ||
                   errorDetails.contains("invalid-key") ||
                   errorDetails.contains("InvalidKey") ||
                   errorDetails.contains("InvalidPassphrase") {
                    self.logger.error("Invalid WiFi password")
                    // Clean up the configuration file if authentication failed
                    await removeConnManConfigFile(ssid: ssid)
                    throw NetworkConnectionError.authenticationFailed
                }

                if errorDetails.contains("InProgress") || errorDetails.contains("AlreadyConnected") {
                    self.logger.info("Connection already in progress or connected")
                    return
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
                "Successfully initiated connection to WiFi network",
                metadata: [
                    "ssid": "\(ssid)"
                ]
            )

            // Wait a bit for the connection to establish
            try await Task.sleep(for: .seconds(1))

            // Verify connection was successful
            if let currentConnection = try await self.getCurrentConnection(),
               currentConnection.ssid == ssid,
               currentConnection.state == .connected {
                self.logger.info(
                    "Successfully connected to WiFi network",
                    metadata: [
                        "ssid": "\(ssid)",
                        "ip": "\(currentConnection.ipAddress ?? "pending")"
                    ]
                )
            } else {
                self.logger.warning("Connection initiated but not yet established, may take a few more seconds")
            }
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

        // Clean up the configuration file for this network
        await removeConnManConfigFile(ssid: connection.ssid)

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
