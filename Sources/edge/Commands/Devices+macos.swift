#if os(macOS)
    import Foundation
    import Logging
    import IOKit
    import IOKit.usb
    import Network
    import SystemConfiguration

    struct PlatformDeviceDiscovery: DeviceDiscovery {
        func listUSBDevices(logger: Logger) async {
            let matchingDict = IOServiceMatching(kIOUSBDeviceClassName)
            var iterator: io_iterator_t = 0

            let result = IOServiceGetMatchingServices(kIOMainPortDefault, matchingDict, &iterator)
            if result != KERN_SUCCESS {
                logger.error(
                    "Error: Failed to get matching services",
                    metadata: ["result": .string(String(result))]
                )
                return
            }

            var usbDevice = IOIteratorNext(iterator)
            var foundEdgeOSDevices = false

            logger.info("USB Devices:")
            while usbDevice != 0 {
                logger.debug(
                    "usbDevice: \(usbDevice)",
                    metadata: ["usbDevice": .string(String(usbDevice))]
                )
                // Get device properties
                let deviceRef = IORegistryEntryCreateCFProperty(
                    usbDevice,
                    "USB Product Name" as CFString,
                    kCFAllocatorDefault,
                    0
                )
                if let deviceName = deviceRef?.takeRetainedValue() as? String {
                    logger.debug("Device found", metadata: ["deviceName": .string(deviceName)])
                    // Only display devices that include "EdgeOS" in their name
                    if !deviceName.contains("EdgeOS") {
                        IOObjectRelease(usbDevice)
                        usbDevice = IOIteratorNext(iterator)
                        continue
                    }
                    foundEdgeOSDevices = true
                    logger.info(
                        "EdgeOS device found",
                        metadata: ["deviceName": .string(deviceName)]
                    )

                    // Get vendor ID and product ID
                    let vendorIdRef = IORegistryEntryCreateCFProperty(
                        usbDevice,
                        "idVendor" as CFString,
                        kCFAllocatorDefault,
                        0
                    )
                    let productIdRef = IORegistryEntryCreateCFProperty(
                        usbDevice,
                        "idProduct" as CFString,
                        kCFAllocatorDefault,
                        0
                    )

                    if let vendorId = vendorIdRef?.takeRetainedValue() as? Int,
                        let productId = productIdRef?.takeRetainedValue() as? Int
                    {
                        print(
                            "\(deviceName) - Vendor ID: \(String(format: "0x%04X", vendorId)), Product ID: \(String(format: "0x%04X", productId))"
                        )
                    }
                }

                IOObjectRelease(usbDevice)
                usbDevice = IOIteratorNext(iterator)
            }

            if !foundEdgeOSDevices {
                logger.info("No EdgeOS devices found.")
            }

            IOObjectRelease(iterator)
        }

        func listEthernetInterfaces(logger: Logger) async {
            guard let interfaces = SCNetworkInterfaceCopyAll() as? [SCNetworkInterface] else {
                logger.error("Failed to get network interfaces")
                return
            }

            var foundEdgeOSInterfaces = false
            print("\nEthernet Interfaces:")

            for interface in interfaces {
                // Check if it's an Ethernet interface
                if let interfaceType = SCNetworkInterfaceGetInterfaceType(interface) as? String,
                    interfaceType == kSCNetworkInterfaceTypeEthernet as String
                        || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String  // WiFi
                        || interfaceType == kSCNetworkInterfaceTypePPP as String
                        || interfaceType == kSCNetworkInterfaceTypeBond as String
                {

                    // Get interface details
                    let name = SCNetworkInterfaceGetBSDName(interface) as? String ?? "Unknown"
                    let displayName =
                        SCNetworkInterfaceGetLocalizedDisplayName(interface) as? String ?? "Unknown"

                    // Only show interfaces containing "EdgeOS" in their name
                    if !displayName.contains("EdgeOS") && !name.contains("EdgeOS") {
                        continue
                    }

                    foundEdgeOSInterfaces = true
                    print("- \(displayName) (\(name)) [\(interfaceType)]")

                    // Get MAC address for physical interfaces
                    if interfaceType == kSCNetworkInterfaceTypeEthernet as String
                        || interfaceType == kSCNetworkInterfaceTypeIEEE80211 as String
                    {
                        if let macAddr = SCNetworkInterfaceGetHardwareAddressString(interface)
                            as? String
                        {
                            print("  MAC Address: \(macAddr)")
                        }
                    }
                }
            }

            if !foundEdgeOSInterfaces {
                print("No EdgeOS Ethernet interfaces found.")
            }
        }

        func listNetworkInterfaces() {
            let monitor = NWPathMonitor()
            let semaphore = DispatchSemaphore(value: 0)

            monitor.pathUpdateHandler = { path in
                if path.status == .satisfied {
                    if path.usesInterfaceType(.wifi) {
                        print("Connected via Wi-Fi")
                    } else if path.usesInterfaceType(.wiredEthernet) {
                        print("Connected via Ethernet")
                    } else if path.usesInterfaceType(.cellular) {
                        print("Connected via Cellular")
                    }

                    // List all available interfaces
                    for interface in path.availableInterfaces {
                        print("Interface: \(interface.name)")
                    }
                } else {
                    print("No connection")
                }

                // Signal that we're done processing the network information
                semaphore.signal()
            }

            // Start monitoring on a background queue
            let queue = DispatchQueue(label: "NetworkMonitor")
            monitor.start(queue: queue)

            // Wait for the path update handler to be called, with a timeout
            _ = semaphore.wait(timeout: .now() + 3.0)

            // Cancel the monitor
            monitor.cancel()
        }
    }
#endif
