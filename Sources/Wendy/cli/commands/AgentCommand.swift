import ArgumentParser
import Crypto
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import Logging
import Noora
import NIOFoundationCompat
import WendyAgentGRPC
import WendyCloudGRPC
import _NIOFileSystem
import WendySDK
import X509
import _NIOFileSystem

struct AgentCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "agent",
        abstract: "Manage the Wendy agent.",
        subcommands: [
            VersionCommand.self,
            ProvisionCommand.self,
            UpdateCommand.self,
            AppsCommand.self,
            SetupCommand.self,
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
            let version = try await withGRPCClient(
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

    struct ProvisionCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "provision",
            abstract: "Provision the EdgeOS agent to your organization."
        )

        @Argument(help: "The ID of the organisation to provision for")
        var organisationID: String

        // TODO: Remote CSR authority support.

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let name = try DistinguishedName {
                CommonName("sh")
                CommonName("wendy")
                CommonName(organisationID)
                CommonName(UUID().uuidString)
            }

            try await withGRPCClient(agentConnectionOptions) { client in
                let agent = Wendy_Agent_Services_V1_WendyProvisioningService.Client(wrapping: client)
                let authority = Authority(
                    privateKey: Certificate.PrivateKey(Curve25519.Signing.PrivateKey()),
                    name: name
                )
                let (stream, continuation) = AsyncStream<
                    Wendy_Agent_Services_V1_ProvisioningResponse
                >.makeStream()

                return try await agent.provision { writer in
                    try await writer.write(
                        .with {
                            $0.request = .startProvisioning(
                                .with {
                                    $0.organisationID = organisationID
                                }
                            )
                        }
                    )

                    for await message in stream {
                        switch message.request {
                        case .csr(let csr):
                            let signed = try authority.sign(
                                CertificateSigningRequest(derEncoded: Array(csr.csrDer)),
                                validUntil: Date().addingTimeInterval(3600)
                            )

                            print("Provisioning signed certificate..")
                            let certificate = try Data(signed.serializeAsPEM().derBytes)
                            try await writer.write(
                                .with {
                                    $0.request = .csr(
                                        .with {
                                            $0.certificateDer = certificate
                                        }
                                    )
                                }
                            )
                        case .none:
                            ()
                        }
                    }
                    print("Provisioning complete!")
                } onResponse: { response in
                    defer { continuation.finish() }
                    for try await response in response.messages {
                        continuation.yield(response)
                    }
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
            let logger = Logger(label: "sh.wendyengineer.agent.update")
            let binary: String

            if let location = self.binary {
                binary = location
            } else {
                binary = try await downloadLatestRelease().path
            }

            try await withGRPCClient(
                agentConnectionOptions,
                title: "Which device do you want to update?"
            ) { client in
                let agent = Wendy_Agent_Services_V1_WendyAgentService.Client(wrapping: client)
                print("Pushing update...")
                try await agent.updateAgent { writer in
                    logger.debug("Opening file...")
                    do {
                        try await FileSystem.shared.withFileHandle(forReadingAt: FilePath(binary)) {
                            handle in
                            logger.debug("Uploading binary...")
                            for try await chunk in handle.readChunks() {
                                try await writer.write(
                                    .with {
                                        $0.chunk = .with {
                                            $0.data = Data(buffer: chunk)
                                        }
                                    }
                                )
                            }

                            logger.debug("Finalizing update")
                            try await writer.write(
                                .with {
                                    $0.control = .with {
                                        $0.command = .update(.init())
                                    }
                                }
                            )
                        }
                    } catch {
                        logger.error("Failed to upload binary: \(error)")
                        throw error
                    }
                } onResponse: { response in
                    for try await event in response.messages {
                        switch event.responseType {
                        case .updated:
                            print("Agent is updated! Restarting the service.")
                            return
                        case .none:
                            ()
                        }
                    }
                    print("Agent is not updated")
                }
            }
        }
    }

    struct SetupCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "setup",
            abstract: "Setup the Wendy agent."
        )
        
        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        @Option
        var cloud = "cloud.wendy.sh"

        func run() async throws {
            let target = ResolvableTargets.DNS(
                host: cloud,
                port: 50051
            )
        
            let transport = try GRPCTransport(
                target: target,
                transportSecurity: .plaintext
            )

            return try await withToken(
                title: "Setup agent",
                cloud: cloud
            ) { token in
                return try await withGRPCClient(transport: transport) { client in
                    let token = Metadata(dictionaryLiteral: ("authorization", "Bearer \(token)"))
                    let orgsAPI = Wendycloud_V1_OrganizationService.Client(wrapping: client)
                    
                    let orgs = try await orgsAPI.listOrganizations(
                        .with {
                            $0.pageSize = 100
                        },
                        metadata: token
                    ).organizations
                    
                    let org = Noora().singleChoicePrompt(
                        title: "Enroll device",
                        question: "Which organization do you want to enroll into?",
                        options: orgs
                    )
                    
                    let certsAPI = Wendycloud_V1_CertificateService.Client(wrapping: client)
                    let enrollmentToken = try await certsAPI.createEnrollmentToken(
                        .with {
                            $0.organizationID = org.id
                        },
                        metadata: token
                    )
                    
                    print(enrollmentToken)
                }
            }
        }
    }
}

extension Wendycloud_V1_Organization: CustomStringConvertible {
    public var description: String {
        self.name
    }
}
