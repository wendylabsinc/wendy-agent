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

        logger.debug("Starting DBus request with serial \(requestSerial)")

        return try await DBusClient.withConnection(
            to: address,
            auth: .external(userID: uid)
        ) { [self] inbound, outbound in
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
                    "Received message with type \(reply.messageType), serial \(reply.serial), replyTo \(reply.replyTo ?? 0)"
                )

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
            try await outbound.write(requestMessage)

            // Process messages until we find our response
            while true {
                guard let reply = try await stream.next() else {
                    self.logger.error("Stream ended without response to request \(requestSerial)")
                    throw NetworkManagerError.noReply
                }

                self.logger.debug("Received message with replyTo \(reply.replyTo ?? 0)")

                // Only process messages that match our request serial
                if reply.replyTo == requestSerial {
                    if case .methodReturn = reply.messageType {
                        self.logger.debug("Found response for request \(requestSerial)")
                        return try T.decode(from: reply)
                    } else if case .error = reply.messageType {
                        self.logger.error("Request \(requestSerial) failed with DBus error")
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
        // Get available networks
        let networks = try await listWiFiNetworks()
        guard let network = networks.first(where: { $0.ssid == ssid }) else {
            throw NetworkManagerError.networkNotFound
        }

        // Find the WiFi device
        let wifiDevicePath = try await findWiFiDevice()

        // Connect to the WiFi network
        self.logger.debug(
            "Connecting to network \(ssid) using device \(wifiDevicePath) and AP \(network.path)"
        )
        return try await executeDBusRequest { serial in
            DBusMessage.createMethodCall(
                destination: "org.freedesktop.NetworkManager",
                path: "/org/freedesktop/NetworkManager",
                interface: "org.freedesktop.NetworkManager",
                method: "AddAndActivateConnection",
                serial: serial,
                body: [
                    .array([
                        .dictionary([
                            .string("connection"): .array([
                                .dictionary([.string("id"): .string(ssid)]),
                                .dictionary([.string("type"): .string("802-11-wireless")]),
                                .dictionary([.string("uuid"): .string(UUID().uuidString)]),
                                .dictionary([.string("autoconnect"): .boolean(true)]),
                            ])
                        ]),
                        .dictionary([
                            .string("802-11-wireless"): .array([
                                .dictionary([
                                    .string("ssid"): .array(ssid.utf8.map { DBusValue.byte($0) })
                                ]),
                                .dictionary([.string("mode"): .string("infrastructure")]),
                            ])
                        ]),
                        .dictionary([
                            .string("802-11-wireless-security"): .array([
                                .dictionary([.string("key-mgmt"): .string("wpa-psk")]),
                                .dictionary([.string("psk"): .string(password)]),
                            ])
                        ]),
                        .dictionary([
                            .string("ipv4"): .array([
                                .dictionary([.string("method"): .string("auto")])
                            ])
                        ]),
                        .dictionary([
                            .string("ipv6"): .array([
                                .dictionary([.string("method"): .string("auto")])
                            ])
                        ]),
                    ]),
                    .objectPath(wifiDevicePath),
                    .objectPath(network.path),
                ]
            )
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

        // Get the active connection path
        let activeConnectionPath: String = try await executeDBusRequest { serial in
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

        // If there's no active connection, return nil
        if activeConnectionPath == "/" {
            return nil
        }

        // Get the connection ID (SSID)
        let ssid: String = try await executeDBusRequest { serial in
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

        return (ssid: ssid, connectionPath: activeConnectionPath)
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
