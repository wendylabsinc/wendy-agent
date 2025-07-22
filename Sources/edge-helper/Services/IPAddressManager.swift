import Foundation
import Logging

/// Protocol for managing IP address assignments for EdgeOS devices
protocol IPAddressManager: Sendable {
    func initialize() async throws
    func assignIPAddress(for interface: NetworkInterface) async throws -> IPConfiguration
    func releaseIPAddress(for interface: NetworkInterface) async
}

/// Platform-specific IP address manager implementation
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
        try scanExistingInterfaces()

        logger.info("IP address manager initialized with \(assignedRanges.count) existing ranges")
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
        let availableRange = try findAvailableRange()
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

    private func findAvailableRange() throws -> Int {
        for range in rangeStart...rangeEnd {
            let rangeKey = "\(baseOctet1).\(baseOctet2).\(range)"
            if !assignedRanges.contains(rangeKey) && !isRangeInUse(range) {
                return range
            }
        }

        throw IPAddressManagerError.noAvailableRanges
    }

    private func isRangeInUse(_ range: Int) -> Bool {
        // Check if the IP range is already in use on the system
        let testIP = "\(baseOctet1).\(baseOctet2).\(range).1"

        do {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/sbin/ifconfig")
            process.arguments = []

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let output = String(data: data, encoding: .utf8) ?? ""

            // Check if any interface has an IP in this range
            let rangePattern = "\(baseOctet1)\\.\(baseOctet2)\\.\(range)\\."
            return output.range(of: rangePattern, options: .regularExpression) != nil

        } catch {
            logger.warning("Failed to check IP range usage: \(error)")
            return false
        }
    }

    private func scanExistingInterfaces() throws {
        logger.debug("Scanning existing network interfaces for IP conflicts")

        do {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/sbin/ifconfig")
            process.arguments = []

            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe

            try process.run()
            process.waitUntilExit()

            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let output = String(data: data, encoding: .utf8) ?? ""

            // Parse ifconfig output for IP addresses in our managed range
            let lines = output.components(separatedBy: .newlines)
            var currentInterface: String?

            for line in lines {
                // Check for interface name
                if !line.hasPrefix("\t") && !line.hasPrefix(" ") && line.contains(":") {
                    let parts = line.components(separatedBy: ":")
                    currentInterface = parts.first?.trimmingCharacters(in: .whitespaces)
                }

                // Check for inet addresses in our range
                if line.contains("inet ") {
                    let pattern = "inet (\\d+\\.\\d+\\.\\d+\\.\\d+)"
                    if let regex = try? NSRegularExpression(pattern: pattern),
                        let match = regex.firstMatch(
                            in: line,
                            range: NSRange(line.startIndex..., in: line)
                        )
                    {
                        let ipRange = Range(match.range(at: 1), in: line)!
                        let ipAddress = String(line[ipRange])

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

                            if let interface = currentInterface {
                                interfaceToIP[interface] = ipAddress
                            }

                            logger.debug(
                                "Found existing IP \(ipAddress) on interface \(currentInterface ?? "unknown")"
                            )
                        }
                    }
                }
            }

        } catch {
            logger.warning("Failed to scan existing interfaces: \(error)")
            throw IPAddressManagerError.scanFailed(error.localizedDescription)
        }
    }
}

/// Errors related to IP address management
enum IPAddressManagerError: Error, LocalizedError {
    case noAvailableRanges
    case scanFailed(String)
    case configurationFailed(String)

    var errorDescription: String? {
        switch self {
        case .noAvailableRanges:
            return "No available IP address ranges in the 192.168.100.x - 192.168.199.x range"
        case .scanFailed(let message):
            return "Failed to scan existing network interfaces: \(message)"
        case .configurationFailed(let message):
            return "IP address configuration failed: \(message)"
        }
    }
}
