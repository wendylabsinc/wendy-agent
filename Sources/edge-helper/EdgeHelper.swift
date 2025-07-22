import ArgumentParser
import EdgeShared
import Foundation
import Logging

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
        logger.info("Foreground mode: \(foreground)")
        logger.info("Debug logging: \(debug)")

        let deviceDiscovery = PlatformDeviceDiscovery(logger: logger)
        let usbMonitor = PlatformUSBMonitor(
            deviceDiscovery: deviceDiscovery,
            logger: logger,
            pollingInterval: .seconds(30)
        )
        let networkConfig = PlatformNetworkConfiguration(
            deviceDiscovery: deviceDiscovery,
            logger: logger
        )
        let ipManager = PlatformIPAddressManager(logger: logger)
        // Create the main daemon service
        let daemon = EdgeHelperDaemon(
            usbMonitor: usbMonitor,
            networkConfig: networkConfig,
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

/// Main daemon service that coordinates USB monitoring and network configuration
actor EdgeHelperDaemon {
    private let logger: Logger
    private let usbMonitor: USBMonitorService
    private let networkConfig: NetworkConfigurationService
    private let ipManager: IPAddressManager
    private var isRunning = false

    init(
        usbMonitor: USBMonitorService,
        networkConfig: NetworkConfigurationService,
        ipManager: IPAddressManager,
        logger: Logger
    ) {
        self.logger = logger
        self.usbMonitor = usbMonitor
        self.networkConfig = networkConfig
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
            let interfaces = await networkConfig.findEdgeOSInterfaces(for: device)
            logger.debug("Found \(interfaces.count) interfaces for device", metadata: ["device": .string(device.name), "interfaces": .array(interfaces.map { .string($0.bsdName) })])

            // 2. Configure each interface
            for interface in interfaces {
                // Check if already configured
                let isConfigured = await networkConfig.isInterfaceConfigured(interface)
                if isConfigured {
                    logger.debug("Interface \(interface.bsdName) already configured", metadata: ["device": .string(device.name), "interface": .string(interface.bsdName)])
                    continue
                }

                // 3. Assign IP address
                let ipConfig = try await ipManager.assignIPAddress(for: interface)

                // 4. Apply network configuration
                try await networkConfig.configureInterface(interface, with: ipConfig)

                logger.debug("Configured interface \(interface.bsdName) with IP \(ipConfig.ipAddress)", metadata: ["device": .string(device.name), "interface": .string(interface.bsdName), "ip": .string(ipConfig.ipAddress)])
            }
        } catch {
            logger.error("Failed to configure EdgeOS device: \(error)")
        }
    }

    private func cleanupEdgeOSDevice(_ device: USBDeviceInfo) async {
        logger.info("Cleaning up EdgeOS device: \(device.name)")

        // Find and cleanup interfaces
        let interfaces = await networkConfig.findEdgeOSInterfaces(for: device)
        for interface in interfaces {
            do {
                try await networkConfig.cleanupInterface(interface)
                await ipManager.releaseIPAddress(for: interface)
                logger.info("Cleaned up interface \(interface.bsdName)")
            } catch {
                logger.error("Failed to cleanup interface \(interface.bsdName): \(error)")
            }
        }
    }
}

@main
enum Main {
    static func main() async {
        await EdgeHelper.main()
    }
}
