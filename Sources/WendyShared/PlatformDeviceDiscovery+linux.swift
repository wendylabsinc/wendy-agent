#if os(Linux)
    import DNSClient
    import Foundation
    import Logging
    import Subprocess

    public struct PlatformDeviceDiscovery: DeviceDiscovery {
        private let logger: Logger

        public init(
            logger: Logger
        ) {
            self.logger = logger
        }

        public func findUSBDevices() async -> [USBDevice] {
            logger.info("Listing USB devices on Linux")
            var devices: [USBDevice] = []

            do {
                let result = try await Subprocess.run(
                    Subprocess.Executable.path("/usr/bin/lsusb"),
                    arguments: Subprocess.Arguments([String]()),
                    output: .string(limit: .max)
                )
                let output = result.standardOutput ?? ""

                for line in output.split(separator: "\n") {
                    let deviceInfo = String(line)
                    logger.debug("Found USB device: \(deviceInfo)")

                    // Parse the lsusb output format: "Bus XXX Device XXX: ID VVVV:PPPP Manufacturer Device"
                    if deviceInfo.contains("Wendy") {
                        // Extract vendor and product IDs
                        if let idRange = deviceInfo.range(
                            of: "ID \\S+",
                            options: String.CompareOptions.regularExpression
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
                                        "Found Wendy USB device: \(name)",
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
                logger.info("No Wendy USB devices found.")
            }

            return devices
        }

        public func findEthernetInterfaces() async -> [EthernetInterface] {
            logger.error("Listing Ethernet interfaces on Linux is not implemented")
            let interfaces: [EthernetInterface] = []
            return interfaces
        }

        public func findLANDevices() async throws -> [LANDevice] {
            let dns = try await DNSClient.connectMulticast(on: .singletonMultiThreadedEventLoopGroup).get()
            let ptr = try await dns.sendQuery(forHost: "_wendy._udp.local", type: .ptr).get()
            
            var interfaces: [LANDevice] = []
            for answer in ptr.answers {
                switch answer {
                case .ptr(let ptr):
                    let name = ptr.domainName.string
                    guard case .srv(let srv) = try await dns.sendQuery(forHost: name, type: .srv).get().answers.first else {
                        continue
                    }
                    guard case .txt(let txt) = try await dns.sendQuery(forHost: name, type: .txt).get().answers.first else {
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
                default:
                    continue
                }
            }

            return interfaces
        }
    }
#endif
