import DBusSwift
import Logging
import NIO

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// Protocol for decoding DBus messages into Swift types
public protocol DBusDecodable {
    static func decode(from message: DBusMessage) throws -> Self
}

// MARK: - DBus Decodable Implementations

extension String: DBusDecodable {
    public static func decode(from message: DBusMessage) throws -> String {
        guard let value = message.body.first else {
            throw NetworkManagerError.noReply
        }

        switch value {
        case .string(let string):
            return string
        case .variant(let variant):
            if case .string(let string) = variant.value {
                return string
            }
            throw NetworkManagerError.invalidType
        default:
            throw NetworkManagerError.invalidType
        }
    }
}

extension UInt32: DBusDecodable {
    public static func decode(from message: DBusMessage) throws -> UInt32 {
        guard let bodyValue = message.body.first else {
            throw NetworkManagerError.noReply
        }

        switch bodyValue {
        case .uint32(let value):
            return value
        case .variant(let variant):
            if case .uint32(let value) = variant.value {
                return value
            }
            throw NetworkManagerError.invalidType
        default:
            throw NetworkManagerError.invalidType
        }
    }
}

extension Int8: DBusDecodable {
    public static func decode(from message: DBusMessage) throws -> Int8 {
        guard let bodyValue = message.body.first else {
            throw NetworkManagerError.noReply
        }

        switch bodyValue {
        case .byte(let byte):
            return Int8(byte)
        case .int16(let value):
            guard value >= Int16(Int8.min) && value <= Int16(Int8.max) else {
                throw NetworkManagerError.invalidType
            }
            return Int8(value)
        case .int32(let value):
            guard value >= Int32(Int8.min) && value <= Int32(Int8.max) else {
                throw NetworkManagerError.invalidType
            }
            return Int8(value)
        case .int64(let value):
            guard value >= Int64(Int8.min) && value <= Int64(Int8.max) else {
                throw NetworkManagerError.invalidType
            }
            return Int8(value)
        case .variant(let variant):
            if case .byte(let byte) = variant.value {
                return Int8(byte)
            }
            if case .int16(let value) = variant.value,
                value >= Int16(Int8.min) && value <= Int16(Int8.max)
            {
                return Int8(value)
            }
            if case .int32(let value) = variant.value,
                value >= Int32(Int8.min) && value <= Int32(Int8.max)
            {
                return Int8(value)
            }
            if case .int64(let value) = variant.value,
                value >= Int64(Int8.min) && value <= Int64(Int8.max)
            {
                return Int8(value)
            }
            throw NetworkManagerError.invalidType
        default:
            throw NetworkManagerError.invalidType
        }
    }
}

extension Bool: DBusDecodable {
    public static func decode(from message: DBusMessage) throws -> Bool {
        guard let bodyValue = message.body.first else {
            throw NetworkManagerError.noReply
        }

        switch bodyValue {
        case .boolean(let value):
            return value
        case .variant(let variant):
            if case .boolean(let value) = variant.value {
                return value
            }
            throw NetworkManagerError.invalidType
        default:
            throw NetworkManagerError.invalidType
        }
    }
}

