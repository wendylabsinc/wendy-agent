#if os(macOS)
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import Network
    import SystemConfiguration

    // macOS specific extension for USBDevice
    extension USBDevice {
        static func fromIORegistryEntry(_ device: io_service_t) -> USBDevice? {
            guard let nameRef = IORegistryEntryCreateCFProperty(
                device,
                "USB Product Name" as CFString,
                kCFAllocatorDefault,
                0
            ),
            let deviceName = nameRef.takeRetainedValue() as? String,
            let vendorIdRef = IORegistryEntryCreateCFProperty(
                device,
                "idVendor" as CFString,
                kCFAllocatorDefault,
                0
            ),
            let vendorId = vendorIdRef.takeRetainedValue() as? Int,
            let productIdRef = IORegistryEntryCreateCFProperty(
                device,
                "idProduct" as CFString,
                kCFAllocatorDefault,
                0
            ),
            let productId = productIdRef.takeRetainedValue() as? Int else {
                return nil
            }
            
            return USBDevice(name: deviceName, vendorId: vendorId, productId: productId)
        }
    }

    struct PlatformDeviceDiscovery: DeviceDiscovery {
        func findUSBDevices(logger: Logger) async -> [USBDevice] {
            var devices: [USBDevice] = []
            let matchingDict = IOServiceMatching(kIOUSBDeviceClassName)
            var iterator: io_iterator_t = 0
            defer { IOObjectRelease(iterator) }
            
            let result = IOServiceGetMatchingServices(kIOMainPortDefault, matchingDict, &iterator)
            if result != KERN_SUCCESS {
                logger.error(
                    "Error: Failed to get matching services",
                    metadata: ["result": .string(String(result))]
                )
                return devices
            }

            var usbDevice = IOIteratorNext(iterator)
            
            while usbDevice != 0 {
                if let device = USBDevice.fromIORegistryEntry(usbDevice) {
                    logger.debug("Found device", metadata: ["device": .string(device.toHumanReadableString())])
                    // Only track EdgeOS devices
                    if device.isEdgeOSDevice {
                        devices.append(device)
                        logger.info(
                            "EdgeOS device found",
                            metadata: ["deviceName": .string(device.name)]
                        )
                    }
                }

                IOObjectRelease(usbDevice)
                usbDevice = IOIteratorNext(iterator)
            }

            if devices.isEmpty {
                logger.info("No EdgeOS devices found.")
            }

            return devices
        }

        func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
            var interfaces: [EthernetInterface] = []
            
            guard let scInterfaces = SCNetworkInterfaceCopyAll() as? [SCNetworkInterface] else {
                logger.error("Failed to get network interfaces")
                return interfaces
            }

            for interface in scInterfaces {
                // Check if it's an Ethernet interface
                guard let interfaceType = SCNetworkInterfaceGetInterfaceType(interface) as? String,
                    interfaceType == kSCNetworkInterfaceTypeEthernet as String
                        || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String  // WiFi
                        || interfaceType == kSCNetworkInterfaceTypePPP as String
                        || interfaceType == kSCNetworkInterfaceTypeBond as String
                else {
                    continue
                }

                // Get interface details
                let name = SCNetworkInterfaceGetBSDName(interface) as? String ?? "Unknown"
                let displayName = SCNetworkInterfaceGetLocalizedDisplayName(interface) as? String ?? "Unknown"

                // Only collect interfaces containing "EdgeOS" in their name
                if !displayName.contains("EdgeOS") && !name.contains("EdgeOS") {
                    continue
                }

                // Get MAC address for physical interfaces
                var macAddress: String? = nil
                if interfaceType == kSCNetworkInterfaceTypeEthernet as String
                    || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String
                {
                    macAddress = SCNetworkInterfaceGetHardwareAddressString(interface) as? String
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
