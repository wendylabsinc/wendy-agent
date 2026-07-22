import Foundation
import GRPCCore
import Testing
import WendyAgentGRPC

@testable import WendyAgentCore

@Suite("WiFiController pure mapping")
struct WiFiControllerMappingTests {
    @Test("rssiToSignalStrength maps the usable dBm range to 0-100")
    func rssiMapping() {
        #expect(WiFiController.rssiToSignalStrength(-30) == 100)
        #expect(WiFiController.rssiToSignalStrength(-100) == 0)
        #expect(WiFiController.rssiToSignalStrength(-200) == 0)
        #expect(WiFiController.rssiToSignalStrength(0) == 100)
        #expect(WiFiController.rssiToSignalStrength(-65) == 50)
    }

    @Test("security round-trips through the proto enum")
    func securityRoundTrip() {
        let cases: [WiFiSecurity] = [
            .unspecified, .open, .wep, .wpaPersonal, .wpa2Personal, .wpa3Personal, .wpa2Enterprise,
        ]
        for security in cases {
            let proto = AgentService.protoSecurity(security)
            #expect(AgentService.modelSecurity(proto) == security)
        }
    }
}

private struct FakeWiFi: WiFiManaging {
    var statusValue = WiFiStatus(connected: false, ssid: nil, errorMessage: nil)
    var scanValue: [WiFiScanResult] = []
    var knownValue: [KnownWiFi] = []
    var connectValue = WiFiActionResult.ok

    func status() async -> WiFiStatus { statusValue }
    func scan() async throws -> [WiFiScanResult] { scanValue }
    func knownNetworks() async -> [KnownWiFi] { knownValue }
    func connect(
        ssid: String,
        password: String,
        security: WiFiSecurity?,
        hidden: Bool
    ) async
        -> WiFiActionResult
    { connectValue }
    func disconnect() async -> WiFiActionResult { .ok }
    func forget(ssid: String) async -> WiFiActionResult { .ok }
    func setPriority(ssid: String, priority: Int32) async -> WiFiActionResult { .ok }
    func reorder(ssids: [String]) async -> WiFiActionResult { .ok }
}

@Suite("AgentService Wi-Fi adapters")
struct WiFiAdapterTests {
    @Test("listWiFiNetworks maps provider results to proto")
    func listMapsResults() async throws {
        let fake = FakeWiFi(scanValue: [
            WiFiScanResult(
                ssid: "Home",
                rssiDbm: -55,
                signalStrength: 64,
                security: .wpa2Personal,
                isKnown: true,
                isConnected: true
            )
        ])
        let service = AgentService(wifi: fake)

        let response = try await service.listWiFiNetworks(
            request: ServerRequest(
                metadata: [:],
                message: Wendy_Agent_Services_V1_ListWiFiNetworksRequest()
            ),
            context: makeWiFiContext(method: "ListWiFiNetworks")
        )
        let networks = try response.message.networks
        #expect(networks.count == 1)
        #expect(networks[0].ssid == "Home")
        #expect(networks[0].rssiDbm == -55)
        #expect(networks[0].security == .wpa2Psk)
        #expect(networks[0].isKnown)
        #expect(networks[0].isConnected)
    }

    @Test("getWiFiStatus reports connected SSID")
    func statusMaps() async throws {
        let fake = FakeWiFi(
            statusValue: WiFiStatus(connected: true, ssid: "Home", errorMessage: nil)
        )
        let service = AgentService(wifi: fake)

        let response = try await service.getWiFiStatus(
            request: ServerRequest(
                metadata: [:],
                message: Wendy_Agent_Services_V1_GetWiFiStatusRequest()
            ),
            context: makeWiFiContext(method: "GetWiFiStatus")
        )
        #expect(try response.message.connected)
        #expect(try response.message.ssid == "Home")
    }

    @Test("connectToWiFi surfaces failure errorMessage")
    func connectFailure() async throws {
        let fake = FakeWiFi(connectValue: .failed("wrong password"))
        let service = AgentService(wifi: fake)

        var request = Wendy_Agent_Services_V1_ConnectToWiFiRequest()
        request.ssid = "Home"
        request.password = "bad"

        let response = try await service.connectToWiFi(
            request: ServerRequest(metadata: [:], message: request),
            context: makeWiFiContext(method: "ConnectToWiFi")
        )
        #expect(try !response.message.success)
        #expect(try response.message.errorMessage == "wrong password")
    }
}

private func makeWiFiContext(method: String) -> ServerContext {
    ServerContext(
        descriptor: MethodDescriptor(
            fullyQualifiedService: "wendy.agent.services.v1.WendyAgentService",
            method: method
        ),
        remotePeer: "in-process:test",
        localPeer: "in-process:test",
        cancellation: .init()
    )
}
