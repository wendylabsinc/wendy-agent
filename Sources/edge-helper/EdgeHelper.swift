import ArgumentParser
import EdgeShared
import Foundation
import Logging

@available(macOS 14.0, *)
struct EdgeHelper: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "edge-helper",
        abstract: "EdgeOS USB device monitoring daemon",
        discussion: """
            A background daemon that monitors for EdgeOS USB devices and automatically
            configures network interfaces with unique IP addresses.
            """,
        version: "1.0.0"
    )

    @Flag(name: .shortAndLong, help: "Run in foreground (don't daemonize)")
    var foreground = false

    @Flag(name: .shortAndLong, help: "Enable debug logging")
    var debug = false

    @Option(name: .shortAndLong, help: "Log file path")
    var logFile: String?

    func run() async throws {
        // Set up logging
        let logLevel: Logger.Level = debug ? .debug : .info
        LoggingSystem.bootstrap { label in
            var handler = StreamLogHandler.standardOutput(label: label)
            handler.logLevel = logLevel
            return handler
        }

        let logger = Logger(label: "edge-helper")

        logger.info("Starting EdgeOS Helper Daemon")
        logger.info("Version: \(Version.current)")
        logger.info("Foreground mode: \(foreground)")
        logger.info("Debug logging: \(debug)")

        let deviceDiscovery = PlatformDeviceDiscovery(logger: logger)
        let usbMonitor = PlatformUSBMonitor(
            deviceDiscovery: deviceDiscovery,
            logger: logger,
            pollingInterval: .seconds(30)
        )
        let networkDaemonClient = NetworkDaemonClient(logger: logger)
        let ipManager = PlatformIPAddressManager(logger: logger)
        // Create the main daemon service
        let daemon = EdgeHelperDaemon(
            usbMonitor: usbMonitor,
            deviceDiscovery: deviceDiscovery,
            networkDaemonClient: networkDaemonClient,
            ipManager: ipManager,
            logger: logger
        )

        logger.info("Initializing daemon services...")
        try await daemon.start()

        // Keep the daemon running
        logger.info("EdgeOS Helper Daemon is running. Press Ctrl+C to stop.")

        // Run indefinitely until cancelled
        do {
            while true {
                try await Task.sleep(for: .seconds(60))
            }
        } catch {
            logger.info("Daemon stopped")
            await daemon.stop()
        }
    }
}

