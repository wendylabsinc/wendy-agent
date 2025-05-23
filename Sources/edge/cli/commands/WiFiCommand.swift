import ArgumentParser
import EdgeAgentGRPC
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging

struct WiFiCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "wifi",
        abstract: "Manage WiFi connections.",
        subcommands: [
            ListNetworksCommand.self,
            ConnectCommand.self,
            StatusCommand.self,
            DisconnectCommand.self,
        ]
    )

    struct ListNetworksCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "list",
            abstract: "List available WiFi networks."
        )

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            // Configure logger
            LoggingSystem.bootstrap { label in
                var handler = StreamLogHandler.standardError(label: label)
                #if DEBUG
                    handler.logLevel = .trace
                #else
                    handler.logLevel = .error
                #endif
                return handler
            }

            let logger = Logger(label: "edge.cli.wifi.list")
            logger.info("Listing available WiFi networks")

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
                let request = Edge_Agent_Services_V1_ListWiFiNetworksRequest()
                let response = try await agent.listWiFiNetworks(request)

                let networks = response.networks

                if networks.isEmpty {
                    if json {
                        print("[]")
                    } else {
                        print("No WiFi networks found.")
                    }
                    return
                }

                // Display networks
                if json {
                    let networksJSON = try formatNetworksAsJSON(networks)
                    print(networksJSON)
                } else {
                    formatNetworksAsText(networks)
                }
            }
        }

        private func formatNetworksAsJSON(
            _ networks: [Edge_Agent_Services_V1_ListWiFiNetworksResponse.WiFiNetwork]
        ) throws -> String {
            struct NetworkInfo: Codable {
                let ssid: String
                let signalStrength: Int?
            }

            let networkInfos = networks.map { network in
                NetworkInfo(
                    ssid: network.ssid,
                    signalStrength: network.hasSignalStrength ? Int(network.signalStrength) : nil
                )
            }

            let jsonData = try JSONEncoder().encode(networkInfos)
            return String(data: jsonData, encoding: .utf8)!
        }

        private func formatNetworksAsText(
            _ networks: [Edge_Agent_Services_V1_ListWiFiNetworksResponse.WiFiNetwork]
        ) {
            print("Available WiFi Networks:")
            print("------------------------")

            for (index, network) in networks.enumerated() {
                let signalInfo =
                    network.hasSignalStrength ? " (Signal: \(network.signalStrength))" : ""
                print("\(index + 1). \(network.ssid)\(signalInfo)")
            }
        }
    }

    struct ConnectCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "connect",
            abstract: "Connect to a WiFi network."
        )

        @Argument(help: "SSID (name) of the WiFi network to connect to")
        var ssid: String

        @Option(name: .shortAndLong, help: "Password for the WiFi network")
        var password: String

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            // Configure logger
            LoggingSystem.bootstrap { label in
                var handler = StreamLogHandler.standardError(label: label)
                #if DEBUG
                    handler.logLevel = .trace
                #else
                    handler.logLevel = .error
                #endif
                return handler
            }

            let logger = Logger(label: "edge.cli.wifi.connect")
            logger.info("Connecting to WiFi network", metadata: ["ssid": "\(ssid)"])

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)

                var request = Edge_Agent_Services_V1_ConnectToWiFiRequest()
                request.ssid = ssid
                request.password = password

                if !json {
                    print("Connecting to WiFi network: \(ssid)...")
                }

                let response = try await agent.connectToWiFi(request)

                if json {
                    struct Response: Codable {
                        let success: Bool
                        let errorMessage: String?
                    }

                    let responseJSON = try JSONEncoder().encode(Response(success: response.success, errorMessage: response.hasErrorMessage ? response.errorMessage : nil))
                    print(String(data: responseJSON, encoding: .utf8)!)
                } else if response.success {
                    print("✅ Successfully connected to \(ssid)")
                } else {
                    let errorMessage =
                        response.hasErrorMessage ? response.errorMessage : "Unknown error"
                    print("❌ Failed to connect to \(ssid): \(errorMessage)")
                }
            }
        }
    }

    struct StatusCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "status",
            abstract: "Check the current WiFi connection status."
        )

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            // Configure logger
            LoggingSystem.bootstrap { label in
                var handler = StreamLogHandler.standardError(label: label)
                #if DEBUG
                    handler.logLevel = .trace
                #else
                    handler.logLevel = .error
                #endif
                return handler
            }

            let logger = Logger(label: "edge.cli.wifi.status")
            logger.info("Checking WiFi connection status")

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
                let request = Edge_Agent_Services_V1_GetWiFiStatusRequest()
                let response = try await agent.getWiFiStatus(request)

                if json {
                    let statusJSON = try formatStatusAsJSON(response)
                    print(statusJSON)
                } else {
                    formatStatusAsText(response)
                }
            }
        }

        private func formatStatusAsJSON(
            _ response: Edge_Agent_Services_V1_GetWiFiStatusResponse
        ) throws -> String {
            struct StatusInfo: Codable {
                let connected: Bool
                let ssid: String?
                let errorMessage: String?
            }

            let statusInfo = StatusInfo(
                connected: response.connected,
                ssid: response.hasSsid ? response.ssid : nil,
                errorMessage: response.hasErrorMessage ? response.errorMessage : nil
            )

            let jsonData = try JSONEncoder().encode(statusInfo)
            return String(data: jsonData, encoding: .utf8)!
        }

        private func formatStatusAsText(_ response: Edge_Agent_Services_V1_GetWiFiStatusResponse) {
            print("WiFi Status:")
            print("------------")

            if response.connected {
                print("Status: Connected")
                if response.hasSsid {
                    print("Network: \(response.ssid)")
                }
            } else {
                print("Status: Disconnected")
            }

            if response.hasErrorMessage {
                print("Error: \(response.errorMessage)")
            }
        }
    }

    struct DisconnectCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "disconnect",
            abstract: "Disconnect from the current WiFi network."
        )

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            // Configure logger
            LoggingSystem.bootstrap { label in
                var handler = StreamLogHandler.standardError(label: label)
                #if DEBUG
                    handler.logLevel = .trace
                #else
                    handler.logLevel = .error
                #endif
                return handler
            }

            let logger = Logger(label: "edge.cli.wifi.disconnect")
            logger.info("Disconnecting from WiFi network")

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
                let request = Edge_Agent_Services_V1_DisconnectWiFiRequest()

                print("Disconnecting from WiFi network...")

                let response = try await agent.disconnectWiFi(request)

                if response.success {
                    print("✅ Successfully disconnected from WiFi network")
                } else {
                    let errorMessage =
                        response.hasErrorMessage ? response.errorMessage : "Unknown error"
                    print("❌ Failed to disconnect: \(errorMessage)")
                }
            }
        }
    }
}
