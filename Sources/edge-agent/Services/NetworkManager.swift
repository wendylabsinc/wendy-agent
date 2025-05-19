import DBusSwift
import Logging
import NIO

#if canImport(FoundationEssentials)
    import FoundationEssentials
#else
    import Foundation
#endif

/// 1. Authenticate with DBus
/// 2. Get all devices
/// 3. Get device type
/// 4. Scan for APs
/// 5. Connect to AP
/// 6. Disconnect from AP
/// 7. Get all connections
/// 8. Get all active connections

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

    private var connection: DBusClient?
    private var inbound: NIOAsyncChannelInboundStream<DBusMessage>?
    private var outbound: NIOAsyncChannelOutboundWriter<DBusMessage>?

    // Serial number for DBus requests
    private var serial: UInt32 = 1

    // Connection state tracking
    private var connectionState: ConnectionState = .disconnected
    private var connectionTask: Task<Void, Error>?

    // Connection configuration
    private let socketPath: String
    private let maxReconnectAttempts: Int
    private let reconnectDelay: TimeInterval
    private let uid: String
    private var persistConnection: Bool

    public init(
        uid: String,
        socketPath: String = "/var/run/dbus/system_bus_socket",
        maxReconnectAttempts: Int = 5,
        reconnectDelay: TimeInterval = 2.0,
        persistConnection: Bool = true
    ) {
        self.uid = uid
        self.socketPath = socketPath
        self.maxReconnectAttempts = maxReconnectAttempts
        self.reconnectDelay = reconnectDelay
        self.persistConnection = persistConnection
    }

    deinit {
        // Can't call actor methods in deinit, so we just abandon resources
        connectionTask?.cancel()
    }

    /// Connect to DBus and authenticate
    public func connect() async throws {
        // Check if already connecting or connected
        guard connectionState == .disconnected else {
            return
        }

        // Change state to connecting and start the connection task
        connectionState = .connecting

        // Create a new connection task
        connectionTask = Task {
            var attemptCount = 0
            var lastError: Error?

            while attemptCount < maxReconnectAttempts {
                do {
                    // Attempt to connect
                    try await establishConnection()
                    return
                } catch {
                    lastError = error
                    attemptCount += 1

                    // Update connection state if we're still in the reconnecting flow
                    if connectionState != .disconnected {
                        connectionState = .reconnecting
                    }

                    logger.warning(
                        "Connection attempt \(attemptCount) failed",
                        metadata: [
                            "error": "\(error)",
                            "reconnectDelay": "\(reconnectDelay)",
                        ]
                    )

                    if attemptCount < maxReconnectAttempts {
                        try await Task.sleep(for: .seconds(reconnectDelay))
                    }
                }
            }

            // If we've exhausted all attempts, update state and throw
            connectionState = .disconnected

            throw lastError ?? NetworkManagerError.connectionFailed
        }

        // Wait for the connection task to complete
        try await connectionTask?.value
    }

    /// Disconnect from DBus
    public func disconnect() {
        connectionTask?.cancel()
        connectionTask = nil
        connectionState = .disconnected
        inbound = nil
        outbound = nil
    }

    /// Establish a new connection
    private func establishConnection() async throws {
        // Create a continuation that will allow us to return from this async function
        // only after the connection is established
        try await withCheckedThrowingContinuation {
            [weak self] (continuation: CheckedContinuation<Void, Error>) in
            guard let self = self else {
                continuation.resume(throwing: NetworkManagerError.notConnected)
                return
            }

            Task {
                do {
                    try await DBusClient.withConnection(
                        to: SocketAddress(unixDomainSocketPath: self.socketPath),
                        auth: .external(userID: self.uid)
                    ) { [weak self] inbound, outbound in
                        guard let self = self else {
                            continuation.resume(throwing: NetworkManagerError.notConnected)
                            return
                        }

                        // This is now in a Task, which can safely call into actor methods
                        try await self.handleConnection(inbound: inbound, outbound: outbound)

                        // Signal that establishment is complete
                        continuation.resume()

                        // Keep connection alive if needed
                        if await self.persistConnection {
                            try await Task.sleep(for: .seconds(UInt64.max))
                        }
                    }
                } catch {
                    continuation.resume(throwing: error)
                }
            }
        }
    }

    // This method runs on the actor and can safely mutate state
    private func handleConnection(
        inbound: NIOAsyncChannelInboundStream<DBusMessage>,
        outbound: NIOAsyncChannelOutboundWriter<DBusMessage>
    ) async throws {
        // Store connection handlers
        self.inbound = inbound
        self.outbound = outbound

        // Authenticate with DBus
        try await self.authenticate()

        // Update connection state
        self.connectionState = .connected

        self.logger.info("Successfully connected to DBus")
    }

    /// Execute an operation with a temporary connection
    /// This is useful for one-off operations that don't need a persistent connection
    public func withTemporaryConnection<T: Sendable>(
        _ operation: @Sendable () async throws -> T
    ) async throws -> T {
        let wasConnected = connectionState == .connected
        let originalPersistSetting = persistConnection

        if !wasConnected {
            // Temporarily set to non-persistent and connect
            self.persistConnection = false
            try await connect()
        }

        defer {
            if !wasConnected {
                // We can't await in defer, so we start a detached task
                Task.detached { [weak self] in
                    await self?.disconnect()
                }
                self.persistConnection = originalPersistSetting
            }
        }

        return try await operation()
    }

    /// Get the next serial number for DBus requests
    private func nextSerial() -> UInt32 {
        defer { serial += 1 }
        return serial
    }

    /// Send a DBus request and await the response
    private func sendRequest<T: DBusDecodable>(method: (UInt32) -> DBusMessage) async throws -> T {
        // Check connection state and reconnect if needed
        if connectionState != .connected {
            try await connect()
        }

        guard let inbound = inbound, let outbound = outbound, connectionState == .connected else {
            throw NetworkManagerError.notConnected
        }

        let serial = nextSerial()
        let message = method(serial)

        logger.debug("Sending request", metadata: ["serial": "\(serial)", "message": "\(message)"])

        do {
            try await outbound.write(message)
        } catch {
            // If writing fails, mark as disconnected and retry once
            connectionState = .disconnected

            // Try to reconnect and send again
            try await connect()

            guard let outbound = self.outbound else {
                throw NetworkManagerError.notConnected
            }

            try await outbound.write(message)
        }

        var stream = inbound.makeAsyncIterator()
        let responseTimeout = Task<Void, Error> {
            try await Task.sleep(for: .seconds(10))
            throw NetworkManagerError.timeout
        }

        do {
            while let reply = try await stream.next() {
                if reply.replyTo == serial {
                    responseTimeout.cancel()
                    logger.debug("Received reply", metadata: ["serial": "\(serial)"])
                    return try T.decode(from: reply)
                }
            }

            responseTimeout.cancel()
            throw NetworkManagerError.noReply
        } catch is CancellationError {
            throw NetworkManagerError.timeout
        } catch {
            responseTimeout.cancel()
            throw error
        }
    }

    /// Authenticate with DBus
    private func authenticate() async throws {
        guard let outbound = outbound, let inbound = inbound else {
            throw NetworkManagerError.notConnected
        }

        let helloMsg = DBusMessage.createMethodCall(
            destination: "org.freedesktop.DBus",
            path: "/org/freedesktop/DBus",
            interface: "org.freedesktop.DBus",
            method: "Hello",
            serial: nextSerial()
        )

        try await outbound.write(helloMsg)

        var stream = inbound.makeAsyncIterator()
        guard let helloReply = try await stream.next() else {
            throw NetworkManagerError.authenticationFailed
        }

        guard case .methodReturn = helloReply.messageType else {
            throw NetworkManagerError.authenticationFailed
        }

        logger.info("Successfully authenticated with DBus")
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
        let message =
            try await sendRequest { serial in
                DBusMessage.createMethodCall(
                    destination: "org.freedesktop.NetworkManager",
                    path: "/org/freedesktop/NetworkManager",
                    interface: "org.freedesktop.NetworkManager",
                    method: "GetDevices",
                    serial: serial
                )
            } as DBusMessage

        return try decodeStringArray(from: message)
    }

    /// Get device type for a specific device path
    public func getDeviceType(devicePath: String) async throws -> UInt32 {
        return try await sendRequest { serial in
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
        let message =
            try await sendRequest { serial in
                DBusMessage.createMethodCall(
                    destination: "org.freedesktop.NetworkManager",
                    path: devicePath,
                    interface: "org.freedesktop.NetworkManager.Device.Wireless",
                    method: "GetAllAccessPoints",
                    serial: serial
                )
            } as DBusMessage

        return try decodeStringArray(from: message)
    }

    /// Get SSID for an access point
    public func getSSID(apPath: String) async throws -> String {
        let message =
            try await sendRequest { serial in
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
            } as DBusMessage

        let ssidBytes = try decodeByteArray(from: message)

        guard let ssid = String(bytes: ssidBytes, encoding: .utf8) else {
            throw NetworkManagerError.invalidSSID
        }

        return ssid
    }

    /// List all available WiFi networks
    public func listWiFiNetworks() async throws -> [(ssid: String, path: String)] {
        // 1. Get all devices
        let devicePaths: [String] = try await getNetworkDevices()

        // 2. Find the WiFi device (type 2 is WiFi)
        var wifiDevicePath: String? = nil
        for devicePath in devicePaths {
            let deviceType: UInt32 = try await getDeviceType(devicePath: devicePath)
            if deviceType == 2 {  // NM_DEVICE_TYPE_WIFI = 2
                wifiDevicePath = devicePath
                break
            }
        }

        guard let wifiDevicePath = wifiDevicePath else {
            throw NetworkManagerError.noWiFiDevice
        }

        // 3. Get all access points
        let accessPointPaths: [String] = try await getAccessPoints(devicePath: wifiDevicePath)

        // 4. Get SSIDs for each access point
        var networks: [(ssid: String, path: String)] = []
        for apPath in accessPointPaths {
            do {
                let ssid = try await getSSID(apPath: apPath)
                networks.append((ssid: ssid, path: apPath))
            } catch {
                logger.warning(
                    "Failed to get SSID for AP: \(apPath)",
                    metadata: ["error": "\(error)"]
                )
                continue
            }
        }

        return networks
    }

    /// Connect to a WiFi network with a one-time connection
    /// This is a convenience method that establishes a connection, connects to WiFi, and then disconnects
    public func setupWiFi(ssid: String, password: String) async throws -> Bool {
        return try await withTemporaryConnection {
            try await connectToNetwork(ssid: ssid, password: password)
        }
    }

    /// Connect to a WiFi network
    public func connectToNetwork(ssid: String, password: String) async throws -> Bool {
        let networks = try await listWiFiNetworks()
        guard let network = networks.first(where: { $0.ssid == ssid }) else {
            throw NetworkManagerError.networkNotFound
        }

        // Get WiFi device path again
        let devicePaths: [String] = try await getNetworkDevices()
        var wifiDevicePath: String? = nil
        for devicePath in devicePaths {
            let deviceType: UInt32 = try await getDeviceType(devicePath: devicePath)
            if deviceType == 2 {
                wifiDevicePath = devicePath
                break
            }
        }

        guard let wifiDevicePath = wifiDevicePath else {
            throw NetworkManagerError.noWiFiDevice
        }

        let message =
            try await sendRequest { serial in
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
                                        .string("ssid"): .array(
                                            ssid.utf8.map { DBusValue.byte($0) }
                                        )
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
            } as DBusMessage

        return try Bool.decode(from: message)
    }
}

// MARK: - Connection State

/// Connection states for the NetworkManager
private enum ConnectionState {
    case disconnected
    case connecting
    case connected
    case reconnecting
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
}

// Add conformance for DBusMessage
extension DBusMessage: DBusDecodable {
    public static func decode(from message: DBusMessage) throws -> DBusMessage {
        return message
    }
}
