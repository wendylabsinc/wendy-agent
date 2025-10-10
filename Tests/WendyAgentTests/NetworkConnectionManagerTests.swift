import Foundation
import Testing
@testable import wendy_agent

@Suite("NetworkConnectionManager Types")
struct NetworkConnectionManagerTests {
    @Test("WiFiNetwork initialization")
    func testWiFiNetworkInit() {
        let network = WiFiNetwork(
            ssid: "TestNetwork",
            path: "/test/path",
            signalStrength: -50,
            isSecured: true
        )

        #expect(network.ssid == "TestNetwork")
        #expect(network.path == "/test/path")
        #expect(network.signalStrength == -50)
        #expect(network.isSecured == true)
    }

    @Test("WiFiNetwork with nil signal strength")
    func testWiFiNetworkNilSignal() {
        let network = WiFiNetwork(
            ssid: "OpenNetwork",
            path: "/open/path",
            signalStrength: nil,
            isSecured: false
        )

        #expect(network.ssid == "OpenNetwork")
        #expect(network.path == "/open/path")
        #expect(network.signalStrength == nil)
        #expect(network.isSecured == false)
    }

    @Test("WiFiConnection initialization")
    func testWiFiConnectionInit() {
        let connection = WiFiConnection(
            ssid: "ConnectedNetwork",
            connectionPath: "/connection/path",
            ipAddress: "192.168.1.100",
            state: .connected
        )

        #expect(connection.ssid == "ConnectedNetwork")
        #expect(connection.connectionPath == "/connection/path")
        #expect(connection.ipAddress == "192.168.1.100")
        #expect(connection.state == .connected)
    }

    @Test("WiFiConnection states")
    func testWiFiConnectionStates() {
        let states: [WiFiConnection.ConnectionState] = [
            .connected,
            .connecting,
            .disconnected,
            .failed
        ]

        for state in states {
            let connection = WiFiConnection(
                ssid: "TestSSID",
                connectionPath: "/path",
                state: state
            )
            #expect(connection.state == state)
        }
    }

    @Test("WiFiConnection with nil IP address")
    func testWiFiConnectionNilIP() {
        let connection = WiFiConnection(
            ssid: "NoIPNetwork",
            connectionPath: "/no/ip/path",
            ipAddress: nil,
            state: .connecting
        )

        #expect(connection.ssid == "NoIPNetwork")
        #expect(connection.ipAddress == nil)
        #expect(connection.state == .connecting)
    }
}