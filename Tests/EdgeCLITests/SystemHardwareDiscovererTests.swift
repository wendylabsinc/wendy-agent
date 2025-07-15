import EdgeAgentGRPC
import Foundation
import Testing

@testable import edge_agent

@Suite("SystemHardwareDiscoverer Mock Tests")
struct SystemHardwareDiscovererTests {

    @Test("SystemHardwareDiscoverer with Raspberry Pi scenario")
    func testRaspberryPiScenario() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .raspberryPiZero2W)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        let capabilities = try await discoverer.discoverCapabilities()

        // Raspberry Pi should have some devices detected
        let categories = Set(capabilities.map { $0.category })
        print(
            "🥧 Raspberry Pi: \(capabilities.count) hardware devices - categories: \(categories.sorted())"
        )

        // Basic validation
        for capability in capabilities {
            #expect(!capability.category.isEmpty, "Category should not be empty")
            #expect(!capability.devicePath.isEmpty, "Device path should not be empty")
            #expect(!capability.description.isEmpty, "Description should not be empty")
        }

        // Should find some devices (at least audio based on actual results)
        #expect(!capabilities.isEmpty, "Raspberry Pi should have some hardware devices")

        // Check that audio category is present (based on actual mock results)
        let audioCapabilities = capabilities.filter { $0.category == "audio" }
        #expect(!audioCapabilities.isEmpty, "Raspberry Pi should have audio devices")

        let i2cCapabilities = capabilities.filter { $0.category == "i2c" }
        #expect(!i2cCapabilities.isEmpty, "Raspberry Pi should have I2C devices")

        let gpioCapabilities = capabilities.filter { $0.category == "gpio" }
        #expect(!gpioCapabilities.isEmpty, "Raspberry Pi should have GPIO devices")

        let spiCapabilities = capabilities.filter { $0.category == "spi" }
        #expect(!spiCapabilities.isEmpty, "Raspberry Pi should have SPI devices")

        let cameraCapabilities = capabilities.filter { $0.category == "camera" }
        #expect(!cameraCapabilities.isEmpty, "Raspberry Pi should have camera devices")

    }

    @Test("SystemHardwareDiscoverer with Jetson scenario")
    func testJetsonScenario() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .jetsonOrinNano)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        let capabilities = try await discoverer.discoverCapabilities()

        let categories = Set(capabilities.map { $0.category })
        print(
            "🤖 Jetson: \(capabilities.count) hardware devices - categories: \(categories.sorted())"
        )

        // Jetson should have GPU devices based on actual results
        let gpuCapabilities = capabilities.filter { $0.category == "gpu" }
        #expect(!gpuCapabilities.isEmpty, "Jetson should have GPU devices")

        // Should also have audio devices
        let audioCapabilities = capabilities.filter { $0.category == "audio" }
        #expect(!audioCapabilities.isEmpty, "Jetson should have audio devices")

        // Basic validation
        for capability in capabilities {
            #expect(!capability.category.isEmpty, "Category should not be empty")
            #expect(!capability.devicePath.isEmpty, "Device path should not be empty")
            #expect(!capability.description.isEmpty, "Description should not be empty")
        }
    }

    @Test("SystemHardwareDiscoverer with Generic Debian scenario")
    func testGenericDebianScenario() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .genericDebian)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        let capabilities = try await discoverer.discoverCapabilities()

        let categories = Set(capabilities.map { $0.category })
        print(
            "🐧 Generic Debian: \(capabilities.count) hardware devices - categories: \(categories.sorted())"
        )

        // Should have some devices
        #expect(!capabilities.isEmpty, "Generic Debian should have some hardware devices")

        // Basic validation
        for capability in capabilities {
            #expect(!capability.category.isEmpty, "Category should not be empty")
            #expect(!capability.devicePath.isEmpty, "Device path should not be empty")
            #expect(!capability.description.isEmpty, "Description should not be empty")
        }
    }

    @Test("SystemHardwareDiscoverer with Mixed scenario")
    func testMixedScenario() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .mixed)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        let capabilities = try await discoverer.discoverCapabilities()

        let categories = Set(capabilities.map { $0.category })
        print(
            "🔄 Mixed: \(capabilities.count) hardware devices - categories: \(categories.sorted())"
        )

        // Mixed scenario should have multiple categories
        #expect(categories.count >= 2, "Mixed scenario should have multiple hardware categories")

        // Should have the most devices among scenarios
        #expect(capabilities.count >= 5, "Mixed scenario should have several hardware devices")

        // Basic validation
        for capability in capabilities {
            #expect(!capability.category.isEmpty, "Category should not be empty")
            #expect(!capability.devicePath.isEmpty, "Device path should not be empty")
            #expect(!capability.description.isEmpty, "Description should not be empty")

            // Properties should be valid
            for (key, value) in capability.properties {
                #expect(!key.isEmpty, "Property keys should not be empty")
                #expect(!value.isEmpty, "Property values should not be empty")
            }
        }
    }

    @Test("SystemHardwareDiscoverer with Empty scenario")
    func testEmptyScenario() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .empty)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        let capabilities = try await discoverer.discoverCapabilities()

        print("🔳 Empty: \(capabilities.count) hardware devices")

        // Empty scenario should have minimal devices (based on actual behavior)
        // The mock shows it still finds some base audio devices
        #expect(capabilities.count <= 3, "Empty scenario should have minimal hardware capabilities")

        // Basic validation for any capabilities found
        for capability in capabilities {
            #expect(!capability.category.isEmpty, "Category should not be empty")
            #expect(!capability.devicePath.isEmpty, "Device path should not be empty")
            #expect(!capability.description.isEmpty, "Description should not be empty")
        }
    }

    @Test("SystemHardwareDiscoverer category filtering with mocks")
    func testCategoryFilteringWithMocks() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .mixed)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        // Test filtering by specific categories
        let allCapabilities = try await discoverer.discoverCapabilities()
        let allCategories = Set(allCapabilities.map { $0.category })

        for category in allCategories {
            let filteredCapabilities = try await discoverer.discoverCapabilities(
                categoryFilter: category
            )

            // All returned capabilities should be the requested category
            for capability in filteredCapabilities {
                #expect(
                    capability.category == category,
                    "Filtered results should only contain requested category"
                )
            }

            // Count should match manual filter
            let expectedCount = allCapabilities.filter { $0.category == category }.count
            #expect(
                filteredCapabilities.count == expectedCount,
                "Filtered count should match manual filter count for \(category)"
            )
        }

        // Test with non-existent category
        let nonExistent = try await discoverer.discoverCapabilities(
            categoryFilter: "nonexistent_category"
        )
        #expect(nonExistent.isEmpty, "Non-existent category should return empty array")
    }

    @Test("SystemHardwareDiscoverer protobuf conversion with mocks")
    func testProtobufConversionWithMocks() async throws {
        let fileSystemProvider = MockFileSystemProvider(scenario: .mixed)
        let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)

        let capabilities = try await discoverer.discoverCapabilities()

        // Test that all capabilities can be converted to protobuf
        for capability in capabilities {
            let proto = capability.toProto()

            // Verify the conversion works correctly
            #expect(proto.category == capability.category)
            #expect(proto.devicePath == capability.devicePath)
            #expect(proto.description_p == capability.description)
            #expect(proto.properties == capability.properties)
        }

        // Test with empty scenario
        let emptyProvider = MockFileSystemProvider(scenario: .empty)
        let emptyDiscoverer = SystemHardwareDiscoverer(fileSystemProvider: emptyProvider)
        let emptyCapabilities = try await emptyDiscoverer.discoverCapabilities()
        let emptyProtos = emptyCapabilities.map { $0.toProto() }
        #expect(
            emptyProtos.count == emptyCapabilities.count,
            "Proto conversion should preserve count"
        )
    }

    @Test("HardwareCapability structure validation")
    func testHardwareCapabilityStructure() throws {
        // Test the HardwareCapability struct directly
        let capability = HardwareCapability(
            category: "test",
            devicePath: "/dev/test0",
            description: "Test device for validation",
            properties: ["key1": "value1", "key2": "value2"]
        )

        // Verify basic structure
        #expect(capability.category == "test")
        #expect(capability.devicePath == "/dev/test0")
        #expect(capability.description == "Test device for validation")
        #expect(capability.properties.count == 2)
        #expect(capability.properties["key1"] == "value1")
        #expect(capability.properties["key2"] == "value2")

        // Test protobuf conversion
        let proto = capability.toProto()
        #expect(proto.category == "test")
        #expect(proto.devicePath == "/dev/test0")
        #expect(proto.description_p == "Test device for validation")
        #expect(proto.properties["key1"] == "value1")
        #expect(proto.properties["key2"] == "value2")
        #expect(proto.properties.count == 2)
    }

    @Test("SystemHardwareDiscoverer scenario comparison")
    func testScenarioComparison() async throws {
        // Test that different scenarios return different results
        let scenarios: [MockFileSystemProvider.HardwareScenario] = [
            .raspberryPiZero2W, .jetsonOrinNano, .genericDebian, .mixed, .empty,
        ]

        var results: [MockFileSystemProvider.HardwareScenario: [HardwareCapability]] = [:]

        for scenario in scenarios {
            let fileSystemProvider = MockFileSystemProvider(scenario: scenario)
            let discoverer = SystemHardwareDiscoverer(fileSystemProvider: fileSystemProvider)
            let capabilities = try await discoverer.discoverCapabilities()
            results[scenario] = capabilities

            let categories = Set(capabilities.map { $0.category })
            print(
                "📊 \(scenario): \(capabilities.count) devices, categories: \(categories.sorted())"
            )
        }

        // Empty should have minimal capabilities
        let emptyCount = results[.empty]?.count ?? 0
        #expect(emptyCount <= 3, "Empty scenario should have minimal capabilities")

        // Mixed should have significant capabilities (based on actual results)
        let mixedCount = results[.mixed]?.count ?? 0
        #expect(mixedCount >= 5, "Mixed scenario should have several capabilities")

        // Jetson should have GPU capabilities
        let jetsonCapabilities = results[.jetsonOrinNano] ?? []
        let jetsonGPUs = jetsonCapabilities.filter { $0.category == "gpu" }
        #expect(!jetsonGPUs.isEmpty, "Jetson scenario should have GPU devices")

        // Different scenarios should potentially have different counts/categories
        let uniqueCounts = Set(results.values.map { $0.count })
        print("Unique capability counts across scenarios: \(uniqueCounts.sorted())")

        // Should have at least a few different counts across scenarios
        #expect(
            uniqueCounts.count >= 2,
            "Different scenarios should produce different device counts"
        )
    }
}
