#if os(macOS)
    import AsyncDNSResolver
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import Network
    import SystemConfiguration

    public struct PlatformDeviceDiscovery: DeviceDiscovery {
        private let ioServiceProvider: IOServiceProvider
        private let networkInterfaceProvider: NetworkInterfaceProvider
        private let logger: Logger

        public init(
            ioServiceProvider: IOServiceProvider = DefaultIOServiceProvider(),
            networkInterfaceProvider: NetworkInterfaceProvider = DefaultNetworkInterfaceProvider(),
            logger: Logger
        ) {
            self.ioServiceProvider = ioServiceProvider
            self.networkInterfaceProvider = networkInterfaceProvider
            self.logger = logger
        }

        public func findUSBDevices() async -> [USBDevice] {
            var devices: [USBDevice] = []
            let matchingDict = ioServiceProvider.createMatchingDictionary(
                className: kIOUSBDeviceClassName
            )
            var iterator: io_iterator_t = 0
            defer { ioServiceProvider.releaseIOObject(object: iterator) }

            let result = ioServiceProvider.getMatchingServices(
                masterPort: kIOMainPortDefault,
                matchingDict: matchingDict,
                iterator: &iterator
            )

            if result != KERN_SUCCESS {
                logger.error(
                    "Error: Failed to get matching services",
                    metadata: ["result": .string(String(result))]
                )
                return devices
            }

            var usbDevice = ioServiceProvider.getNextItem(iterator: iterator)

            while usbDevice != 0 {
                if let device = USBDevice.fromIORegistryEntry(
                    usbDevice,
                    provider: ioServiceProvider
                ) {
                    logger.debug(
                        "Found device",
                        metadata: ["device": .string(device.toHumanReadableString())]
                    )
                    // Only track Wendy devices
                    if device.isWendyDevice {
                        devices.append(device)
                        logger.info(
                            "Wendy device found",
                            metadata: ["deviceName": .string(device.name)]
                        )
                    }
                }

                ioServiceProvider.releaseIOObject(object: usbDevice)
                usbDevice = ioServiceProvider.getNextItem(iterator: iterator)
            }

            if devices.isEmpty {
                logger.info("No Wendy devices found.")
            }

            return devices
        }

        public func findEthernetInterfaces() async -> [EthernetInterface] {
            var interfaces: [EthernetInterface] = []

            guard let scInterfaces = networkInterfaceProvider.copyAllNetworkInterfaces() else {
                logger.error("Failed to get network interfaces")
                return interfaces
            }

            for interface in scInterfaces {
                // Check if it's an Ethernet interface
                guard
                    let interfaceType = networkInterfaceProvider.getInterfaceType(
                        interface: interface
                    ),
                    interfaceType == kSCNetworkInterfaceTypeEthernet as String
                        || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String  // WiFi
                        || interfaceType == kSCNetworkInterfaceTypePPP as String
                        || interfaceType == kSCNetworkInterfaceTypeBond as String
                else {
                    continue
                }

                // Get interface details
                let name = networkInterfaceProvider.getBSDName(interface: interface) ?? "Unknown"
                let displayName =
                    networkInterfaceProvider.getLocalizedDisplayName(interface: interface)
                    ?? "Unknown"

                // Only collect interfaces containing "Wendy" in their name
                if !displayName.contains("Wendy") && !name.contains("Wendy") {
                    continue
                }

                // Get MAC address for physical interfaces
                var macAddress: String? = nil
                if interfaceType == kSCNetworkInterfaceTypeEthernet as String
                    || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String
                {
                    macAddress = networkInterfaceProvider.getHardwareAddressString(
                        interface: interface
                    )
                }

                let ethernetInterface = EthernetInterface(
                    name: name,
                    displayName: displayName,
                    interfaceType: interfaceType,
                    macAddress: macAddress
                )

                interfaces.append(ethernetInterface)
                logger.info("Wendy interface found", metadata: ["interface": .string(displayName)])
            }

            if interfaces.isEmpty {
                logger.info("No Wendy Ethernet interfaces found.")
            }

            return interfaces
        }

        public func findLANDevices() async throws -> [LANDevice] {
            var interfaces: [LANDevice] = []

            let resolver = try AsyncDNSResolver()
            let ptrWendy = try await resolver.queryPTR(name: "_wendyos._udp.local")
            let ptrEdge = try await resolver.queryPTR(name: "_edgeos._udp.local")
            for name in (ptrWendy.names + ptrEdge.names) {
                guard
                    let srv = try await resolver.querySRV(name: name).first,
                    let txt = try await resolver.queryTXT(name: name).first,
                    let id = txt.txt.split(separator: "=").last.map(String.init)
                else {
                    continue
                }

                let lanDevice = LANDevice(
                    id: id,
                    displayName: "Wendy Device",
                    hostname: srv.host,
                    port: Int(srv.port),
                    interfaceType: "LAN",
                    isWendyDevice: true
                )

                // Prevent duplicates
                if !interfaces.contains(where: { $0.id == id || $0.hostname == srv.host }) {
                    interfaces.append(lanDevice)
                }
            }

            return interfaces
        }
    }
#endif
