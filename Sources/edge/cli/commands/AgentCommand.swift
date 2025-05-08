import ArgumentParser
import EdgeAgentGRPC
import Foundation
import GRPCCore
import GRPCNIOTransportHTTP2
import NIOFoundationCompat
import _NIOFileSystem

struct AgentCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
        commandName: "agent",
        abstract: "Manage the EdgeOS agent.",
        subcommands: [
            UpdateCommand.self
        ]
    )

    struct UpdateCommand: AsyncParsableCommand {
        static let configuration = CommandConfiguration(
            commandName: "update",
            abstract: "Update the EdgeOS agent."
        )

        @Option(help: "The path to the new version of the EdgeOS agent.")
        var binary: String

        @OptionGroup var agentConnectionOptions: AgentConnectionOptions

        func run() async throws {
            let agentEndpoint = agentConnectionOptions.agent

            let target = ResolvableTargets.DNS(
                host: agentEndpoint.host,
                port: agentEndpoint.port
            )

            #if os(macOS)
                let transport = try HTTP2ClientTransport.TransportServices(
                    target: target,
                    transportSecurity: .plaintext
                )
            #else
                let transport = try HTTP2ClientTransport.Posix(
                    target: target,
                    transportSecurity: .plaintext
                )
            #endif

            print("Connecting to EdgeOS agent at \(agentEndpoint.host):\(agentEndpoint.port)")
            try await withGRPCClient(transport: transport) { client in
                let agent = Edge_Agent_Services_V1_EdgeAgentService.Client(wrapping: client)
                print("Pushing update...")
                try await agent.updateAgent { writer in
                    print("Opening file...")
                    do {
                        try await FileSystem.shared.withFileHandle(forReadingAt: FilePath(binary)) {
                            handle in
                            print("Uploading binary...")
                            for try await chunk in handle.readChunks() {
                                try await writer.write(
                                    .with {
                                        $0.chunk = .with {
                                            $0.data = Data(buffer: chunk)
                                        }
                                    }
                                )
                            }

                            print("Finalizing update")
                            try await writer.write(
                                .with {
                                    $0.control = .with {
                                        $0.command = .update(.init())
                                    }
                                }
                            )
                        }
                    } catch {
                        print("Failed to upload binary: \(error)")
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
}
