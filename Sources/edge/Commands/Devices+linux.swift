#if os(Linux)
    import Foundation
    import Logging
    import Shell

    struct PlatformDeviceDiscovery: DeviceDiscovery {
        func findUSBDevices(logger: Logger) async -> [USBDevice] {
            logger.info("Listing USB devices on Linux")
            var devices: [USBDevice] = []

            do {
                let output = try await Shell.run(["lsusb"])

                for line in output.split(separator: "\n") {
                    let deviceInfo = String(line)
                    logger.debug("Found USB device: \(deviceInfo)")

                    // Parse the lsusb output format: "Bus XXX Device XXX: ID VVVV:PPPP Manufacturer Device"
                    if deviceInfo.contains("EdgeOS") {
                        // Extract vendor and product IDs
                        if let idRange = deviceInfo.range(
                            of: "ID \\S+",
                            options: .regularExpression
                        ) {
                            let idStr = deviceInfo[idRange].dropFirst(3)  // Drop "ID "
                            let parts = idStr.split(separator: ":")

                            if parts.count == 2,
                                let vendorId = Int(parts[0], radix: 16),
                                let productId = Int(parts[1], radix: 16)
                            {

                                // Extract name - everything after the ID part
                                let nameStartIndex = deviceInfo.index(
                                    idRange.upperBound,
                                    offsetBy: 1
                                )
                                if nameStartIndex < deviceInfo.endIndex {
                                    let name = String(deviceInfo[nameStartIndex...])
                                        .trimmingCharacters(in: .whitespaces)

                                    devices.append(
                                        USBDevice(
                                            name: name,
                                            vendorId: vendorId,
                                            productId: productId
                                        )
                                    )

                                    logger.info(
                                        "Found EdgeOS USB device: \(name)",
                                        metadata: [
                                            "vendorId": .string(String(format: "0x%04X", vendorId)),
                                            "productId": .string(
                                                String(format: "0x%04X", productId)
                                            ),
                                        ]
                                    )
                                }
                            }
                        }
                    }
                }
            } catch {
                logger.error("Failed to list USB devices: \(error)")
            }

            if devices.isEmpty {
                logger.info("No EdgeOS USB devices found.")
            }

            return devices
        }

        func findEthernetInterfaces(logger: Logger) async -> [EthernetInterface] {
            logger.error("Listing Ethernet interfaces on Linux is not implemented")
            var interfaces: [EthernetInterface] = []
            return interfaces
        }
    }
#endif
