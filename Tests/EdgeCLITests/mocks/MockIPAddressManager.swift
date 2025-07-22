import EdgeShared
import Foundation
import Logging

// Import the helper services
@testable import edge_helper

/// Mock IP address manager for testing
actor MockIPAddressManager: IPAddressManager {

    // Test control properties
    var assignedIPs: [String: IPConfiguration] = [:]
    var shouldFailInitialization = false
    var shouldFailAssignment = false
    var ipExhausted = false
    var nextIPAddress = "192.168.100.1"

    // Call tracking
    var initializeCallCount = 0
    var assignCallCount = 0
    var releaseCallCount = 0

    // IP assignment history
    private var assignmentHistory: [(NetworkInterface, IPConfiguration)] = []
    private var releaseHistory: [NetworkInterface] = []

    // IP range simulation
    private var currentRange = 100
    private let baseIP = "192.168"

    init() {}

    func initialize() async throws {
        initializeCallCount += 1

        if shouldFailInitialization {
            throw MockIPAddressManagerError.initializationFailed("Mock initialization failure")
        }

        // Simulate successful initialization
    }

    func assignIPAddress(for interface: NetworkInterface) async throws -> IPConfiguration {
        assignCallCount += 1

        if shouldFailAssignment {
            throw MockIPAddressManagerError.assignmentFailed("Mock assignment failure")
        }

        if ipExhausted {
            throw MockIPAddressManagerError.noAvailableRanges
        }

        // Check if interface already has an assigned IP
        if let existingConfig = assignedIPs[interface.bsdName] {
            return existingConfig
        }

        // Create new IP configuration
        let ipAddress = nextIPAddress
        let config = IPConfiguration(
            ipAddress: ipAddress,
            subnetMask: "255.255.255.0",
            gateway: nil
        )

        // Record the assignment
        assignedIPs[interface.bsdName] = config
        assignmentHistory.append((interface, config))

        // Update next IP address for subsequent assignments
        updateNextIPAddress()

        return config
    }

    func releaseIPAddress(for interface: NetworkInterface) async {
        releaseCallCount += 1

        // Remove the IP assignment
        assignedIPs.removeValue(forKey: interface.bsdName)
        releaseHistory.append(interface)
    }

    // Test helper methods
    private func updateNextIPAddress() {
        currentRange += 1
        nextIPAddress = "\(baseIP).\(currentRange).1"
    }

    func setNextIPAddress(_ ip: String) async {
        nextIPAddress = ip
    }

    func simulateIPExhaustion() async {
        ipExhausted = true
    }

    func getAssignedIPCount() async -> Int {
        return assignedIPs.count
    }

    func getAssignedIPs() async -> [String: IPConfiguration] {
        return assignedIPs
    }

    func getAssignmentHistory() async -> [(NetworkInterface, IPConfiguration)] {
        return assignmentHistory
    }

    func getReleaseHistory() async -> [NetworkInterface] {
        return releaseHistory
    }

    func isInterfaceAssigned(_ interface: NetworkInterface) async -> Bool {
        return assignedIPs[interface.bsdName] != nil
    }

    func resetCounts() async {
        initializeCallCount = 0
        assignCallCount = 0
        releaseCallCount = 0
        assignmentHistory.removeAll()
        releaseHistory.removeAll()
    }

    func reset() async {
        await resetCounts()
        assignedIPs.removeAll()
        shouldFailInitialization = false
        shouldFailAssignment = false
        ipExhausted = false
        nextIPAddress = "192.168.100.1"
        currentRange = 100
    }

    // Simulate specific IP assignment scenarios
    func simulateSpecificIPAssignment(_ interface: NetworkInterface, ip: String) async {
        let config = IPConfiguration(
            ipAddress: ip,
            subnetMask: "255.255.255.0",
            gateway: nil
        )
        assignedIPs[interface.bsdName] = config
    }

    func simulateConflictingIPRange(_ rangeStart: Int) async {
        // Simulate that a specific range is already in use
        let conflictIP = "\(baseIP).\(rangeStart).1"
        let tempInterface = NetworkInterface(
            name: "Conflict",
            bsdName: "conflict0",
            deviceId: "conflict"
        )
        await simulateSpecificIPAssignment(tempInterface, ip: conflictIP)
    }

    // Helper methods for setting test properties
    func setShouldFailInitialization(_ value: Bool) async {
        shouldFailInitialization = value
    }

    func setShouldFailAssignment(_ value: Bool) async {
        shouldFailAssignment = value
    }

    func setIPExhausted(_ value: Bool) async {
        ipExhausted = value
    }
}

/// Mock errors for IP address manager testing
enum MockIPAddressManagerError: Error, LocalizedError {
    case initializationFailed(String)
    case assignmentFailed(String)
    case noAvailableRanges
    case configurationFailed(String)

    var errorDescription: String? {
        switch self {
        case .initializationFailed(let message):
            return "Mock IP manager initialization failed: \(message)"
        case .assignmentFailed(let message):
            return "Mock IP assignment failed: \(message)"
        case .noAvailableRanges:
            return "Mock: No available IP address ranges"
        case .configurationFailed(let message):
            return "Mock IP configuration failed: \(message)"
        }
    }
}
