import ArgumentParser
import Crypto
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import NIOFoundationCompat
import Noora
import WendyAgentGRPC
import WendyCloudGRPC
import WendySDK
import X509
import _NIOFileSystem

struct DeviceCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "device",
        abstract: "Manage the Wendy device.",
        subcommands: [
            SetupCommand.self,
            AppsCommand.self,
            HardwareCommand.self,
            WiFiCommand.self,
            VersionCommand.self,
            UpdateCommand.self,
        ]
    )

    struct VersionCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "version",
            abstract: "Get the version of the Wendy agent."
        )

        @Flag(name: [.customShort("j"), .long], help: "Output in JSON format")
        var json: Bool = false

        @Flag(help: "Check for updates")
        var checkUpdates: Bool = false

        @Flag(help: "Check for pre-releases")
        var prerelease: Bool = false

        struct JSONOutput: Codable {
            let currentVersion: String
            let latestVersion: String?
        }

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let version = try await withAgentGRPCClient(
                agentConnectionOptions,
                title: "For which device do you want to get the agent version?"
            ) { client in
                let agent = Wendy_Agent_Services_V1_WendyAgentService.Client(wrapping: client)
                return try await agent.getAgentVersion(request: .init(message: .init()))
            }

            var latestVersion: String? = nil

            if checkUpdates {
                let releases = try await fetchReleases()
                if prerelease {
                    latestVersion = releases.first?.name
                } else {
                    latestVersion = releases.first(where: { $0.prerelease == false })?.name
                }
            }

            if json {
                let json = JSONOutput(currentVersion: version.version, latestVersion: latestVersion)
                let data = try JSONEncoder().encode(json)
                print(String(data: data, encoding: .utf8)!)
            } else {
                print("Current version: \(version.version)")
                if let latestVersion, version.version != latestVersion {
                    print("Update available: \(latestVersion)")
                } else if checkUpdates {
                    print("No update available")
                }
            }
        }
    }

    struct UpdateCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "update",
            abstract: "Update the Wendy agent."
        )

        @Option(help: "The path to the new version of the Wendy agent.")
        var binary: String?

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let binary: String

            if let location = self.binary {
                binary = location
            } else {
                binary = try await downloadLatestRelease().path
            }

            let success = try await withAgentGRPCClient(
                agentConnectionOptions,
                title: "Which device do you want to update?"
            ) { client in
                let agent = Agent(client: client)
                return try await agent.update(fromBinary: binary)
            }

            guard success else {
                Noora().error("Failed to update agent")
                Self.exit(withError: nil)
            }

            Noora().success("Agent updated successfully")
        }
    }

    struct SetupCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "setup",
            abstract: "Setup the Wendy agent."
        )

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let endpoint = try await withCloudGRPCClient(title: "Setup agent") { cloudClient in
                let orgs = try await cloudClient.listOrganizations()

                if orgs.isEmpty {
                    Noora().error("No organizations found")
                    Self.exit(withError: nil)
                }

                let org = Noora().singleChoicePrompt(
                    title: "Enroll device",
                    question: "Which organization do you want to enroll into?",
                    options: orgs
                )

                let name = Noora().textPrompt(
                    title: "Name your device",
                    prompt: "Name",
                    collapseOnAnswer: false
                )

                let certsAPI = Wendycloud_V1_CertificateService.Client(wrapping: cloudClient.grpc)
                let tokenResponse = try await certsAPI.createAssetEnrollmentToken(
                    .with {
                        $0.organizationID = org.id
                        $0.name = name
                    },
                    metadata: cloudClient.metadata
                )

                let endpoint = try await agentConnectionOptions.read(title: "Provisioning device")
                try await withAgentGRPCClient(endpoint, title: "Provisioning device") { client in
                    let agent = Agent(client: client)
                    try await agent.provision(
                        enrollmentToken: tokenResponse.enrollmentToken,
                        assetID: tokenResponse.assetID,
                        organizationID: org.id,
                        cloudHost: endpoint.host
                    )
                }
                return endpoint
            }

            func getWifiStatus() async throws -> Wendy_Agent_Services_V1_GetWiFiStatusResponse {
                while !Task.isCancelled {
                    do {
                        return try await withAgentGRPCClient(
                            endpoint,
                            title: "Checking agent status"
                        ) { client in
                            let agent = Wendy_Agent_Services_V1_WendyAgentService.Client(
                                wrapping: client
                            )
                            let response = try await agent.getAgentVersion(.init())
                            Noora().info("Agent is provisioned (version: \(response.version))")
                            return try await agent.getWiFiStatus(.init())
                        }
                    } catch {
                        continue  // Failed to check agent status, try again
                    }
                }

                throw CancellationError()
            }

            let status = try await getWifiStatus()

            try await withAgentGRPCClient(
                endpoint,
                title: "Listing available WiFi networks"
            ) { client in
                let agent = Agent(client: client)

                if !status.connected {
                    let setupWifi = Noora().yesOrNoChoicePrompt(
                        question: "Do you want to setup WiFi?",
                        collapseOnSelection: false
                    )

                    if setupWifi {
                        while !Task.isCancelled {
                            let ssid = try await agent.discoverSSID()

                            let password = Noora().textPrompt(
                                title: "Enter the password for the WiFi network",
                                prompt: "Password"
                            )

                            let result = try await agent.connectToWiFi(
                                ssid: ssid,
                                password: password
                            )

                            if result.success {
                                Noora().success("Connected to WiFi network \(ssid)")
                                break
                            } else {
                                Noora().error(
                                    "Failed to connect to WiFi network: \(result.errorMessage)"
                                )
                            }
                        }
                    }
                }

                let shouldUpdate = Noora().yesOrNoChoicePrompt(
                    question: "Do you want to update the agent?",
                    collapseOnSelection: false
                )

                guard shouldUpdate else {
                    return
                }

                let binary = try await downloadLatestRelease().path

                let success = try await withAgentGRPCClient(
                    endpoint,
                    title: "Which device do you want to update?"
                ) { client in
                    let agent = Agent(client: client)
                    return try await agent.update(fromBinary: binary)
                }

                guard success else {
                    Noora().error("Failed to update agent")
                    Self.exit(withError: nil)
                }

                Noora().success("Agent updated successfully")
            }
        }
    }
}

extension Wendycloud_V1_Organization: CustomStringConvertible {
    public var description: String {
        self.name
    }
}
