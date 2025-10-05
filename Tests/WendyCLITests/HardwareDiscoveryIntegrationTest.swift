import Foundation
import Testing
import WendyAgentGRPC

@Suite("Hardware Discovery Integration Test")
struct HardwareDiscoveryIntegrationTest {

    @Test("Hardware discovery protobuf messages work correctly")
    func testHardwareDiscoveryProtobufMessages() throws {
        // This test verifies that the protobuf messages are properly generated and accessible

        // Test request message creation
        var request = WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesRequest()
        #expect(!request.hasCategoryFilter)

        request.categoryFilter = "gpu"
        #expect(request.hasCategoryFilter)
        #expect(request.categoryFilter == "gpu")

        // Test response message creation
        var response = WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse()
        #expect(response.capabilities.isEmpty)

        // Test hardware capability message creation
        var capability = WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse
            .HardwareCapability()
        capability.category = "test"
        capability.devicePath = "/dev/test0"
        capability.description_p = "Test device"
        capability.properties = ["key": "value"]

        response.capabilities = [capability]

        #expect(response.capabilities.count == 1)
        #expect(response.capabilities.first?.category == "test")
        #expect(response.capabilities.first?.devicePath == "/dev/test0")
        #expect(response.capabilities.first?.description_p == "Test device")
        #expect(response.capabilities.first?.properties["key"] == "value")
    }

    @Test("Expected protobuf structure is correct")
    func testProtobufStructure() throws {
        // Verify the protobuf messages have the expected structure
        let request = WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesRequest()
        let response = WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse()

        // Request should have optional categoryFilter
        #expect(type(of: request.categoryFilter) == String.self)

        // Response should have array of capabilities
        #expect(
            type(of: response.capabilities)
                == Array<
                    WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse
                        .HardwareCapability
                >.self
        )

        // Capability should have expected fields
        let capability = WendyAgentGRPC.Wendy_Agent_Services_V1_ListHardwareCapabilitiesResponse
            .HardwareCapability()
        #expect(type(of: capability.category) == String.self)
        #expect(type(of: capability.devicePath) == String.self)
        #expect(type(of: capability.description_p) == String.self)
        #expect(type(of: capability.properties) == Dictionary<String, String>.self)
    }
}
