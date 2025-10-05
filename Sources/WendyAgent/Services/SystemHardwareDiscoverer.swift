import WendyAgentGRPC
import Foundation
import Logging

/// Represents a hardware capability discovered on the system
public struct HardwareCapability: Sendable {
    let category: String
    let devicePath: String
    let description: String
    let properties: [String: String]

    public init(
        category: String,
        devicePath: String,
        description: String,
        properties: [String: String] = [:]
    ) {
        self.category = category
        self.devicePath = devicePath
        self.description = description
        self.properties = properties
    }

    /// Convert to protobuf format
    public func toProto()
        -> Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse.HardwareCapability
    {
        return .with {
            $0.category = category
            $0.devicePath = devicePath
            $0.description_p = description
            $0.properties = properties
        }
    }
}

/// Cross-platform hardware discovery service
public struct SystemHardwareDiscoverer: Sendable {
    private let logger = Logger(label: "SystemHardwareDiscoverer")
    private let fileSystemProvider: any FileSystemProvider

    public init(fileSystemProvider: any FileSystemProvider = DefaultFileSystemProvider()) {
        self.fileSystemProvider = fileSystemProvider
    }

    /// Discover all available hardware capabilities
    public func discoverCapabilities(
        categoryFilter: String? = nil
    ) async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // Discover different categories of hardware
        let discoveryMethods: [(String, () async throws -> [HardwareCapability])] = [
            ("gpu", discoverGPUDevices),
            ("usb", discoverUSBDevices),
            ("i2c", discoverI2CDevices),
            ("spi", discoverSPIDevices),
            ("gpio", discoverGPIODevices),
            ("camera", discoverCameraDevices),
            ("audio", discoverAudioDevices),
            ("input", discoverInputDevices),
            ("serial", discoverSerialDevices),
            ("network", discoverNetworkDevices),
            ("storage", discoverStorageDevices),
        ]

        for (category, discoveryMethod) in discoveryMethods {
            // Apply category filter if specified
            if let filter = categoryFilter, filter != category {
                continue
            }

            do {
                let categoryCapabilities = try await discoveryMethod()
                capabilities.append(contentsOf: categoryCapabilities)
                logger.debug("Discovered \(categoryCapabilities.count) \(category) devices")
            } catch {
                logger.warning("Failed to discover \(category) devices: \(error)")
            }
        }

        logger.info("Discovered \(capabilities.count) total hardware capabilities")
        return capabilities
    }
}

// MARK: - Platform-specific Discovery Methods

extension SystemHardwareDiscoverer {

    /// Discover GPU devices (NVIDIA, Mali, etc.)
    private func discoverGPUDevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // Check for NVIDIA GPU devices
        let nvidiaDevices = [
            "/dev/nvidia0", "/dev/nvidia1", "/dev/nvidia2", "/dev/nvidiactl", "/dev/nvidia-uvm",
        ]
        for devicePath in nvidiaDevices {
            if deviceExists(devicePath) {
                let properties = gatherNVIDiaProperties(devicePath: devicePath)
                capabilities.append(
                    HardwareCapability(
                        category: "gpu",
                        devicePath: devicePath,
                        description: "NVIDIA GPU device",
                        properties: properties
                    )
                )
            }
        }

        // Check for Mali GPU (common on ARM devices)
        let maliDevice = "/dev/mali0"
        if deviceExists(maliDevice) {
            capabilities.append(
                HardwareCapability(
                    category: "gpu",
                    devicePath: maliDevice,
                    description: "Mali GPU device"
                )
            )
        }

        // Check for generic DRM devices
        let drmDevices = findDevicesByPattern("/dev/dri/card*")
        for devicePath in drmDevices {
            let properties = gatherDRMProperties(devicePath: devicePath)
            capabilities.append(
                HardwareCapability(
                    category: "gpu",
                    devicePath: devicePath,
                    description: "DRM graphics device",
                    properties: properties
                )
            )
        }

