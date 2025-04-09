import ArgumentParser

struct AgentConnectionOptions: ParsableArguments {
    @Option(name: .long, help: "The host of the Edge Agent to connect to.")
    var agentHost: String

    @Option(name: .long, help: "The port of the Edge Agent to connect to.")
    var agentPort: Int = 50051
}