/// Main daemon service that coordinates USB monitoring and network configuration via XPC
@available(macOS 14.0, *)
actor EdgeHelperDaemon {
    private let logger: Logger
    private let usbMonitor: USBMonitorService
    private let deviceDiscovery: DeviceDiscovery
    private let networkDaemonClient: NetworkDaemonClient
    private let ipManager: IPAddressManager
    private var isRunning = false

    init(
        usbMonitor: USBMonitorService,
        deviceDiscovery: DeviceDiscovery,
        networkDaemonClient: NetworkDaemonClient,
        ipManager: IPAddressManager,
        logger: Logger
    ) {
        self.logger = logger
        self.usbMonitor = usbMonitor
        self.deviceDiscovery = deviceDiscovery
        self.networkDaemonClient = networkDaemonClient
        self.ipManager = ipManager
    }

    func start() async throws {
        guard !isRunning else { return }

        logger.info("Starting EdgeOS Helper Daemon services...")

        // Initialize IP manager
        try await ipManager.initialize()

        // Set up device event handling
        await usbMonitor.setDeviceHandler { [weak self] event in
            Task { await self?.handleDeviceEvent(event) }
        }

        // Start USB monitoring
        do {
            try await usbMonitor.start()
        } catch {
            logger.error("Failed to start USB monitoring: \(error)")
            isRunning = false
            return
        }

        isRunning = true
        logger.info("EdgeOS Helper Daemon started successfully")
    }

    func stop() async {
        guard isRunning else { return }

        await usbMonitor.stop()
        isRunning = false
        logger.info("EdgeOS Helper Daemon stopped")
    }

    private func handleDeviceEvent(_ event: USBDeviceEvent) async {
        logger.info("Handling device event: \(event)")
        switch event {
        case .connected(let device) where device.isEdgeOS:
            logger.info("Connected EdgeOS device: \(device.name)")
            await configureEdgeOSDevice(device)
        case .disconnected(let device) where device.isEdgeOS:
            logger.info("Disconnected EdgeOS device: \(device.name)")
            await cleanupEdgeOSDevice(device)
        default:
            break
        }
    }

    private func configureEdgeOSDevice(_ device: USBDeviceInfo) async {
        logger.debug("Configuring EdgeOS device", metadata: ["device": .string(device.name)])

        do {
            // 1. Find network interfaces for this device
            let ethernetInterfaces = await deviceDiscovery.findEthernetInterfaces()
            let edgeOSInterfaces = ethernetInterfaces.filter { $0.isEdgeOSDevice }

            let interfaces = edgeOSInterfaces.map { interface in
                (name: interface.displayName, bsdName: interface.name, deviceId: device.id)
            }

            logger.debug(
                "Found \(interfaces.count) interfaces for device",
                metadata: [
                    "device": .string(device.name),
                    "interfaces": .array(interfaces.map { .string($0.bsdName) }),
                ]
            )

            // 2. Configure each interface via XPC
            for interface in interfaces {
                // Check if already configured via network daemon
                let isConfigured = try await networkDaemonClient.isInterfaceConfigured(
                    name: interface.name,
                    bsdName: interface.bsdName,
                    deviceId: interface.deviceId
                )

                if isConfigured {
                    logger.debug(
                        "Interface \(interface.bsdName) already configured",
                        metadata: [
                            "device": .string(device.name), "interface": .string(interface.bsdName),
                        ]
                    )
                    continue
                }

                // 3. Assign IP address
                let networkInterface = NetworkInterface(
                    name: interface.name,
                    bsdName: interface.bsdName,
                    deviceId: interface.deviceId
                )
                let ipConfig = try await ipManager.assignIPAddress(for: networkInterface)

                // 4. Apply network configuration via XPC to privileged daemon
                try await networkDaemonClient.configureInterface(
                    name: interface.name,
                    bsdName: interface.bsdName,
                    deviceId: interface.deviceId,
                    ipAddress: ipConfig.ipAddress,
                    subnetMask: ipConfig.subnetMask,
                    gateway: ipConfig.gateway
                )

                logger.debug(
                    "Configured interface \(interface.bsdName) with IP \(ipConfig.ipAddress)",
                    metadata: [
                        "device": .string(device.name), "interface": .string(interface.bsdName),
                        "ip": .string(ipConfig.ipAddress),
                    ]
                )
            }
        } catch {
            logger.error("Failed to configure EdgeOS device: \(error)")
        }
    }

    private func cleanupEdgeOSDevice(_ device: USBDeviceInfo) async {
        logger.info("Cleaning up EdgeOS device: \(device.name)")

        // Find and cleanup interfaces via XPC
        let ethernetInterfaces = await deviceDiscovery.findEthernetInterfaces()
        let edgeOSInterfaces = ethernetInterfaces.filter { $0.isEdgeOSDevice }

        for interface in edgeOSInterfaces {
            do {
                // Cleanup via network daemon
                try await networkDaemonClient.cleanupInterface(
                    name: interface.displayName,
                    bsdName: interface.name,
                    deviceId: device.id
                )

                // Release IP from local manager
                let networkInterface = NetworkInterface(
                    name: interface.displayName,
                    bsdName: interface.name,
                    deviceId: device.id
                )
                await ipManager.releaseIPAddress(for: networkInterface)

                logger.info("Cleaned up interface \(interface.name)")
            } catch {
                logger.error("Failed to cleanup interface \(interface.name): \(error)")
            }
        }
    }
}

@available(macOS 14.0, *)
@main
enum Main {
    static func main() async {
        await EdgeHelper.main()
    }
}