        return capabilities
    }

    /// Discover USB devices
    private func discoverUSBDevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // Simple USB device discovery by checking common device paths
        let usbDevicePaths = findDevicesByPattern("/dev/bus/usb/*/*")
        for devicePath in usbDevicePaths.prefix(10) {  // Limit to avoid too many results
            let pathComponents = devicePath.split(separator: "/")
            if pathComponents.count >= 4 {
                let busNum = String(pathComponents[pathComponents.count - 2])
                let devNum = String(pathComponents[pathComponents.count - 1])

                capabilities.append(
                    HardwareCapability(
                        category: "usb",
                        devicePath: devicePath,
                        description: "USB Device",
                        properties: [
                            "busnum": busNum,
                            "devnum": devNum,
                        ]
                    )
                )
            }
        }

        return capabilities
    }

    /// Discover I2C devices
    private func discoverI2CDevices() async throws -> [HardwareCapability] {
        let i2cDevices = findDevicesByPattern("/dev/i2c-*")
        return i2cDevices.map { devicePath in
            let busNumber = String(devicePath.dropFirst("/dev/i2c-".count))
            return HardwareCapability(
                category: "i2c",
                devicePath: devicePath,
                description: "I2C bus \(busNumber)",
                properties: ["bus_number": busNumber]
            )
        }
    }

    /// Discover SPI devices
    private func discoverSPIDevices() async throws -> [HardwareCapability] {
        let spiDevices = findDevicesByPattern("/dev/spidev*")
        return spiDevices.map { devicePath in
            let deviceName = String(devicePath.dropFirst("/dev/".count))
            return HardwareCapability(
                category: "spi",
                devicePath: devicePath,
                description: "SPI device \(deviceName)",
                properties: ["device_name": deviceName]
            )
        }
    }

    /// Discover GPIO devices
    private func discoverGPIODevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // Check for gpiochip devices
        let gpioDevices = findDevicesByPattern("/dev/gpiochip*")
        for devicePath in gpioDevices {
            let chipNumber = String(devicePath.dropFirst("/dev/gpiochip".count))
            let properties = gatherGPIOProperties(chipNumber: chipNumber)
            capabilities.append(
                HardwareCapability(
                    category: "gpio",
                    devicePath: devicePath,
                    description: "GPIO chip \(chipNumber)",
                    properties: properties
                )
            )
        }

        return capabilities
    }

    /// Discover camera devices
    private func discoverCameraDevices() async throws -> [HardwareCapability] {
        let videoDevices = findDevicesByPattern("/dev/video*")
        var capabilities: [HardwareCapability] = []

        for devicePath in videoDevices {
            let properties = gatherV4LProperties(devicePath: devicePath)
            capabilities.append(
                HardwareCapability(
                    category: "camera",
                    devicePath: devicePath,
                    description: properties["card"] ?? "Video capture device",
                    properties: properties
                )
            )
        }

        return capabilities
    }

    /// Discover audio devices
    private func discoverAudioDevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // ALSA sound devices
        let soundDevices = findDevicesByPattern("/dev/snd/*")
        for devicePath in soundDevices {
            let deviceName = String(devicePath.dropFirst("/dev/snd/".count))
            capabilities.append(
                HardwareCapability(
                    category: "audio",
                    devicePath: devicePath,
                    description: "ALSA sound device \(deviceName)"
                )
            )
        }

        return capabilities
    }

    /// Discover input devices (keyboards, mice, etc.)
    private func discoverInputDevices() async throws -> [HardwareCapability] {
        let inputDevices = findDevicesByPattern("/dev/input/event*")
        var capabilities: [HardwareCapability] = []

        for devicePath in inputDevices {
            let properties = gatherInputProperties(devicePath: devicePath)
            capabilities.append(
                HardwareCapability(
                    category: "input",
                    devicePath: devicePath,
                    description: properties["name"] ?? "Input device",
                    properties: properties
                )
            )
        }

        return capabilities
    }

    /// Discover serial devices
    private func discoverSerialDevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // TTY devices
        var ttyDevices: [String] = []
        ttyDevices.append(contentsOf: findDevicesByPattern("/dev/ttyUSB*"))
        ttyDevices.append(contentsOf: findDevicesByPattern("/dev/ttyACM*"))
        ttyDevices.append(contentsOf: findDevicesByPattern("/dev/ttyS*"))

        for devicePath in ttyDevices {
            let deviceName = String(devicePath.dropFirst("/dev/".count))
            capabilities.append(
                HardwareCapability(
                    category: "serial",
                    devicePath: devicePath,
                    description: "Serial device \(deviceName)"
                )
            )
        }

        return capabilities
    }

    /// Discover network devices
    private func discoverNetworkDevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // Basic network interface discovery by reading from /sys/class/net
        let netInterfaces = findNetworkInterfaces()
        for interfaceName in netInterfaces {
            if interfaceName == "lo" { continue }  // Skip loopback

            let properties = gatherNetworkProperties(interfaceName: interfaceName)
            capabilities.append(
                HardwareCapability(
                    category: "network",
                    devicePath: "/dev/\(interfaceName)",  // Virtual path for consistency
                    description: "\(properties["type"] ?? "Network") interface \(interfaceName)",
                    properties: properties
                )
            )
        }

        return capabilities
    }

    /// Discover storage devices
    private func discoverStorageDevices() async throws -> [HardwareCapability] {
        var capabilities: [HardwareCapability] = []

        // Block devices
        var blockDevices: [String] = []
        blockDevices.append(contentsOf: findDevicesByPattern("/dev/sd*"))
        blockDevices.append(contentsOf: findDevicesByPattern("/dev/nvme*"))
        blockDevices.append(contentsOf: findDevicesByPattern("/dev/mmcblk*"))

        for devicePath in blockDevices {
            // Only include whole devices, not partitions
            let deviceName = String(devicePath.dropFirst("/dev/".count))

            // Skip partitions based on device naming patterns
            if deviceName.contains("p") && deviceName.last?.isNumber == true {
                // Skip NVMe partitions like nvme0n1p1, nvme0n1p2
                continue
            } else if deviceName.hasPrefix("sd") && deviceName.count > 3
                && deviceName.last?.isNumber == true
            {
                // Skip SCSI/SATA partitions like sda1, sdb2
                continue
            } else if deviceName.hasPrefix("mmcblk") && deviceName.contains("p") {
                // Skip MMC partitions like mmcblk0p1, mmcblk0p2
                continue
            }

            let properties = gatherStorageProperties(devicePath: devicePath)
            capabilities.append(
                HardwareCapability(
                    category: "storage",
                    devicePath: devicePath,
                    description: properties["model"] ?? "Storage device",
                    properties: properties
                )
            )
        }

        return capabilities
    }
}

