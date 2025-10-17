import Foundation
import Testing

@testable import wendy_agent

@Suite("ConnMan")
struct ConnManTests {

    @Test("WiFiNetwork creation from ConnMan data")
    func testWiFiNetworkFromConnManData() {
        // Test creating WiFiNetwork with ConnMan-style data
        let network = WiFiNetwork(
            ssid: "TestNetwork",
            path: "/net/connman/service/wifi_test",
            signalStrength: 75,
            isSecured: true
        )

        #expect(network.ssid == "TestNetwork")
        #expect(network.path == "/net/connman/service/wifi_test")
        #expect(network.signalStrength == 75)
        #expect(network.isSecured == true)
    }

    @Test("Error handling for ConnMan operations")
    func testConnManErrorHandling() async {
        let connMan = ConnMan(uid: "1000", socketPath: "/invalid/socket")

        // Operations should throw appropriate errors when D-Bus is unavailable
        await #expect(throws: Error.self) {
            _ = try await connMan.listWiFiNetworks()
        }

        await #expect(throws: Error.self) {
            try await connMan.connectToNetwork(ssid: "TestNetwork", password: "password")
        }

        await #expect(throws: Error.self) {
            _ = try await connMan.getCurrentConnection()
        }

        await #expect(throws: Error.self) {
            _ = try await connMan.disconnectFromNetwork()
        }
    }
}
