#if os(macOS)
    import AsyncDNSResolver
    import DNSClient
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
                logger.debug("No Wendy devices found.")
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
                logger.debug("Wendy interface found", metadata: ["interface": .string(displayName)])
            }

            if interfaces.isEmpty {
                logger.debug("No Wendy Ethernet interfaces found.")
            }

            return interfaces
        }

        public func findLANDevices() async throws -> [LANDevice] {
            let dns = try await DNSClient.connectMulticast(on: .singletonMultiThreadedEventLoopGroup).get()
            async let wendyPTR = try? await dns.sendQuery(forHost: "_wendy._udp.local", type: .ptr, timeout: .seconds(5)).get()
            async let edgePTR = try? await dns.sendQuery(forHost: "_edgeos._udp.local", type: .ptr, timeout: .seconds(5)).get()
            let messages = await [wendyPTR, edgePTR]
            logger.debug("Going to process answers to PTR query", metadata: ["answers": .stringConvertible(messages.count)])
            
            var interfaces: [LANDevice] = []
            for case .some(let message) in messages {
                let ptr = message.answers.compactMap { answer in
                    switch answer {
                    case .ptr(let ptr):
                        return ptr
                    default:
                        return nil
                    }
                }.first

                let srv = message.answers.compactMap { answer in
                    switch answer {
                    case .srv(let srv):
                        return srv
                    default:
                        return nil
                    }
                }.first

                let txt = message.answers.compactMap { answer in
                    switch answer {
                    case .txt(let txt):
                        return txt
                    default:
                        return nil
                    }
                }.first

                guard 
                    let ptr,
                    let srv,
                    let txt
                else {
                    logger.debug("Got no answers to PTR, SRV, or TXT query")
                    continue
                }

                let id = txt.resource.values.values.first ?? ""

                let lanDevice = LANDevice(
                    id: id,
                    displayName: "WendyOS Device",
                    hostname: srv.resource.domainName.string,
                    port: Int(srv.resource.port),
                    interfaceType: "LAN",
                    isWendyDevice: true
                )

                // Prevent duplicates
                if !interfaces.contains(where: { $0.id == id || $0.hostname == lanDevice.hostname }) {
                    interfaces.append(lanDevice)
                }
            }

            return interfaces
        }
    }
#endif