// MARK: - Helper Methods

extension SystemHardwareDiscoverer {

    /// Check if a device file exists
    private func deviceExists(_ path: String) -> Bool {
        return fileSystemProvider.fileExists(atPath: path)
    }

    /// Find devices matching a shell-style pattern
    private func findDevicesByPattern(_ pattern: String) -> [String] {
        // Simple implementation using FileManager for device discovery
        let basePath = String(pattern.prefix(while: { $0 != "*" }))
        let lastSlashIndex = basePath.lastIndex(of: "/") ?? basePath.startIndex
        let baseDir = String(basePath.prefix(upTo: lastSlashIndex))
        let prefix = String(basePath.suffix(from: basePath.index(after: lastSlashIndex)))

        logger.debug(
            "Finding devices by pattern",
            metadata: ["pattern": "\(pattern)", "baseDir": "\(baseDir)", "prefix": "\(prefix)"]
        )
        var devices: [String] = []

        do {
            let contents = try fileSystemProvider.contentsOfDirectory(atPath: baseDir)
            for item in contents {
                if item.hasPrefix(prefix.replacingOccurrences(of: "*", with: "")) {
                    let fullPath = baseDir + "/" + item
                    if deviceExists(fullPath) {
                        devices.append(fullPath)
                    }
                }
            }
        } catch {
            // Directory might not exist on some systems
            logger.warning("Failed to find devices by pattern", metadata: ["error": "\(error)"])
        }

        return devices.sorted()
    }

    /// Find network interfaces by reading /sys/class/net
    private func findNetworkInterfaces() -> [String] {
        do {
            return try fileSystemProvider.contentsOfDirectory(atPath: "/sys/class/net")
        } catch {
            return []
        }
    }

    /// Read a single line from a file, returning nil if file doesn't exist
    private func readSysFile(_ path: String) -> String? {
        do {
            return try fileSystemProvider.readFile(atPath: path)
        } catch {
            return nil
        }
    }