/// NetworkManager provides an interface to interact with NetworkManager over DBus
public actor NetworkManager {
    private let logger = Logger(label: "NetworkManager")

    // Serial number for DBus requests
    private var serial: UInt32 = 1

    // Connection configuration
    private let socketPath: String
    private let uid: String

    public init(
        uid: String,
        socketPath: String = "/var/run/dbus/system_bus_socket"
    ) {
        self.uid = uid
        self.socketPath = socketPath
    }

    /// Get the next serial number for DBus requests
    private func nextSerial() -> UInt32 {
        defer { serial += 1 }
        return serial
    }

    /// Execute a DBus request and handle the response
    private func executeDBusRequest<T: DBusDecodable & Sendable>(
        _ method: @Sendable @escaping (UInt32) -> DBusMessage
    ) async throws -> T {
        // Create socket address
        let address = try SocketAddress(unixDomainSocketPath: socketPath)

        // Get a serial number for the request
        let requestSerial = nextSerial()
        let requestMessage = method(requestSerial)

        // Log request details
        logger.debug("Starting DBus request with serial \(requestSerial)")

        return try await DBusClient.withConnection(
            to: address,
            auth: .external(userID: uid)
        ) { [self] inbound, outbound in
            // Helper function to log signal details
            func logSignalDetails(_ signal: DBusMessage) {
                self.logger.debug(
                    "DETAILED SIGNAL: MessageType=\(signal.messageType), Serial=\(signal.serial)"
                )

                if !signal.body.isEmpty {
                    // Detailed body logging based on message body content type
                    self.logger.debug("Signal body contents:")
                    for (index, value) in signal.body.enumerated() {
                        self.logger.debug("  Body[\(index)]: \(value)")

                        // Try to extract more detailed information from body elements
                        switch value {
                        case .string(let message):
                            if message.contains("error") || message.contains("permission")
                                || message.contains("denied") || message.contains("failed")
                            {
                                self.logger.error("⚠️ IMPORTANT ERROR MESSAGE: \(message)")
                            }
                            self.logger.debug("  - String value: \(message)")
                        case .uint32(let code):
                            self.logger.debug("  - Code value: \(code)")
                        case .variant(let variant):
                            self.logger.debug("  - Variant value: \(variant.value)")
                        case .dictionary(let dict):
                            self.logger.debug("  - Dictionary entries: \(dict.count)")
                            for (key, dictValue) in dict {
                                self.logger.debug("    * \(key): \(dictValue)")
                            }
                        case .array(let array):
                            self.logger.debug("  - Array elements: \(array.count)")
                            for (i, element) in array.enumerated() {
                                self.logger.debug("    * [\(i)]: \(element)")
                            }
                        default:
                            self.logger.debug("  - Other value type: \(value)")
                        }
                    }
                } else {
                    self.logger.debug("Signal body is empty")
                }
            }

            // Create a single iterator for the entire operation
            var stream = inbound.makeAsyncIterator()

            // Authenticate with DBus by sending Hello
            let helloSerial = await self.nextSerial()
            let helloMessage = DBusMessage.createMethodCall(
                destination: "org.freedesktop.DBus",
                path: "/org/freedesktop/DBus",
                interface: "org.freedesktop.DBus",
                method: "Hello",
                serial: helloSerial
            )

            self.logger.debug("Sending Hello message with serial \(helloSerial)")
            try await outbound.write(helloMessage)

            // Look for Hello response
            var receivedHello = false
            while !receivedHello {
                guard let reply = try await stream.next() else {
                    self.logger.error("Stream ended without Hello response")
                    throw NetworkManagerError.authenticationFailed
                }

                self.logger.debug(
                    "Received message with serial: \(reply.serial), messageType: \(reply.messageType), replyTo: \(reply.replyTo ?? 0)"
                )

                // Skip signal messages (replyTo == 0)
                if reply.replyTo == 0 {
                    if case .signal = reply.messageType {
                        logSignalDetails(reply)
                    } else {
                        self.logger.debug("Skipping message with replyTo=0")
                    }
                    continue
                }

                if reply.replyTo == helloSerial {
                    if case .methodReturn = reply.messageType {
                        self.logger.debug("Hello authentication successful")
                        receivedHello = true
                    } else {
                        self.logger.error(
                            "Authentication failed with message type \(reply.messageType)"
                        )
                        throw NetworkManagerError.authenticationFailed
                    }
                }
            }

            // Send the actual request
            self.logger.debug("Sending request message with serial \(requestSerial)")

            do {
                try await outbound.write(requestMessage)
            } catch {
                self.logger.error("Failed to write request to DBus: \(error)")
                throw NetworkManagerError.connectionFailed
            }

            // Process messages until we find our response
            while true {
                guard let reply = try await stream.next() else {
                    self.logger.error("Stream ended without response to request \(requestSerial)")
                    throw NetworkManagerError.noReply
                }

                self.logger.debug(
                    "Received message with serial: \(reply.serial), messageType: \(reply.messageType), replyTo: \(reply.replyTo ?? 0)"
                )

                // Skip signal messages (replyTo == 0)
                if reply.replyTo == 0 {
                    if case .signal = reply.messageType {
                        logSignalDetails(reply)
                    } else {
                        self.logger.debug("Skipping message with replyTo=0")
                    }
                    continue
                }

                // Only process messages that match our request serial
                if reply.replyTo == requestSerial {
                    if case .methodReturn = reply.messageType {
                        self.logger.debug("Found response for request \(requestSerial)")
                        do {
                            let result = try T.decode(from: reply)
                            return result
                        } catch {
                            self.logger.error("Failed to decode response: \(error)")
                            throw error
                        }
                    } else if case .error = reply.messageType {
                        // Extract error details from the body if possible
                        var errorDetails = "No details available"
                        if let bodyValue = reply.body.first, case .string(let message) = bodyValue {
                            errorDetails = message
                        }

                        self.logger.error("DBus connection error: \(errorDetails)")
                        self.logger.debug("Full error reply: \(reply)")
                        throw NetworkManagerError.connectionFailed
                    }
                }
            }
        }
    }

    /// Helper to decode an array of strings from DBus message
    private func decodeStringArray(from message: DBusMessage) throws -> [String] {
        guard let bodyValue = message.body.first else {
            throw NetworkManagerError.noReply
        }

        var paths: [String] = []

        switch bodyValue {
        case .array(let values):
            paths = values.compactMap { value in
                if case .objectPath(let path) = value {
                    return path
                } else if case .variant(let variant) = value,
                    case .objectPath(let path) = variant.value
                {
                    return path
                }
                return nil
            }
        case .variant(let variant):
            if case .array(let values) = variant.value {
                paths = values.compactMap { value in
                    if case .objectPath(let path) = value {
                        return path
                    } else if case .variant(let variant) = value,
                        case .objectPath(let path) = variant.value
                    {
                        return path
                    }
                    return nil
                }
            }
        default:
            throw NetworkManagerError.invalidType
        }

        return paths
    }

    /// Helper to decode an array of bytes from DBus message
    private func decodeByteArray(from message: DBusMessage) throws -> [UInt8] {
        guard let bodyValue = message.body.first else {
            throw NetworkManagerError.noReply
        }

        var bytes: [UInt8] = []

        switch bodyValue {
        case .array(let values):
            bytes = values.compactMap { value in
                if case .byte(let byte) = value {
                    return byte
                } else if case .variant(let variant) = value, case .byte(let byte) = variant.value {
                    return byte
                }
                return nil
            }
        case .variant(let variant):
            if case .array(let values) = variant.value {
                bytes = values.compactMap { value in
                    if case .byte(let byte) = value {
                        return byte
                    } else if case .variant(let variant) = value,
                        case .byte(let byte) = variant.value
                    {
                        return byte
                    }
                    return nil
                }
            }
        default:
            throw NetworkManagerError.invalidType
        }

        return bytes
    }

    /// Get all network devices
    public func getNetworkDevices() async throws -> [String] {
        let message: DBusMessage = try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: "/org/freedesktop/NetworkManager",
                interface: "org.freedesktop.NetworkManager",
                method: "GetDevices",
                serial: serial
            )
        }

        return try decodeStringArray(from: message)
    }

    /// Get device type for a specific device path
    public func getDeviceType(devicePath: String) async throws -> UInt32 {
        return try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: devicePath,
                interface: "org.freedesktop.DBus.Properties",
                method: "Get",
                serial: serial,
                body: [
                    .string("org.freedesktop.NetworkManager.Device"),
                    .string("DeviceType"),
                ]
            )
        }
    }

    /// Get all access points for a WiFi device
    public func getAccessPoints(devicePath: String) async throws -> [String] {
        let message: DBusMessage = try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: devicePath,
                interface: "org.freedesktop.NetworkManager.Device.Wireless",
                method: "GetAllAccessPoints",
                serial: serial
            )
        }

        return try decodeStringArray(from: message)
    }

    /// Get SSID for an access point
    public func getSSID(apPath: String) async throws -> String {
        let message: DBusMessage = try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: apPath,
                interface: "org.freedesktop.DBus.Properties",
                method: "Get",
                serial: serial,
                body: [
                    .string("org.freedesktop.NetworkManager.AccessPoint"),
                    .string("Ssid"),
                ]
            )
        }

        let ssidBytes = try decodeByteArray(from: message)

        guard let ssid = String(bytes: ssidBytes, encoding: .utf8) else {
            throw NetworkManagerError.invalidSSID
        }

        return ssid
    }

    /// Get the signal strength for an access point
    public func getSignalStrength(apPath: String) async throws -> Int8 {
        return try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: apPath,
                interface: "org.freedesktop.DBus.Properties",
                method: "Get",
                serial: serial,
                body: [
                    .string("org.freedesktop.NetworkManager.AccessPoint"),
                    .string("Strength"),
                ]
            )
        }
    }

    /// Find the WiFi device path among the available devices
    private func findWiFiDevice() async throws -> String {
        // Get all devices
        let devicePaths = try await getNetworkDevices()
        self.logger.debug("Found \(devicePaths.count) network devices")

        if devicePaths.isEmpty {
            throw NetworkManagerError.noWiFiDevice
        }

        // Try to find a WiFi device (type 2)
        for devicePath in devicePaths {
            do {
                let deviceType = try await getDeviceType(devicePath: devicePath)
                if deviceType == 2 {  // NM_DEVICE_TYPE_WIFI = 2
                    self.logger.debug("Found WiFi device at path: \(devicePath)")
                    return devicePath
                }
            } catch {
                // Log the error but continue trying other devices
                self.logger.warning("Failed to get device type for \(devicePath): \(error)")
                continue
            }
        }

        // If we get here, no WiFi device was found
        throw NetworkManagerError.noWiFiDevice
    }

    /// List all available WiFi networks
    public func listWiFiNetworks() async throws -> [(
        ssid: String, path: String, signalStrength: Int8?
    )] {
        // Find the WiFi device
        let wifiDevicePath = try await findWiFiDevice()

        // Get all access points
        self.logger.debug("Getting access points for WiFi device: \(wifiDevicePath)")
        let accessPointPaths = try await getAccessPoints(devicePath: wifiDevicePath)
        self.logger.debug("Found \(accessPointPaths.count) access points")

        if accessPointPaths.isEmpty {
            return []
        }

        // Get SSIDs and signal strength for each access point
        var networks: [(ssid: String, path: String, signalStrength: Int8?)] = []
        for apPath in accessPointPaths {
            do {
                let ssid = try await getSSID(apPath: apPath)
                var signalStrength: Int8? = nil

                do {
                    // Try to get signal strength, but don't fail if it's not available
                    signalStrength = try await getSignalStrength(apPath: apPath)
                } catch {
                    self.logger.warning(
                        "Failed to get signal strength for AP: \(apPath)",
                        metadata: ["error": "\(error)"]
                    )
                }

                networks.append((ssid: ssid, path: apPath, signalStrength: signalStrength))
                self.logger.debug(
                    "Added network: \(ssid) (signal: \(signalStrength?.description ?? "unknown"))"
                )
            } catch {
                self.logger.warning(
                    "Failed to get SSID for AP: \(apPath)",
                    metadata: ["error": "\(error)"]
                )
                continue
            }
        }

        return networks
    }

    /// Connect to a WiFi network
    public func connectToNetwork(ssid: String, password: String) async throws -> Bool {
        do {
            // Get available networks
            let networks = try await listWiFiNetworks()

            self.logger.debug(
                "Searching for network with SSID: '\(ssid)' among \(networks.count) networks"
            )

            guard let network = networks.first(where: { $0.ssid == ssid }) else {
                self.logger.error("Network not found: '\(ssid)'")
                self.logger.debug("Available networks: \(networks.map { $0.ssid })")
                throw NetworkManagerError.networkNotFound
            }

            // Find the WiFi device
            let wifiDevicePath = try await findWiFiDevice()

            // Connect to the WiFi network
            self.logger.debug(
                "Connecting to network '\(ssid)' using device \(wifiDevicePath) and AP \(network.path)"
            )

            let address = try SocketAddress(unixDomainSocketPath: socketPath)

            return try await DBusClient.withConnection(
                to: address,
                auth: .external(userID: uid)
            ) { inbound, outbound in
                // Helper function to log signal details
                func logSignalDetails(_ signal: DBusMessage) {
                    self.logger.debug(
                        "DETAILED SIGNAL: MessageType=\(signal.messageType), Serial=\(signal.serial)"
                    )

                    if !signal.body.isEmpty {
                        // Detailed body logging based on message body content type
                        self.logger.debug("Signal body contents:")
                        for (index, value) in signal.body.enumerated() {
                            self.logger.debug("  Body[\(index)]: \(value)")

                            // Try to extract more detailed information from body elements
                            switch value {
                            case .string(let message):
                                if message.contains("error") || message.contains("permission")
                                    || message.contains("denied") || message.contains("failed")
                                {
                                    self.logger.error("⚠️ IMPORTANT ERROR MESSAGE: \(message)")
                                }
                                self.logger.debug("  - String value: \(message)")
                            case .uint32(let code):
                                self.logger.debug("  - Code value: \(code)")
                            case .variant(let variant):
                                self.logger.debug("  - Variant value: \(variant.value)")
                            case .dictionary(let dict):
                                self.logger.debug("  - Dictionary entries: \(dict.count)")
                                for (key, dictValue) in dict {
                                    self.logger.debug("    * \(key): \(dictValue)")
                                }
                            case .array(let array):
                                self.logger.debug("  - Array elements: \(array.count)")
                                for (i, element) in array.enumerated() {
                                    self.logger.debug("    * [\(i)]: \(element)")
                                }
                            default:
                                self.logger.debug("  - Other value type: \(value)")
                            }
                        }
                    } else {
                        self.logger.debug("Signal body is empty")
                    }
                }

                // Get a serial number for the request
                let requestSerial = await self.nextSerial()

                // Authenticate with DBus
                let helloSerial = await self.nextSerial()
                let helloMessage = DBusMessage.createMethodCall(
                    destination: "org.freedesktop.DBus",
                    path: "/org/freedesktop/DBus",
                    interface: "org.freedesktop.DBus",
                    method: "Hello",
                    serial: helloSerial
                )

                self.logger.debug("Sending Hello message for connection request")
                try await outbound.write(helloMessage)

                var stream = inbound.makeAsyncIterator()

                // Process Hello response
                while true {
                    guard let reply = try await stream.next() else {
                        self.logger.error("Stream ended waiting for Hello response")
                        throw NetworkManagerError.authenticationFailed
                    }

                    self.logger.debug(
                        "Received message with serial: \(reply.serial), messageType: \(reply.messageType), replyTo: \(reply.replyTo ?? 0)"
                    )

                    // Skip signal messages (replyTo == 0)
                    if reply.replyTo == 0 {
                        if case .signal = reply.messageType {
                            logSignalDetails(reply)
                        } else {
                            self.logger.debug("Skipping message with replyTo=0")
                        }
                        continue
                    }

                    if reply.replyTo == helloSerial {
                        if case .methodReturn = reply.messageType {
                            self.logger.debug("Hello authentication successful")
                            break
                        } else {
                            self.logger.error(
                                "Authentication failed with message type \(reply.messageType)"
                            )
                            throw NetworkManagerError.authenticationFailed
                        }
                    }
                }

                // Create the connection request
                let connectMessage = DBusMessage.createMethodCall(
                    destination: "org.freedesktop.NetworkManager",
                    path: "/org/freedesktop/NetworkManager",
                    interface: "org.freedesktop.NetworkManager",
                    method: "AddAndActivateConnection",
                    serial: requestSerial,
                    body: [
                        // First parameter - a{sa{sv}} - A dictionary of settings
                        .dictionary([
                            // "connection" setting group
                            .string("connection"): .dictionary([
                                .string("id"): .variant(DBusVariant(.string(ssid))),
                                .string("type"): .variant(DBusVariant(.string("802-11-wireless"))),
                                .string("uuid"): .variant(DBusVariant(.string(UUID().uuidString))),
                                .string("autoconnect"): .variant(DBusVariant(.boolean(true))),
                            ]),

                            // "802-11-wireless" setting group
                            .string("802-11-wireless"): .dictionary([
                                .string("ssid"): .variant(
                                    DBusVariant(
                                        .array(ssid.utf8.map { DBusValue.byte($0) })
                                    )
                                ),
                                .string("mode"): .variant(DBusVariant(.string("infrastructure"))),
                            ]),

                            // "802-11-wireless-security" setting group
                            .string("802-11-wireless-security"): .dictionary([
                                .string("key-mgmt"): .variant(DBusVariant(.string("wpa-psk"))),
                                .string("psk"): .variant(DBusVariant(.string(password))),
                            ]),

                            // "ipv4" setting group
                            .string("ipv4"): .dictionary([
                                .string("method"): .variant(DBusVariant(.string("auto")))
                            ]),

                            // "ipv6" setting group
                            .string("ipv6"): .dictionary([
                                .string("method"): .variant(DBusVariant(.string("auto")))
                            ]),
                        ]),

                        // Second parameter - o - Device path
                        .objectPath(wifiDevicePath),

                        // Third parameter - o - Access point path
                        .objectPath(network.path),
                    ]
                )

                // Log the full message details for debugging
                self.logger.debug("==================== DBus MESSAGE DETAILS ====================")
                self.logger.debug("DBUS CONNECTION REQUEST SIGNATURE:")
                self.logger.debug("Full message: \(connectMessage)")
                self.logger.debug("Message Type: \(connectMessage.messageType)")
                self.logger.debug("Serial: \(connectMessage.serial)")
                self.logger.debug("Body types count: \(connectMessage.body.count)")

                // Print body details
                for (index, value) in connectMessage.body.enumerated() {
                    self.logger.debug("Body[\(index)] type: \(type(of: value))")
                    self.logger.debug("Body[\(index)] value: \(value)")

                    // For the first body parameter (connection settings)
                    if index == 0, case .array(let connectionSettings) = value {
                        self.logger.debug(
                            "Connection settings array count: \(connectionSettings.count)"
                        )
                        for (i, setting) in connectionSettings.enumerated() {
                            if case .dictionary(let dict) = setting {
                                self.logger.debug(
                                    "  Setting[\(i)] keys: \(dict.keys.map { String(describing: $0) })"
                                )
                            }
                        }
                    }

                    // For object paths
                    if case .objectPath(let path) = value {
                        self.logger.debug("Body[\(index)] objectPath: \(path)")
                    }
                }
                self.logger.debug("==================== END MESSAGE DETAILS ====================")

                self.logger.debug("Sending connection request with serial: \(requestSerial)")

                do {
                    try await outbound.write(connectMessage)
                } catch {
                    self.logger.error("Failed to send connection request: \(error)")
                    throw NetworkManagerError.connectionFailed
                }

                // Process response
                let maxAttempts = 5
                var attemptCount = 0

                while attemptCount < maxAttempts {
                    do {
                        guard let reply = try await stream.next() else {
                            self.logger.error("Connection stream ended unexpectedly")
                            throw NetworkManagerError.connectionFailed
                        }

                        self.logger.debug(
                            "Received reply with serial: \(reply.serial), messageType: \(reply.messageType), replyTo: \(reply.replyTo ?? 0)"
                        )

                        // Skip signal messages (replyTo == 0)
                        if reply.replyTo == 0 {
                            if case .signal = reply.messageType {
                                logSignalDetails(reply)
                            } else {
                                self.logger.debug("Skipping message with replyTo=0")
                            }
                            continue
                        }

                        if reply.replyTo == requestSerial {
                            if case .methodReturn = reply.messageType {
                                self.logger.debug("Successfully connected to WiFi network: \(ssid)")
                                return true
                            } else if case .error = reply.messageType {
                                // Extract error details from the body if possible
                                var errorDetails = "No details available"
                                if let bodyValue = reply.body.first,
                                    case .string(let message) = bodyValue
                                {
                                    errorDetails = message
                                }

                                // Check for common permission errors
                                if errorDetails.contains("Permission denied")
                                    || errorDetails.contains("Not authorized")
                                {
                                    self.logger.error(
                                        "DBus permission error: \(errorDetails) - Check that the user has permissions to manage NetworkManager"
                                    )
                                    throw NetworkManagerError.authenticationFailed
                                }

                                // Check for authentication/wrong password errors
                                if errorDetails.contains("Failed to activate")
                                    && errorDetails.contains("Secrets were required")
                                {
                                    self.logger.error(
                                        "WiFi authentication error: Invalid password or authentication failure"
                                    )
                                    throw NetworkManagerError.authenticationFailed
                                }

                                self.logger.error("DBus connection error: \(errorDetails)")
                                self.logger.debug("Full error reply: \(reply)")
                                throw NetworkManagerError.connectionFailed
                            }
                        }
                    } catch {
                        self.logger.error("Error while waiting for connection response: \(error)")

                        // Special handling for earlyEOF errors - these often indicate permission problems
                        if String(describing: error).contains("earlyEOF") {
                            self.logger.error(
                                """
                                DBus connection closed unexpectedly. This often indicates one of:
                                1. The user lacks permissions to manage NetworkManager connections
                                2. The NetworkManager policy prevents this connection type
                                3. The DBus message format for connection is incorrect

                                Check the NetworkManager and DBus permissions for user: \(self.uid)
                                """
                            )

                            // If we've already tried several times, change the error type
                            if attemptCount >= 2 {
                                throw NetworkManagerError.authenticationFailed
                            }
                        }

                        attemptCount += 1

                        if attemptCount >= maxAttempts {
                            throw NetworkManagerError.connectionFailed
                        }

                        // Longer delay for earlyEOF errors
                        if String(describing: error).contains("earlyEOF") {
                            try await Task.sleep(for: .milliseconds(1000))
                        } else {
                            try await Task.sleep(for: .milliseconds(500))
                        }
                    }
                }

                self.logger.error("Connection attempt timed out after \(maxAttempts) attempts")
                throw NetworkManagerError.timeout
            }
        } catch let error as NetworkManagerError {
            // Just re-throw NetworkManagerErrors
            throw error
        } catch {
            // Wrap other errors
            self.logger.error("Unexpected error connecting to WiFi: \(error)")

            // Check the error message string for common error patterns
            let errorString = String(describing: error)
            if errorString.contains("Permission denied") {
                self.logger.error(
                    "Permission denied accessing DBus socket. Check if the user has correct permissions."
                )
                throw NetworkManagerError.authenticationFailed
            } else if errorString.contains("Connection refused") {
                self.logger.error("Connection refused to DBus. Check if NetworkManager is running.")
                throw NetworkManagerError.notConnected
            }

            throw NetworkManagerError.connectionFailed
        }
    }

    /// Setup WiFi (alias for connectToNetwork for backward compatibility)
    public func setupWiFi(ssid: String, password: String) async throws -> Bool {
        return try await connectToNetwork(ssid: ssid, password: password)
    }

    /// Get the current active WiFi connection information
    public func getCurrentConnection() async throws -> (ssid: String, connectionPath: String)? {
        // Find the WiFi device
        let wifiDevicePath = try await findWiFiDevice()

        do {
            // Get the active connection path with enhanced debugging
            let message: DBusMessage = try await executeDBusRequest { serial in
                DBusMessage.createMethodCall(
                    destination: "org.freedesktop.NetworkManager",
                    path: wifiDevicePath,
                    interface: "org.freedesktop.DBus.Properties",
                    method: "Get",
                    serial: serial,
                    body: [
                        .string("org.freedesktop.NetworkManager.Device"),
                        .string("ActiveConnection"),
                    ]
                )
            }

            // Debug the response body
            self.logger.debug("ActiveConnection response: \(message)")
            if let firstBodyValue = message.body.first {
                self.logger.debug("ActiveConnection value type: \(type(of: firstBodyValue))")
            }

            // More robust extraction of connection path
            var activeConnectionPath = "/"
            if let bodyValue = message.body.first {
                switch bodyValue {
                case .objectPath(let path):
                    activeConnectionPath = path
                case .string(let path):
                    activeConnectionPath = path
                case .variant(let variant):
                    switch variant.value {
                    case .objectPath(let path):
                        activeConnectionPath = path
                    case .string(let path):
                        activeConnectionPath = path
                    default:
                        self.logger.error(
                            "Unexpected variant type for ActiveConnection: \(variant.value)"
                        )
                    }
                default:
                    self.logger.error("Unexpected type for ActiveConnection: \(bodyValue)")
                    // Try to extract a string from the body
                    let bodyString = String(describing: bodyValue)
                    if bodyString.contains("/org/freedesktop/NetworkManager/ActiveConnection") {
                        // Try to extract path using regex-like approach
                        if let range = bodyString.range(
                            of: "/org/freedesktop/NetworkManager/ActiveConnection[^ \"']+"
                        ) {
                            activeConnectionPath = String(bodyString[range])
                            self.logger.debug(
                                "Extracted path using fallback method: \(activeConnectionPath)"
                            )
                        }
                    }
                }
            }

            // If there's no active connection, return nil
            if activeConnectionPath == "/" {
                self.logger.debug("No active connection found")
                return nil
            }

            // Verify the path looks valid
            if !activeConnectionPath.hasPrefix("/org/freedesktop/NetworkManager") {
                self.logger.error("Invalid ActiveConnection path: \(activeConnectionPath)")
                throw NetworkManagerError.invalidType
            }

            self.logger.debug("Found active connection at path: \(activeConnectionPath)")

            // Get the connection ID (SSID)
            let ssidMessage: DBusMessage = try await executeDBusRequest {
                [activeConnectionPath] serial in
                DBusMessage.createMethodCall(
                    destination: "org.freedesktop.NetworkManager",
                    path: activeConnectionPath,
                    interface: "org.freedesktop.DBus.Properties",
                    method: "Get",
                    serial: serial,
                    body: [
                        .string("org.freedesktop.NetworkManager.Connection.Active"),
                        .string("Id"),
                    ]
                )
            }

            // Debug the response
            self.logger.debug("SSID response: \(ssidMessage)")

            // More robust extraction of SSID
            var ssid = ""
            if let bodyValue = ssidMessage.body.first {
                switch bodyValue {
                case .string(let value):
                    ssid = value
                case .variant(let variant):
                    if case .string(let value) = variant.value {
                        ssid = value
                    } else {
                        self.logger.error("Unexpected variant type for SSID: \(variant.value)")
                    }
                // Handle byte array (sometimes SSIDs are returned as byte arrays)
                case .array(let array):
                    let bytes = array.compactMap { value -> UInt8? in
                        if case .byte(let byte) = value {
                            return byte
                        } else if case .variant(let variant) = value,
                            case .byte(let byte) = variant.value
                        {
                            return byte
                        }
                        return nil
                    }

                    if let string = String(bytes: bytes, encoding: .utf8) {
                        ssid = string
                    } else {
                        self.logger.error("Failed to decode SSID from byte array")
                    }
                default:
                    self.logger.error("Unexpected type for SSID: \(bodyValue)")
                }
            }

            if ssid.isEmpty {
                self.logger.error("Empty SSID returned")
                throw NetworkManagerError.invalidSSID
            }

            return (ssid: ssid, connectionPath: activeConnectionPath)
        } catch {
            // Handle specific errors for better debugging
            if let nmError = error as? NetworkManagerError {
                if nmError == .invalidType {
                    self.logger.error(
                        "Invalid DBus response type when getting WiFi status. This might indicate NetworkManager version mismatch or interface changes."
                    )
                }
                throw nmError
            }

            // Log and rethrow other errors
            self.logger.error("Error getting current WiFi connection: \(error)")
            throw error
        }
    }

    /// Disconnect from the current WiFi network
    public func disconnectFromNetwork() async throws -> Bool {
        // Find the WiFi device
        let wifiDevicePath = try await findWiFiDevice()

        // Check if there's an active connection
        guard (try await getCurrentConnection()) != nil else {
            throw NetworkManagerError.noActiveConnection
        }

        // Disconnect by bringing the device down
        return try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: wifiDevicePath,
                interface: "org.freedesktop.NetworkManager.Device",
                method: "Disconnect",
                serial: serial
            )
        }
    }
}

// MARK: - Errors

/// Errors that can occur when using NetworkManager
public enum NetworkManagerError: Error {
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
}

// Add conformance for DBusMessage
extension DBusMessage: DBusDecodable {
    public static func decode(from message: DBusMessage) throws -> DBusMessage {
        return message
    }
}
