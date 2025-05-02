#if os(macOS)
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import Network
    import SystemConfiguration
    struct PlatformDeviceDiscovery: DeviceDiscovery {
        private let ioServiceProvider: IOServiceProvider
        private let networkInterfaceProvider: NetworkInterfaceProvider

        init(
            ioServiceProvider: IOServiceProvider = DefaultIOServiceProvider(),
            networkInterfaceProvider: NetworkInterfaceProvider = DefaultNetworkInterfaceProvider()
        ) {
            self.ioServiceProvider = ioServiceProvider
            self.networkInterfaceProvider = networkInterfaceProvider
        }

        func findUSBDevices(logger: Logger) async -> [USBDevice] {
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
                    // Only track EdgeOS devices
                    if device.isEdgeOSDevice {
                        devices.append(device)
                        logger.info(
                            "EdgeOS device found",
                            metadata: ["deviceName": .string(device.name)]
                        )
                    }
                }

                ioServiceProvider.releaseIOObject(object: usbDevice)
                usbDevice = ioServiceProvider.getNextItem(iterator: iterator)
            }

            if devices.isEmpty {
                logger.info("No EdgeOS devices found.")
            }

            return devices
        }

        func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
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

                // Only collect interfaces containing "EdgeOS" in their name
                if !displayName.contains("EdgeOS") && !name.contains("EdgeOS") {
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
                logger.info("EdgeOS interface found", metadata: ["interface": .string(displayName)])
            }

            if interfaces.isEmpty {
                logger.info("No EdgeOS Ethernet interfaces found.")
            }

            return interfaces
        }
    }
#endif