    /// Gather NVIDIA GPU properties
    private func gatherNVIDiaProperties(devicePath: String) -> [String: String] {
        var properties: [String: String] = [:]

        // Try to read NVIDIA GPU information from sysfs or nvidia-ml-py equivalent
        if devicePath.contains("nvidia") && !devicePath.contains("ctl")
            && !devicePath.contains("uvm")
        {
            // Extract GPU index
            if let gpuIndex = devicePath.last?.wholeNumberValue {
                properties["gpu_index"] = String(gpuIndex)
            }
        }

        return properties
    }

    /// Gather DRM device properties
    private func gatherDRMProperties(devicePath: String) -> [String: String] {
        var properties: [String: String] = [:]

        // Extract card number
        if let cardMatch = devicePath.range(of: #"card(\d+)"#, options: .regularExpression) {
            let cardNum = String(devicePath[cardMatch]).replacingOccurrences(of: "card", with: "")
            properties["card_number"] = cardNum
        }

        return properties
    }

    /// Gather GPIO chip properties
    private func gatherGPIOProperties(chipNumber: String) -> [String: String] {
        var properties: [String: String] = [:]
        properties["chip_number"] = chipNumber

        if let label = readSysFile("/sys/class/gpio/gpiochip\(chipNumber)/label") {
            properties["label"] = label
        }

        if let ngpio = readSysFile("/sys/class/gpio/gpiochip\(chipNumber)/ngpio") {
            properties["ngpio"] = ngpio
        }

        return properties
    }

    /// Gather V4L2 (Video4Linux) properties
    private func gatherV4LProperties(devicePath: String) -> [String: String] {
        var properties: [String: String] = [:]

        // Extract device number
        if let deviceMatch = devicePath.range(of: #"video(\d+)"#, options: .regularExpression) {
            let deviceNum = String(devicePath[deviceMatch]).replacingOccurrences(
                of: "video",
                with: ""
            )
            properties["device_number"] = deviceNum
        }

        properties["device_caps"] = "capture"
        return properties
    }

    /// Gather input device properties
    private func gatherInputProperties(devicePath: String) -> [String: String] {
        var properties: [String: String] = [:]

        // Extract event number
        if let eventMatch = devicePath.range(of: #"event(\d+)"#, options: .regularExpression) {
            let eventNum = String(devicePath[eventMatch]).replacingOccurrences(
                of: "event",
                with: ""
            )
            properties["event_number"] = eventNum

            if let name = readSysFile("/sys/class/input/event\(eventNum)/device/name") {
                properties["name"] = name
            }
        }

        return properties
    }

    /// Gather network interface properties
    private func gatherNetworkProperties(interfaceName: String) -> [String: String] {
        var properties: [String: String] = [:]
        properties["interface"] = interfaceName

        if let type = readSysFile("/sys/class/net/\(interfaceName)/type") {
            // Convert type number to human-readable format
            switch type {
            case "1": properties["type"] = "Ethernet"
            case "772": properties["type"] = "WiFi"
            case "778": properties["type"] = "WiFi"
            default: properties["type"] = "Network"
            }
        }

        if let address = readSysFile("/sys/class/net/\(interfaceName)/address") {
            properties["mac_address"] = address
        }

        if let mtu = readSysFile("/sys/class/net/\(interfaceName)/mtu") {
            properties["mtu"] = mtu
        }

        return properties
    }

    /// Gather storage device properties
    private func gatherStorageProperties(devicePath: String) -> [String: String] {
        var properties: [String: String] = [:]

        let deviceName = String(devicePath.dropFirst("/dev/".count))
        properties["device_name"] = deviceName

        if let size = readSysFile("/sys/block/\(deviceName)/size") {
            // Convert 512-byte blocks to bytes
            if let sizeBlocks = Int(size) {
                let sizeBytes = sizeBlocks * 512
                properties["size_bytes"] = String(sizeBytes)
                properties["size_gb"] = String(format: "%.1f", Double(sizeBytes) / 1_000_000_000)
            }
        }

        if let model = readSysFile("/sys/block/\(deviceName)/device/model") {
            properties["model"] = model
        }

        if let vendor = readSysFile("/sys/block/\(deviceName)/device/vendor") {
            properties["vendor"] = vendor
        }

        return properties
    }
}
