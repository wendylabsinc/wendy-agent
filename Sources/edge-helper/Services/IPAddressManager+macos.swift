#if os(macOS)
    import Darwin
    import Logging
    import EdgeShared

    /// Platform-specific IP address manager implementation using native macOS APIs
    actor PlatformIPAddressManager: IPAddressManager {
        private let logger: Logger
        private var assignedRanges: Set<String> = []
        private var interfaceToIP: [String: String] = [:]

        // Base IP range: 192.168.100.x to 192.168.199.x
        private let baseOctet1 = 192
        private let baseOctet2 = 168
        private let rangeStart = 100
        private let rangeEnd = 199

        init(logger: Logger) {
            self.logger = logger
        }

        func initialize() async throws {
            logger.info("Initializing IP address manager")

            // Scan for existing EdgeOS interfaces to avoid conflicts
            try await scanExistingInterfaces()

            logger.info(
                "IP address manager initialized with \(assignedRanges.count) existing ranges"
            )
        }

        func assignIPAddress(for interface: NetworkInterface) async throws -> IPConfiguration {
            // Check if this interface already has an assigned IP
            if let existingIP = interfaceToIP[interface.bsdName] {
                logger.info("Interface \(interface.name) already has assigned IP: \(existingIP)")
                return IPConfiguration(
                    ipAddress: existingIP,
                    subnetMask: "255.255.255.0",
                    gateway: nil
                )
            }

            // Find an available IP range
            let availableRange = try await findAvailableRange()
            let ipAddress = "\(baseOctet1).\(baseOctet2).\(availableRange).1"

            // Mark range as assigned
            assignedRanges.insert("\(baseOctet1).\(baseOctet2).\(availableRange)")
            interfaceToIP[interface.bsdName] = ipAddress

            logger.info("Assigned IP \(ipAddress) to interface \(interface.name)")

            return IPConfiguration(
                ipAddress: ipAddress,
                subnetMask: "255.255.255.0",
                gateway: nil
            )
        }

        func releaseIPAddress(for interface: NetworkInterface) async {
            guard let ipAddress = interfaceToIP[interface.bsdName] else {
                logger.debug("No IP address assigned to interface \(interface.name)")
                return
            }

            // Extract the range from the IP address
            let components = ipAddress.split(separator: ".")
            if components.count >= 3 {
                let range = "\(baseOctet1).\(baseOctet2).\(components[2])"
                assignedRanges.remove(range)
                interfaceToIP.removeValue(forKey: interface.bsdName)

                logger.info("Released IP \(ipAddress) from interface \(interface.name)")
            }
        }

        private func findAvailableRange() async throws -> Int {
            for range in rangeStart...rangeEnd {
                let rangeKey = "\(baseOctet1).\(baseOctet2).\(range)"

                // Check if range is not already assigned and not in use
                if !assignedRanges.contains(rangeKey) {
                    let inUse = await isRangeInUse(range)
                    if !inUse {
                        return range
                    }
                }
            }

            throw IPAddressManagerError.noAvailableRanges
        }

        private func isRangeInUse(_ range: Int) async -> Bool {
            // Use getifaddrs to check if the IP range is already in use on the system
            var ifaddrs: UnsafeMutablePointer<ifaddrs>?

            guard getifaddrs(&ifaddrs) == 0 else {
                logger.warning("Failed to get interface addresses for range check")
                return false
            }

            defer { freeifaddrs(ifaddrs) }

            var current = ifaddrs
            while current != nil {
                let addr = current!.pointee

                // Get interface name from the fixed-size character array
                let interfaceName = withUnsafePointer(to: addr.ifa_name) {
                    $0.withMemoryRebound(
                        to: CChar.self,
                        capacity: MemoryLayout.size(ofValue: addr.ifa_name)
                    ) {
                        String(cString: $0)
                    }
                }

                if let ifaAddr = addr.ifa_addr,
                    ifaAddr.pointee.sa_family == AF_INET
                {

                    let sockaddr = ifaAddr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) {
                        $0.pointee
                    }
                    let ip = inet_ntoa(sockaddr.sin_addr)
                    guard let ipCString = ip else {
                        current = addr.ifa_next
                        continue
                    }
                    let ipString = String(cString: ipCString)

                    // Check if this IP is in our target range
                    let components = ipString.split(separator: ".")
                    if components.count == 4,
                        components[0] == String(baseOctet1),
                        components[1] == String(baseOctet2),
                        let thirdOctet = Int(components[2]),
                        thirdOctet == range
                    {

                        logger.debug(
                            "Range \(range) is in use by interface \(interfaceName) with IP \(ipString)"
                        )
                        return true
                    }
                }

                current = addr.ifa_next
            }

            return false
        }

        private func scanExistingInterfaces() async throws {
            logger.debug("Scanning existing network interfaces for IP conflicts")

            var ifaddrs: UnsafeMutablePointer<ifaddrs>?

            guard getifaddrs(&ifaddrs) == 0 else {
                logger.warning("Failed to get interface addresses for scanning")
                throw IPAddressManagerError.scanFailed("Could not get interface addresses")
            }

            defer { freeifaddrs(ifaddrs) }

            var current = ifaddrs
            while current != nil {
                let addr = current!.pointee

                // Get interface name from the fixed-size character array
                let interfaceName = withUnsafePointer(to: addr.ifa_name) {
                    $0.withMemoryRebound(
                        to: CChar.self,
                        capacity: MemoryLayout.size(ofValue: addr.ifa_name)
                    ) {
                        String(cString: $0)
                    }
                }

                if let ifaAddr = addr.ifa_addr,
                    ifaAddr.pointee.sa_family == AF_INET
                {

                    let sockaddr = ifaAddr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) {
                        $0.pointee
                    }
                    let ip = inet_ntoa(sockaddr.sin_addr)
                    guard let ipCString = ip else {
                        current = addr.ifa_next
                        continue
                    }
                    let ipAddress = String(cString: ipCString)

                    // Check if it's in our managed range
                    let components = ipAddress.split(separator: ".")
                    if components.count == 4,
                        components[0] == String(baseOctet1),
                        components[1] == String(baseOctet2),
                        let thirdOctet = Int(components[2]),
                        thirdOctet >= rangeStart && thirdOctet <= rangeEnd
                    {

                        let rangeKey = "\(baseOctet1).\(baseOctet2).\(thirdOctet)"
                        assignedRanges.insert(rangeKey)
                        interfaceToIP[interfaceName] = ipAddress

                        logger.debug("Found existing IP \(ipAddress) on interface \(interfaceName)")
                    }
                }

                current = addr.ifa_next
            }
        }
    }
#endif
