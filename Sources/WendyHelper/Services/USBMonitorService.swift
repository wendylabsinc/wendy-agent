import Foundation
import Logging
import WendyShared

/// Service for monitoring USB device connections and disconnections
protocol USBMonitorService: Sendable {
    func start() async throws
    func stop() async
    func setDeviceHandler(_ handler: @escaping @Sendable (USBDeviceEvent) -> Void) async
}

/// USB device event types
enum USBDeviceEvent: Sendable {
    case connected(USBDeviceInfo)
    case disconnected(USBDeviceInfo)
}

/// Information about a detected USB device
struct USBDeviceInfo: Sendable, Hashable {
    let id: String
    let name: String
    let vendorId: String
    let productId: String
    let isWendyDevice: Bool

    init(from device: USBDevice) {
        self.id = device.vendorId + ":" + device.productId
        self.name = device.name
        self.vendorId = device.vendorId
        self.productId = device.productId
        self.isWendyDevice = device.isWendyDevice
    }
}

/// Platform-specific USB monitoring implementation
actor PlatformUSBMonitor: USBMonitorService {
    private let logger: Logger
    private let deviceDiscovery: DeviceDiscovery
    private let pollingInterval: Duration
    private var deviceHandler: (@Sendable (USBDeviceEvent) -> Void)?
    private var monitoringTask: Task<Void, Error>?
    private var lastKnownDevices: Set<USBDeviceInfo> = []

    init(deviceDiscovery: DeviceDiscovery, logger: Logger, pollingInterval: Duration) {
        self.logger = logger
        self.deviceDiscovery = deviceDiscovery
        self.pollingInterval = pollingInterval
    }

    func start() async throws {
        guard monitoringTask == nil else {
            logger.warning("USB monitoring is already running")
            return
        }

        logger.info("Starting USB device monitoring...")

        // Get initial device state
        let initialDevices = await deviceDiscovery.findUSBDevices()
        lastKnownDevices = Set(initialDevices.map(USBDeviceInfo.init))

        // Start monitoring task
        monitoringTask = Task {
            try await self.runMonitoringLoop()
        }

        logger.info("USB device monitoring started")
    }

    func stop() async {
        logger.info("Stopping USB device monitoring...")

        monitoringTask?.cancel()
        monitoringTask = nil

        logger.info("USB device monitoring stopped")
    }

    func setDeviceHandler(_ handler: @escaping @Sendable (USBDeviceEvent) -> Void) async {
        self.deviceHandler = handler
    }

    private func runMonitoringLoop() async throws {
        while !Task.isCancelled {
            do {
                logger.debug("Polling for USB devices")
                // Discover current USB devices
                let currentDevices = await deviceDiscovery.findUSBDevices()
                let currentDeviceInfos = Set(currentDevices.map(USBDeviceInfo.init))

                // Find newly connected devices
                let connectedDevices = currentDeviceInfos.subtracting(lastKnownDevices)
                for device in connectedDevices {
                    logger.debug("USB device connected: \(device.name)")
                    deviceHandler?(.connected(device))
                }

                // Find disconnected devices
                let disconnectedDevices = lastKnownDevices.subtracting(currentDeviceInfos)
                for device in disconnectedDevices {
                    logger.debug("USB device disconnected: \(device.name)")
                    deviceHandler?(.disconnected(device))
                }

                // Update known devices
                lastKnownDevices = currentDeviceInfos

                // Wait before next poll (now configurable)
                try await Task.sleep(for: pollingInterval)
            } catch {
                if !Task.isCancelled {
                    logger.error("Error during USB monitoring: \(error)")
                    try await Task.sleep(for: .seconds(5))
                }
            }
        }
    }
}
