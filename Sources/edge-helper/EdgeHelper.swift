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

        // Create the main daemon service
        let daemon = EdgeHelperDaemon(logger: logger)

        logger.info("Initializing daemon services...")
        await daemon.start()

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
    private let deviceDiscovery: DeviceDiscovery
    private var isRunning = false
    private var monitoringTask: Task<Void, Error>?

    init(logger: Logger) {
        self.logger = logger
        self.deviceDiscovery = PlatformDeviceDiscovery()
    }

    func start() async {
        guard !isRunning else {
            logger.warning("Daemon is already running")
            return
        }

        logger.info("Starting EdgeOS Helper Daemon services...")

        // Start periodic device scanning
        monitoringTask = Task {
            await self.runPeriodicScan()
        }

        isRunning = true
        logger.info("EdgeOS Helper Daemon started successfully")
    }

    func stop() async {
        guard isRunning else { return }

        logger.info("Stopping EdgeOS Helper Daemon...")

        // Stop monitoring
        monitoringTask?.cancel()

        isRunning = false
        logger.info("EdgeOS Helper Daemon stopped")
    }

    private func runPeriodicScan() async {
        while !Task.isCancelled {
            do {
                // Perform periodic device discovery
                await performDeviceDiscovery()

                // Wait 30 seconds before next scan
                try await Task.sleep(for: .seconds(30))
            } catch {
                logger.error("Error during periodic scan: \(error)")
                do {
                    try await Task.sleep(for: .seconds(5))
                } catch {
                    // If we can't even sleep, break out of the loop
                    break
                }
            }
        }
    }

    private func performDeviceDiscovery() async {
        logger.debug("Performing periodic device discovery...")

        // Discover current USB devices
        let usbDevices = await deviceDiscovery.findUSBDevices(logger: logger)
        let edgeOSDevices = usbDevices.filter { $0.isEdgeOSDevice }

        logger.debug("Found \(edgeOSDevices.count) EdgeOS USB devices")

        // For each EdgeOS device, log discovery (network configuration will be added later)
        for device in edgeOSDevices {
            logger.info(
                "EdgeOS device detected: \(device.name) (VID: \(device.vendorId), PID: \(device.productId))"
            )
        }
    }
}

@main
enum Main {
    static func main() async {
        await EdgeHelper.main()
    }
}
