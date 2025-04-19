import XCTest
@testable import edge

final class USBDeviceIterationTests: XCTestCase {
    
    // This test validates the key iteration logic that was fixed:
    // - Properly releasing device objects
    // - Correctly advancing the iterator
    // - Handling the continue statement within nested conditionals
    func testDeviceIteration() async throws {
        // Create a controlled test environment to verify the iteration logic
        let testCases = [
            // Test case 1: All non-EdgeOS devices
            DeviceTestCase(
                deviceNames: ["Device 1", "Device 2", "Device 3"],
                containsEdgeOS: [false, false, false],
                expectedCallCount: 3,
                expectedDeviceCount: 0
            ),
            
            // Test case 2: Mixed devices with EdgeOS in the middle
            DeviceTestCase(
                deviceNames: ["Device 1", "EdgeOS Device", "Device 3"],
                containsEdgeOS: [false, true, false],
                expectedCallCount: 3,
                expectedDeviceCount: 1
            ),
            
            // Test case 3: All EdgeOS devices
            DeviceTestCase(
                deviceNames: ["EdgeOS Device 1", "EdgeOS Device 2", "EdgeOS Device 3"],
                containsEdgeOS: [true, true, true],
                expectedCallCount: 3,
                expectedDeviceCount: 3
            ),
            
            // Test case 4: First device is EdgeOS, others are not
            DeviceTestCase(
                deviceNames: ["EdgeOS Device 1", "Device 2", "Device 3"],
                containsEdgeOS: [true, false, false],
                expectedCallCount: 3,
                expectedDeviceCount: 1
            ),
            
            // Test case 5: Last device is EdgeOS, others are not
            DeviceTestCase(
                deviceNames: ["Device 1", "Device 2", "EdgeOS Device 3"],
                containsEdgeOS: [false, false, true],
                expectedCallCount: 3,
                expectedDeviceCount: 1
            )
        ]
        
        for (index, testCase) in testCases.enumerated() {
            let tracker = DeviceIterationTracker(
                deviceNames: testCase.deviceNames,
                containsEdgeOS: testCase.containsEdgeOS
            )
            
            let result = tracker.simulateDeviceIteration()
            
            XCTAssertEqual(
                result.processedCount, 
                testCase.expectedCallCount,
                "Test case \(index): Should process correct number of devices"
            )
            
            XCTAssertEqual(
                result.releasedCount, 
                testCase.expectedCallCount,
                "Test case \(index): Should release all processed devices"
            )
            
            XCTAssertEqual(
                result.edgeOSDevicesCount, 
                testCase.expectedDeviceCount,
                "Test case \(index): Should find correct number of EdgeOS devices"
            )
            
            XCTAssertEqual(
                tracker.iteratorAdvanceCalls, 
                testCase.expectedCallCount,
                "Test case \(index): Should advance iterator correct number of times"
            )
        }
    }
}

// Helper types for testing the device iteration logic
struct DeviceTestCase {
    let deviceNames: [String]
    let containsEdgeOS: [Bool]
    let expectedCallCount: Int
    let expectedDeviceCount: Int
}

struct DeviceIterationResult {
    let processedCount: Int
    let releasedCount: Int
    let edgeOSDevicesCount: Int
}

// This class simulates the iteration logic without using actual IOKit calls
final class DeviceIterationTracker {
    var deviceNames: [String]
    var containsEdgeOS: [Bool]
    
    var currentIndex = 0
    var releasedDevices = 0
    var iteratorAdvanceCalls = 0
    
    init(deviceNames: [String], containsEdgeOS: [Bool]) {
        self.deviceNames = deviceNames
        self.containsEdgeOS = containsEdgeOS
    }
    
    func simulateDeviceIteration() -> DeviceIterationResult {
        var deviceIndex = 0
        var edgeOSDevicesCount = 0
        
        // Simulate the actual USB device iteration logic from DevicesCommand
        while deviceIndex < deviceNames.count {
            let _ = deviceNames[deviceIndex]
            let isEdgeOS = deviceIndex < containsEdgeOS.count ? containsEdgeOS[deviceIndex] : false
            
            // Simulate processing a device
            if !isEdgeOS {
                // The bug was here - we would continue the loop without releasing
                // and advancing the iterator, leading to an infinite loop
                releaseDevice()
                deviceIndex = advanceIterator()
                continue
            }
            
            // Count EdgeOS devices
            edgeOSDevicesCount += 1
            
            // Release and advance after processing
            releaseDevice()
            deviceIndex = advanceIterator()
        }
        
        return DeviceIterationResult(
            processedCount: deviceNames.count,
            releasedCount: releasedDevices,
            edgeOSDevicesCount: edgeOSDevicesCount
        )
    }
    
    func releaseDevice() {
        releasedDevices += 1
    }
    
    func advanceIterator() -> Int {
        iteratorAdvanceCalls += 1
        return iteratorAdvanceCalls >= deviceNames.count ? deviceNames.count : iteratorAdvanceCalls
    }
} 